package traefik_maintenance_plugin

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// sharedCacheState holds maintenance status for multiple environments.
type sharedCacheState struct {
	sync.RWMutex
	environments         map[string]*EnvironmentCache
	environmentEndpoints map[string]string
	environmentSecrets   map[string]EnvironmentSecret
	cacheDuration        time.Duration
	requestTimeout       time.Duration
	client               *http.Client
	debug                bool
	initialized          bool
	refresherRunning     bool
	stopCh               chan struct{}
	userAgent            string
	secretHeader         string
	secretHeaderValue    string
}

var (
	sharedCache  sharedCacheState
	initLock     sync.Mutex
	envLocksMu   sync.Mutex
	envLocks     = make(map[string]*sync.Mutex)
	shutdownOnce sync.Once // Ensure clean shutdown happens only once
)

// envLock returns the per-environment refresh lock for the given suffix,
// creating it on first use. A per-environment lock lets different
// environments refresh concurrently while serializing refreshes of the same
// environment — and, unlike the old global TryLock, a contended refresh never
// reports false success without actually fetching.
func envLock(suffix string) *sync.Mutex {
	envLocksMu.Lock()
	defer envLocksMu.Unlock()
	l, ok := envLocks[suffix]
	if !ok {
		l = &sync.Mutex{}
		envLocks[suffix] = l
	}
	return l
}

func ensureSharedCacheInitialized(environmentEndpoints map[string]string, environmentSecrets map[string]EnvironmentSecret, cacheDuration, requestTimeout time.Duration, debug bool, userAgent string, secretHeader, secretHeaderValue string) {
	sharedCache.RLock()
	alreadyInit := sharedCache.initialized
	sharedCache.RUnlock()
	if alreadyInit {
		return
	}

	initLock.Lock()
	defer initLock.Unlock()

	if sharedCache.initialized {
		return
	}

	if cacheDuration <= 0 {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Warning: invalid cache duration %v, using default of 10s\n", cacheDuration)
		}
		cacheDuration = 10 * time.Second
	}

	if requestTimeout <= 0 {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Warning: invalid request timeout %v, using default of 5s\n", requestTimeout)
		}
		requestTimeout = 5 * time.Second
	}

	sharedCache.stopCh = make(chan struct{})

	transport := &http.Transport{
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
	}

	client := &http.Client{
		Timeout:   requestTimeout,
		Transport: transport,
	}

	sharedCache.Lock()
	sharedCache.client = client
	sharedCache.environments = make(map[string]*EnvironmentCache)
	sharedCache.environmentEndpoints = environmentEndpoints
	sharedCache.environmentSecrets = environmentSecrets
	sharedCache.cacheDuration = cacheDuration
	sharedCache.requestTimeout = requestTimeout
	sharedCache.debug = debug
	sharedCache.userAgent = userAgent
	sharedCache.initialized = true
	sharedCache.refresherRunning = false
	sharedCache.secretHeader = secretHeader
	sharedCache.secretHeaderValue = secretHeaderValue
	sharedCache.Unlock()

	// Perform initial fetch for all environments
	if debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Performing initial fetch for all environments\n")
	}

	sharedCache.RLock()
	warmupStopCh := sharedCache.stopCh
	sharedCache.RUnlock()

	// Warm every environment before New returns, so no environment serves traffic
	// with an unpopulated cache during the startup window. Warm concurrently, then
	// wait for all. An environment whose API is unreachable still gives up after its
	// retries (staying fail-open) — that is the intended availability posture.
	var warmupWG sync.WaitGroup
	for envSuffix := range environmentEndpoints {
		warmupWG.Add(1)
		go func(suffix string) {
			defer warmupWG.Done()
			warmupEnvironment(suffix, debug, warmupStopCh)
		}(envSuffix)
	}
	warmupWG.Wait()

	startBackgroundRefresher()
}

// warmupEnvironment retries the initial fetch for one environment with
// exponential backoff (100ms, 200ms, ... up to 5 attempts). The wait between
// attempts selects on stopCh, so a shutdown during warmup returns promptly
// instead of blocking in time.Sleep.
func warmupEnvironment(envSuffix string, debug bool, stopCh <-chan struct{}) {
	retryDelay := 100 * time.Millisecond
	for i := 0; i < 5; i++ {
		if refreshMaintenanceStatusForEnvironment(envSuffix) {
			return
		}
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Initial fetch failed for environment '%s', retrying in %v\n", envSuffix, retryDelay)
		}
		select {
		case <-stopCh:
			return
		case <-time.After(retryDelay):
		}
		retryDelay *= 2
	}
}

