// Package maintenance keeps a background-refreshed cache of per-environment
// maintenance status. The maintenance API is never called on the request path:
// a single background goroutine polls each configured environment and writes to
// a process-global cache that requests only read.
package maintenance

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/logx"
)

// DefaultEndpoint is the maintenance-API URL used as a last-resort fallback when
// no environment endpoint is configured for a domain.
const DefaultEndpoint = "http://maintenance-service/v1/configurations/"

// Secret carries the header name and value used to authenticate to a
// maintenance API. A secret-gated API returns the whitelist only when the value
// matches its stored token.
type Secret struct {
	Header string
	Value  string
}

// maintenanceResponse is the decoded maintenance-API payload. The JSON field
// names are a fixed contract with the maintenance service.
type maintenanceResponse struct {
	SystemConfig *struct {
		Maintenance *struct {
			IsActive  bool     `json:"is_active"`
			Whitelist []string `json:"whitelist"`
		} `json:"maintenance"`
	} `json:"system_config"`
}

// environmentCache is the cached maintenance state for a single environment.
type environmentCache struct {
	isActive       bool
	whitelist      []string
	expiry         time.Time
	failedAttempts int
}

// sharedCacheState holds maintenance status for multiple environments.
type sharedCacheState struct {
	sync.RWMutex
	environments         map[string]*environmentCache
	environmentEndpoints map[string]string
	environmentSecrets   map[string]Secret
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

// Init populates the shared cache from configuration and starts the background
// refresher. It warms every environment synchronously before returning, so no
// environment serves traffic with an unpopulated cache. It is idempotent: only
// the first call per process initializes; later calls return immediately.
func Init(endpoints map[string]string, secrets map[string]Secret, topHeader, topValue string, cacheDuration, requestTimeout time.Duration, debug bool, userAgent string) {
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
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Warning: invalid cache duration %v, using default of 10s\n", cacheDuration)
		}
		cacheDuration = 10 * time.Second
	}

	if requestTimeout <= 0 {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Warning: invalid request timeout %v, using default of 5s\n", requestTimeout)
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
	sharedCache.environments = make(map[string]*environmentCache)
	sharedCache.environmentEndpoints = endpoints
	sharedCache.environmentSecrets = secrets
	sharedCache.cacheDuration = cacheDuration
	sharedCache.requestTimeout = requestTimeout
	sharedCache.debug = debug
	sharedCache.userAgent = userAgent
	sharedCache.initialized = true
	sharedCache.refresherRunning = false
	sharedCache.secretHeader = topHeader
	sharedCache.secretHeaderValue = topValue
	sharedCache.Unlock()

	warnMissingSecrets(endpoints, secrets, topHeader, topValue)

	warmupAllEnvironments(endpoints, debug)

	startBackgroundRefresher()
}

// warnMissingSecrets logs a startup warning for any environment that has
// neither a per-environment secret nor a usable top-level secret. A
// secret-gated API returns no whitelist for such an environment, which would
// lock out every operator the instant maintenance activates. Diagnostic only;
// this changes no behavior.
func warnMissingSecrets(endpoints map[string]string, secrets map[string]Secret, topHeader, topValue string) {
	for envSuffix := range endpoints {
		perEnv, ok := secrets[envSuffix]
		hasPerEnv := ok && perEnv.Value != ""
		hasTopLevel := topHeader != "" && topValue != ""
		if !hasPerEnv && !hasTopLevel {
			label := envSuffix
			if label == "" {
				label = "(default)"
			}
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Warning: environment '%s' has no secret configured; a secret-gated API will return no whitelist and block all clients during maintenance\n", label)
		}
	}
}

