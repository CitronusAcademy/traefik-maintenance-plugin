package traefik_maintenance_plugin

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// sharedCache holds maintenance status for multiple environments
var (
	sharedCache struct {
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
	if sharedCache.initialized {
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

	var wg sync.WaitGroup
	if sharedCache.initialized && sharedCache.refresherRunning && sharedCache.stopCh != nil {
		wg.Add(1)
		oldStopCh := sharedCache.stopCh

		sharedCache.stopCh = make(chan struct{})

		sharedCache.Lock()
		shutdownInProgress := true
		sharedCache.Unlock()

		close(oldStopCh)

		go func() {
			shutdownTimer := time.NewTimer(500 * time.Millisecond)
			defer shutdownTimer.Stop()

			<-shutdownTimer.C

			sharedCache.Lock()
			if shutdownInProgress && sharedCache.refresherRunning {
				if debug {
					fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Refresher didn't shut down in time, forcing cleanup\n")
				}
				sharedCache.refresherRunning = false
			}
			sharedCache.Unlock()

			wg.Done()
		}()

		wg.Wait()
	} else {
		sharedCache.stopCh = make(chan struct{})
	}

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

	firstEnv := true
	for envSuffix := range environmentEndpoints {
		if firstEnv {
			// Первую среду загружаем синхронно
			var retryDelay time.Duration = 100 * time.Millisecond
			for i := 0; i < 5; i++ {
				if refreshMaintenanceStatusForEnvironment(envSuffix) {
					break
				}

				if debug {
					fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Initial fetch failed for environment '%s', retrying in %v\n", envSuffix, retryDelay)
				}
				time.Sleep(retryDelay)
				retryDelay *= 2
			}
			firstEnv = false
		} else {
			go func(env string) {
				var retryDelay time.Duration = 100 * time.Millisecond
				for i := 0; i < 5; i++ {
					if refreshMaintenanceStatusForEnvironment(env) {
						break
					}

					if debug {
						fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Initial fetch failed for environment '%s', retrying in %v\n", env, retryDelay)
					}
					select {
					case <-sharedCache.stopCh:
						return
					case <-time.After(retryDelay):
					}
					retryDelay *= 2
				}
			}(envSuffix)
		}
	}

	startBackgroundRefresher()
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
		envCache.lastSuccessfulFetch = time.Now()
	} else {
		envCache.expiry = time.Now().Add(duration)
		envCache.failedAttempts = failedAttempts
	}
}

func getMaintenanceStatusForDomain(domain string) (bool, []string) {
	sharedCache.RLock()
	defer sharedCache.RUnlock()

	if !sharedCache.initialized {
		return false, []string{}
	}

	var envSuffix string
	for suffix := range sharedCache.environmentEndpoints {
		if suffix == "" {
			continue
		}
		if strings.HasSuffix(domain, suffix) {
			envSuffix = suffix
			break
		}
	}

	if envSuffix == "" {
		envSuffix = ""
	}

	envCache, exists := sharedCache.environments[envSuffix]
	if !exists {
		return false, []string{}
	}

	if sharedCache.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Using cached status for environment '%s' (domain: %s): active=%v, whitelist count=%d\n",
			envSuffix, domain, envCache.isActive, len(envCache.whitelist))
	}

	whitelistCopy := make([]string, len(envCache.whitelist))
	copy(whitelistCopy, envCache.whitelist)

	return envCache.isActive, whitelistCopy
}

func getEndpointForDomain(domain string) string {
	sharedCache.RLock()
	defer sharedCache.RUnlock()

	for suffix, endpoint := range sharedCache.environmentEndpoints {
		if suffix == "" {
			continue
		}
		if strings.HasSuffix(domain, suffix) {
			return endpoint
		}
	}

	if defaultEndpoint, exists := sharedCache.environmentEndpoints[""]; exists {
		return defaultEndpoint
	}

	return "http://maintenance-service.admin/v1/configurations/"
}

// CloseSharedCache should be called if you need to clean up resources
func CloseSharedCache() {
	shutdownOnce.Do(func() {
		initLock.Lock()
		defer initLock.Unlock()

		if sharedCache.initialized && sharedCache.stopCh != nil {
			if sharedCache.debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Beginning shared cache cleanup\n")
			}

			close(sharedCache.stopCh)

			// Wait a bit for goroutines to terminate
			time.Sleep(200 * time.Millisecond)

			// Clear client to release connections
			sharedCache.client = nil
			sharedCache.initialized = false

			if sharedCache.debug {
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

	if sharedCache.initialized && sharedCache.stopCh != nil {
		close(sharedCache.stopCh)

		time.Sleep(300 * time.Millisecond)

		maxWait := 1 * time.Second
		startTime := time.Now()
		for time.Since(startTime) < maxWait {
			sharedCache.RLock()
			isRunning := sharedCache.refresherRunning
			sharedCache.RUnlock()

			if !isRunning {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	sharedCache = struct {
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
	}{}

	shutdownOnce = sync.Once{}
}