func updateEnvironmentCache(envSuffix string, result *MaintenanceResponse, duration time.Duration, failedAttempts int, success bool) {
	sharedCache.Lock()
	defer sharedCache.Unlock()

	if sharedCache.environments == nil {
		sharedCache.environments = make(map[string]*EnvironmentCache)
	}

	envCache, exists := sharedCache.environments[envSuffix]
	if !exists {
		envCache = &EnvironmentCache{}
		sharedCache.environments[envSuffix] = envCache
	}

	if success && result != nil {
		envCache.isActive = result.SystemConfig.Maintenance.IsActive
		envCache.whitelist = make([]string, len(result.SystemConfig.Maintenance.Whitelist))
		copy(envCache.whitelist, result.SystemConfig.Maintenance.Whitelist)
		envCache.expiry = time.Now().Add(duration)
		envCache.failedAttempts = 0
	} else {
		envCache.expiry = time.Now().Add(duration)
		envCache.failedAttempts = failedAttempts
	}
}

// resolveEnvSuffix returns the longest non-empty configured suffix that matches
// domain, or "" if none match (the caller uses "" as the default-environment
// key). Longest-match makes routing deterministic regardless of Go's
// randomized map iteration order, so the status lookup and the endpoint lookup
// always agree on the environment.
func resolveEnvSuffix(domain string, endpoints map[string]string) string {
	best := ""
	for suffix := range endpoints {
		if suffix == "" {
			continue
		}
		if strings.HasSuffix(domain, suffix) && len(suffix) > len(best) {
			best = suffix
		}
	}
	return best
}

func getMaintenanceStatusForDomain(domain string) (bool, []string) {
	sharedCache.RLock()
	defer sharedCache.RUnlock()

	if !sharedCache.initialized {
		return false, []string{}
	}

	envSuffix := resolveEnvSuffix(domain, sharedCache.environmentEndpoints)

	envCache, exists := sharedCache.environments[envSuffix]
	if !exists {
		return false, []string{}
	}

	if sharedCache.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Using cached status for environment '%s' (domain: %s): active=%v, whitelist count=%d\n",
			envSuffix, domain, envCache.isActive, len(envCache.whitelist))
	}

	// The whitelist is only consulted while maintenance is active, so avoid the
	// per-request allocation+copy in the common (inactive) case.
	if !envCache.isActive {
		return false, nil
	}

	whitelistCopy := make([]string, len(envCache.whitelist))
	copy(whitelistCopy, envCache.whitelist)

	return true, whitelistCopy
}

func getEndpointForDomain(domain string) string {
	sharedCache.RLock()
	defer sharedCache.RUnlock()

	if suffix := resolveEnvSuffix(domain, sharedCache.environmentEndpoints); suffix != "" {
		return sharedCache.environmentEndpoints[suffix]
	}

	if defaultEndpoint, exists := sharedCache.environmentEndpoints[""]; exists {
		return defaultEndpoint
	}

	return defaultMaintenanceEndpoint
}

// CloseSharedCache should be called if you need to clean up resources
func CloseSharedCache() {
	shutdownOnce.Do(func() {
		initLock.Lock()
		defer initLock.Unlock()

		sharedCache.RLock()
		initialized := sharedCache.initialized
		stopCh := sharedCache.stopCh
		debug := sharedCache.debug
		sharedCache.RUnlock()

		if initialized && stopCh != nil {
			if debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Beginning shared cache cleanup\n")
			}

			close(stopCh)

			// Wait a bit for goroutines to terminate
			time.Sleep(200 * time.Millisecond)

			// Clear client/initialized under the cache lock — ServeHTTP and the
			// refresher read these fields under the RWMutex, so writing them under
			// initLock alone is a data race.
			sharedCache.Lock()
			sharedCache.client = nil
			sharedCache.initialized = false
			sharedCache.Unlock()

			if debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Shared cache resources cleaned up\n")
			}
		}
	})
}

// ResetSharedCacheForTesting resets all shared state for testing
func ResetSharedCacheForTesting() {
	initLock.Lock()
	defer initLock.Unlock()

	envLocksMu.Lock()
	envLocks = make(map[string]*sync.Mutex)
	envLocksMu.Unlock()

	// Signal the refresher to stop. CloseSharedCache may have already closed
	// stopCh (and cleared initialized); only close it here if it is still open,
	// to avoid a double close.
	if sharedCache.initialized && sharedCache.stopCh != nil {
		close(sharedCache.stopCh)
	}

	// Always wait (under the RWMutex) for the refresher goroutine to observe the
	// stop and clear refresherRunning before overwriting sharedCache. Reading the
	// flag under RLock establishes a happens-before edge with the goroutine's
	// final write, so the lock-free wholesale reset below cannot race it — this
	// holds even when CloseSharedCache already cleared initialized.
	maxWait := 1 * time.Second
	startTime := time.Now()
	for time.Since(startTime) < maxWait {
		sharedCache.RLock()
		isRunning := sharedCache.refresherRunning
		sharedCache.RUnlock()

		if !isRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	sharedCache = sharedCacheState{}

	shutdownOnce = sync.Once{}
}