// warmupAllEnvironments fetches every environment's maintenance status before
// Init returns, so no environment serves traffic with an unpopulated cache
// during the startup window. Environments warm concurrently and the call waits
// for all of them. An environment whose API is unreachable still gives up after
// its retries (staying fail-open) — that is the intended availability posture.
func warmupAllEnvironments(endpoints map[string]string, debug bool) {
	if debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Performing initial fetch for all environments\n")
	}

	sharedCache.RLock()
	warmupStopCh := sharedCache.stopCh
	sharedCache.RUnlock()

	var warmupWG sync.WaitGroup
	for envSuffix := range endpoints {
		warmupWG.Add(1)
		go func(suffix string) {
			defer warmupWG.Done()
			warmupEnvironment(suffix, debug, warmupStopCh)
		}(envSuffix)
	}
	warmupWG.Wait()
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
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Initial fetch failed for environment '%s', retrying in %v\n", envSuffix, retryDelay)
		}
		select {
		case <-stopCh:
			return
		case <-time.After(retryDelay):
		}
		retryDelay *= 2
	}
}

func updateEnvironmentCache(envSuffix string, result *maintenanceResponse, duration time.Duration, failedAttempts int, success bool) {
	sharedCache.Lock()
	defer sharedCache.Unlock()

	if sharedCache.environments == nil {
		sharedCache.environments = make(map[string]*environmentCache)
	}

	envCache, exists := sharedCache.environments[envSuffix]
	if !exists {
		envCache = &environmentCache{}
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
		if suffixMatchesDomain(domain, suffix) && len(suffix) > len(best) {
			best = suffix
		}
	}
	return best
}

// suffixMatchesDomain reports whether suffix matches domain on a label boundary:
// the whole host equals the suffix, the suffix itself starts with '.', or the
// character just before the matched suffix is '.'. This stops a dotless suffix
// key (e.g. "pro") from over-matching mid-label (e.g. "mypro").
func suffixMatchesDomain(domain, suffix string) bool {
	if !strings.HasSuffix(domain, suffix) {
		return false
	}
	if domain == suffix || strings.HasPrefix(suffix, ".") {
		return true
	}
	return domain[len(domain)-len(suffix)-1] == '.'
}

// StatusForDomain returns the cached maintenance state for the environment that
// owns domain: whether maintenance is active and, when active, a copy of the
// whitelist. An uninitialized cache or an unknown environment reads as inactive.
func StatusForDomain(domain string) (bool, []string) {
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
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Using cached status for environment '%s' (domain: %s): active=%v, whitelist count=%d\n",
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

	return DefaultEndpoint
}

// Close releases background-refresh resources. It is safe to call more than
// once; only the first call performs the shutdown.
func Close() {
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
				fmt.Fprintf(logx.Out, "[MaintenanceCheck] Beginning shared cache cleanup\n")
			}

			close(stopCh)

			// Wait a bit for goroutines to terminate
			time.Sleep(200 * time.Millisecond)

			// Clear client/initialized under the cache lock — readers and the
			// refresher read these fields under the RWMutex, so writing them under
			// initLock alone is a data race.
			sharedCache.Lock()
			sharedCache.client = nil
			sharedCache.initialized = false
			sharedCache.Unlock()

			if debug {
				fmt.Fprintf(logx.Out, "[MaintenanceCheck] Shared cache resources cleaned up\n")
			}
		}
	})
}

// ResetForTesting resets all shared state for testing.
func ResetForTesting() {
	initLock.Lock()
	defer initLock.Unlock()

	// Signal the refresher to stop. Close may have already closed stopCh (and
	// cleared initialized); only close it here if it is still open, to avoid a
	// double close.
	if sharedCache.initialized && sharedCache.stopCh != nil {
		close(sharedCache.stopCh)
	}

	// Always wait (under the RWMutex) for the refresher goroutine to observe the
	// stop and clear refresherRunning before overwriting sharedCache. Reading the
	// flag under RLock establishes a happens-before edge with the goroutine's
	// final write, so the lock-free wholesale reset below cannot race it — this
	// holds even when Close already cleared initialized.
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

	// Clear per-environment locks only now that the refresher has stopped. A
	// still-running refresher calls envLock() and would otherwise re-populate the
	// map immediately after we cleared it.
	envLocksMu.Lock()
	envLocks = make(map[string]*sync.Mutex)
	envLocksMu.Unlock()

	sharedCache = sharedCacheState{}

	shutdownOnce = sync.Once{}
}
