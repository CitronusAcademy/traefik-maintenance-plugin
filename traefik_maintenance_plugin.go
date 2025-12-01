package traefik_maintenance_plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Initialize random source for jitter calculations
var (
	randSource = rand.New(rand.NewSource(time.Now().UnixNano()))
	randMutex  sync.Mutex // Mutex to protect randSource as it's not concurrent-safe
)

type Config struct {
	EnvironmentEndpoints    map[string]string            `json:"environmentEndpoints,omitempty"`
	EnvironmentSecrets      map[string]EnvironmentSecret `json:"environmentSecrets,omitempty"`
	CacheDurationInSeconds  int                          `json:"cacheDurationInSeconds,omitempty"`
	SkipPrefixes            []string                     `json:"skipPrefixes,omitempty"`
	SkipHosts               []string                     `json:"skipHosts,omitempty"`
	RequestTimeoutInSeconds int                          `json:"requestTimeoutInSeconds,omitempty"`
	MaintenanceStatusCode   int                          `json:"maintenanceStatusCode,omitempty"`
	Debug                   bool                         `json:"debug,omitempty"`
	SecretHeader            string                       `json:"secretHeader,omitempty"`
	SecretHeaderValue       string                       `json:"secretHeaderValue,omitempty"`
}

type EnvironmentSecret struct {
	Header string `json:"header"`
	Value  string `json:"value"`
}

func CreateConfig() *Config {
	return &Config{
		EnvironmentEndpoints: map[string]string{
			".com":   "http://maintenance-service.admin/v1/configurations/",
			".world": "http://maintenance-service.stage-admin/v1/configurations/",
			".pro":   "http://maintenance-service.develop-admin/v1/configurations/",
			"":       "http://maintenance-service.admin/v1/configurations/",
		},
		EnvironmentSecrets: map[string]EnvironmentSecret{
			".com":   {Header: "X-Plugin-Secret", Value: ""},
			".world": {Header: "X-Plugin-Secret", Value: ""},
			".pro":   {Header: "X-Plugin-Secret", Value: ""},
			"":       {Header: "X-Plugin-Secret", Value: ""},
		},
		CacheDurationInSeconds:  10,
		SkipPrefixes:            []string{},
		SkipHosts:               []string{},
		RequestTimeoutInSeconds: 5,
		MaintenanceStatusCode:   512,
		Debug:                   false,
	}
}

type MaintenanceResponse struct {
	SystemConfig struct {
		Maintenance struct {
			IsActive  bool     `json:"is_active"`
			Whitelist []string `json:"whitelist"`
		} `json:"maintenance"`
	} `json:"system_config"`
}

type EnvironmentCache struct {
	isActive            bool
	whitelist           []string
	expiry              time.Time
	failedAttempts      int
	lastSuccessfulFetch time.Time
}

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
	refreshLock  sync.Mutex
	shutdownOnce sync.Once // Ensure clean shutdown happens only once
)

type MaintenanceCheck struct {
	next                  http.Handler
	skipPrefixes          []string
	skipHosts             []string
	maintenanceStatusCode int
	debug                 bool
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

func startBackgroundRefresher() {
	if sharedCache.refresherRunning {
		return
	}

	sharedCache.Lock()
	if sharedCache.refresherRunning {
		sharedCache.Unlock()
		return
	}

	sharedCache.refresherRunning = true
	stopCh := sharedCache.stopCh
	debug := sharedCache.debug
	cacheDuration := sharedCache.cacheDuration
	sharedCache.Unlock()

	if debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Started shared background refresher with interval of %v\n", cacheDuration)
	}

	go func() {
		ticker := time.NewTicker(cacheDuration)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				refreshAllEnvironments()
			case <-stopCh:
				if debug {
					fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Shared background refresher stopped\n")
				}

				sharedCache.Lock()
				sharedCache.refresherRunning = false
				sharedCache.Unlock()
				return
			}
		}
	}()
}

func refreshAllEnvironments() {
	sharedCache.RLock()
	environmentEndpoints := sharedCache.environmentEndpoints
	sharedCache.RUnlock()

	for envSuffix := range environmentEndpoints {
		refreshMaintenanceStatusForEnvironment(envSuffix)
	}
}

func refreshMaintenanceStatusForEnvironment(envSuffix string) bool {
	if !refreshLock.TryLock() {
		return true
	}

	defer func() {
		refreshLock.Unlock()
	}()

	sharedCache.RLock()
	client := sharedCache.client
	requestTimeout := sharedCache.requestTimeout
	userAgent := sharedCache.userAgent
	cacheDuration := sharedCache.cacheDuration
	debug := sharedCache.debug

	envCache, exists := sharedCache.environments[envSuffix]
	if !exists {
		envCache = &EnvironmentCache{
			expiry: time.Now().Add(-1 * time.Minute),
		}
	}
	needsRefresh := time.Now().After(envCache.expiry)
	currentFailedAttempts := envCache.failedAttempts

	var secretHeader, secretHeaderValue string
	if envSecret, exists := sharedCache.environmentSecrets[envSuffix]; exists {
		secretHeader = envSecret.Header
		secretHeaderValue = envSecret.Value
	} else if sharedCache.secretHeader != "" && sharedCache.secretHeaderValue != "" {
		secretHeader = sharedCache.secretHeader
		secretHeaderValue = sharedCache.secretHeaderValue
	}

	sharedCache.RUnlock()

	if !needsRefresh {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Environment '%s' cache is still valid, skipping refresh\n", envSuffix)
		}
		return true
	}

	endpoint := getEndpointForDomain(envSuffix)

	if debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Fetching maintenance status from '%s' for environment '%s'\n", endpoint, envSuffix)
	}

	if client == nil {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] HTTP client is nil, skipping refresh\n")
		}
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Error creating request: %v\n", err)
		}

		backoffTime := calculateBackoff(currentFailedAttempts)
		updateEnvironmentCache(envSuffix, nil, backoffTime, currentFailedAttempts+1, false)
		return false
	}

	req.Header.Set("User-Agent", userAgent)

	if secretHeader != "" && secretHeaderValue != "" {
		req.Header.Set(secretHeader, secretHeaderValue)
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Added secret header '%s' for environment '%s'\n", secretHeader, envSuffix)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Error making request: %v\n", err)
		}

		backoffTime := calculateBackoff(currentFailedAttempts)
		updateEnvironmentCache(envSuffix, nil, backoffTime, currentFailedAttempts+1, false)
		return false
	}

	if resp != nil && resp.Body != nil {
		defer func() {
			err := resp.Body.Close()
			if err != nil && debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Error closing response body: %v\n", err)
			}
		}()
	}

	if resp == nil {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Nil response received\n")
		}

		backoffTime := calculateBackoff(currentFailedAttempts)
		updateEnvironmentCache(envSuffix, nil, backoffTime, currentFailedAttempts+1, false)
		return false
	}

	if resp.StatusCode != http.StatusOK {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] API returned status code: %d\n", resp.StatusCode)
		}

		backoffTime := calculateBackoff(currentFailedAttempts)
		updateEnvironmentCache(envSuffix, nil, backoffTime, currentFailedAttempts+1, false)
		return false
	}

	const maxResponseSize = 10 * 1024 * 1024
	limitedReader := http.MaxBytesReader(nil, resp.Body, maxResponseSize)

	var result MaintenanceResponse
	decoder := json.NewDecoder(limitedReader)
	if err := decoder.Decode(&result); err != nil {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Error parsing JSON: %v\n", err)
		}

		backoffTime := calculateBackoff(currentFailedAttempts)
		updateEnvironmentCache(envSuffix, nil, backoffTime, currentFailedAttempts+1, false)
		return false
	}

	isActive := result.SystemConfig.Maintenance.IsActive
	whitelist := result.SystemConfig.Maintenance.Whitelist

	whitelistCopy := make([]string, len(whitelist))
	copy(whitelistCopy, whitelist)

	updateEnvironmentCache(envSuffix, &MaintenanceResponse{result.SystemConfig}, cacheDuration, 0, true)

	if debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Successfully updated maintenance status for environment '%s': active=%v, whitelist count=%d\n",
			envSuffix, isActive, len(whitelist))
	}

	return true
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

func getMaintenanceStatus() (bool, []string) {
	return false, []string{}
}

// calculateBackoff returns an exponential backoff duration with jitter
func calculateBackoff(attempts int) time.Duration {
	if attempts <= 0 {
		return 5 * time.Second // Minimum backoff
	}

	// Cap maximum number of attempts for backoff calculation to avoid excessive delays
	if attempts > 10 {
		attempts = 10
	}

	// Base exponential backoff: 5s, 10s, 20s, 40s, etc. up to ~1h
	backoff := 5 * time.Second * time.Duration(1<<uint(attempts))

	// Add jitter of +/- 20% to avoid thundering herd problem
	randMutex.Lock()
	jitterFactor := 0.8 + 0.4*randSource.Float64()
	randMutex.Unlock()

	jitter := time.Duration(float64(backoff) * jitterFactor)

	// Cap maximum backoff at 1 hour
	maxBackoff := 1 * time.Hour
	if jitter > maxBackoff {
		return maxBackoff
	}

	return jitter
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

func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if config.MaintenanceStatusCode < 100 || config.MaintenanceStatusCode > 599 {
		return nil, fmt.Errorf("invalid maintenance status code: %d (must be between 100-599)",
			config.MaintenanceStatusCode)
	}

	cacheDuration := time.Duration(config.CacheDurationInSeconds) * time.Second
	requestTimeout := time.Duration(config.RequestTimeoutInSeconds) * time.Second
	userAgent := fmt.Sprintf("TraefikMaintenancePlugin/%s", name)

	if cacheDuration <= 0 {
		cacheDuration = 10 * time.Second
	}

	if requestTimeout <= 0 {
		requestTimeout = 5 * time.Second
	}

	environmentEndpoints := config.EnvironmentEndpoints
	if len(environmentEndpoints) == 0 {
		environmentEndpoints = map[string]string{
			".com":   "http://maintenance-service.admin/v1/configurations/",
			".world": "http://maintenance-service.stage-admin/v1/configurations/",
			".pro":   "http://maintenance-service.develop-admin/v1/configurations/",
			"":       "http://maintenance-service.admin/v1/configurations/",
		}
	}

	ensureSharedCacheInitialized(environmentEndpoints, config.EnvironmentSecrets, cacheDuration, requestTimeout, config.Debug, userAgent, config.SecretHeader, config.SecretHeaderValue)

	skipPrefixesCopy := make([]string, len(config.SkipPrefixes))
	copy(skipPrefixesCopy, config.SkipPrefixes)

	skipHostsCopy := make([]string, len(config.SkipHosts))
	copy(skipHostsCopy, config.SkipHosts)

	m := &MaintenanceCheck{
		next:                  next,
		skipPrefixes:          skipPrefixesCopy,
		skipHosts:             skipHostsCopy,
		maintenanceStatusCode: config.MaintenanceStatusCode,
		debug:                 config.Debug,
	}

	go func() {
		<-ctx.Done()
		if config.Debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Context cancelled for middleware instance\n")
		}
	}()

	return m, nil
}

func (m *MaintenanceCheck) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req == nil {
		http.Error(rw, "Bad Request: nil request received", http.StatusBadRequest)
		return
	}

	if m.handleCORSPreflightRequest(rw, req) {
		return
	}

	m.logRequestHeadersForDebugging(req)

	normalizedHost := m.extractHostWithoutPort(req.Host)

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Evaluating request: host=%s, path=%s\n", normalizedHost, req.URL.Path)
	}

	if m.isHostSkipped(normalizedHost) {
		if m.debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Host '%s' is in skip list, bypassing maintenance check\n", normalizedHost)
		}
		m.next.ServeHTTP(rw, req)
		return
	}

	if m.isPrefixSkipped(req.URL.Path) {
		if m.debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Path '%s' matches skip prefix, bypassing maintenance check\n", req.URL.Path)
		}
		m.next.ServeHTTP(rw, req)
		return
	}

	isActive, whitelist := getMaintenanceStatusForDomain(normalizedHost)
	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Maintenance status: active=%v, whitelist=%v\n", isActive, whitelist)
	}

	if isActive {
		if m.isClientAllowed(req, whitelist) {
			m.next.ServeHTTP(rw, req)
			return
		}

		m.sendMaintenanceResponseWithCORS(rw, req)
		return
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Maintenance mode is inactive, allowing request\n")
	}
	m.next.ServeHTTP(rw, req)
}

func (m *MaintenanceCheck) handleCORSPreflightRequest(rw http.ResponseWriter, req *http.Request) bool {
	if req.Method != http.MethodOptions {
		return false
	}

	isActive, whitelist := getMaintenanceStatusForDomain(req.Host)
	if !isActive {
		return false
	}

	clientOrigin := req.Header.Get("Origin")
	m.setCORSPreflightHeaders(rw, clientOrigin)

	if !m.isClientAllowed(req, whitelist) {
		m.sendBlockedPreflightResponse(rw)
		return true
	}

	m.sendSuccessfulPreflightResponse(rw)
	return true
}

func (m *MaintenanceCheck) setCORSPreflightHeaders(rw http.ResponseWriter, origin string) {
	if origin == "" {
		return
	}

	corsHeaders := map[string]string{
		"Access-Control-Allow-Origin":      origin,
		"Access-Control-Allow-Methods":     "GET, POST, PUT, DELETE, OPTIONS",
		"Access-Control-Allow-Headers":     "Accept, Authorization, Content-Type, X-CSRF-Token",
		"Access-Control-Allow-Credentials": "true",
		"Access-Control-Max-Age":           "86400",
	}

	for headerName, headerValue := range corsHeaders {
		rw.Header().Set(headerName, headerValue)
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] CORS preflight handled for origin: %s\n", origin)
	}
}

func (m *MaintenanceCheck) isMaintenanceActiveForClient(req *http.Request) bool {
	isActive, whitelist := getMaintenanceStatusForDomain(req.Host)
	return isActive && !m.isClientAllowed(req, whitelist)
}

func (m *MaintenanceCheck) sendBlockedPreflightResponse(rw http.ResponseWriter) {
	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] CORS preflight completed, but actual request will be blocked due to maintenance mode\n")
	}

	// Preflight must always return 2xx status according to CORS spec
	rw.WriteHeader(http.StatusOK)
}

func (m *MaintenanceCheck) sendSuccessfulPreflightResponse(rw http.ResponseWriter) {
	rw.WriteHeader(http.StatusNoContent)
}

func (m *MaintenanceCheck) logRequestHeadersForDebugging(req *http.Request) {
	if !m.debug {
		return
	}

	fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Request headers for diagnostics:\n")
	for headerName, headerValues := range req.Header {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck]   %s: %s\n", headerName, strings.Join(headerValues, ", "))
	}

	fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Using only Cf-Connecting-Ip header for IP detection (supports single value or CSV)\n")
}

func (m *MaintenanceCheck) extractHostWithoutPort(originalHost string) string {
	host := originalHost
	if colonIndex := strings.IndexByte(host, ':'); colonIndex > 0 {
		host = host[:colonIndex]
		if m.debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Normalized host from '%s' to '%s'\n", originalHost, host)
		}
	}
	return host
}

func (m *MaintenanceCheck) sendMaintenanceResponseWithCORS(rw http.ResponseWriter, req *http.Request) {
	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Access denied, returning status code %d\n", m.maintenanceStatusCode)
	}

	m.addCORSHeadersToMaintenanceResponse(rw, req)

	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.WriteHeader(m.maintenanceStatusCode)
	_, _ = rw.Write([]byte("Service is in maintenance mode"))
}

func (m *MaintenanceCheck) addCORSHeadersToMaintenanceResponse(rw http.ResponseWriter, req *http.Request) {
	clientOrigin := req.Header.Get("Origin")
	if clientOrigin == "" {
		return
	}

	rw.Header().Set("Access-Control-Allow-Origin", clientOrigin)
	rw.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	rw.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-CSRF-Token")
	rw.Header().Set("Access-Control-Allow-Credentials", "true")
	rw.Header().Set("Access-Control-Max-Age", "86400")

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Added CORS headers to maintenance response for origin: %s\n", clientOrigin)
	}
}

// getAllClientIPs extracts all IP addresses from Cf-Connecting-Ip header only
// Returns a slice of unique IPs found in the header (supports single value or CSV)
func getAllClientIPs(req *http.Request, debug bool) []string {
	// Guard against nil request
	if req == nil {
		return []string{}
	}

	// Use only Cf-Connecting-Ip header (CloudFlare)
	const cfHeader = "Cf-Connecting-Ip"

	// Use a map to track unique IPs
	uniqueIPs := make(map[string]struct{})
	var allIPs []string

	if debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Extracting client IPs from %s header only\n", cfHeader)
	}

	// Get Cf-Connecting-Ip header value
	addresses := req.Header.Get(cfHeader)
	if addresses != "" {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Processing header %s with value: %s\n", cfHeader, addresses)
		}

		// Handle comma-separated values (CSV) or single value
		parts := strings.Split(addresses, ",")
		for _, part := range parts {
			ip := strings.TrimSpace(part)
			// Handle IPv6 bracket notation [IPv6]:port
			ip = cleanIPAddress(ip)
			if ip != "" {
				if _, exists := uniqueIPs[ip]; !exists {
					uniqueIPs[ip] = struct{}{}
					allIPs = append(allIPs, ip)
					if debug {
						fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Extracted IP from %s header: %s\n", cfHeader, ip)
					}
				}
			}
		}
	} else if debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] %s header not found or empty\n", cfHeader)
	}

	if debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Total unique IPs extracted: %d - %v\n", len(allIPs), allIPs)
	}

	return allIPs
}

// cleanIPAddress removes brackets, ports, and whitespace from an IP address
func cleanIPAddress(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}

	// Handle IPv6 bracket notation [IPv6]:port or [IPv6]
	if strings.HasPrefix(ip, "[") {
		if bracketEnd := strings.Index(ip, "]"); bracketEnd != -1 {
			ip = ip[1:bracketEnd]
		}
	} else if strings.Contains(ip, ".") {
		// IPv4 with possible port
		if colonIdx := strings.LastIndex(ip, ":"); colonIdx != -1 {
			// Make sure this is not IPv6 (IPv6 has multiple colons)
			if strings.Count(ip, ":") == 1 {
				ip = ip[:colonIdx]
			}
		}
	}

	return ip
}

// getClientIP returns the first client IP from Cf-Connecting-Ip header
// This is kept for backward compatibility
func getClientIP(req *http.Request, debug bool) string {
	allIPs := getAllClientIPs(req, debug)
	if len(allIPs) > 0 {
		return allIPs[0]
	}
	return ""
}

func (m *MaintenanceCheck) isClientAllowed(req *http.Request, whitelist []string) bool {
	// Guard against nil request or whitelist
	if req == nil {
		return false
	}

	if len(whitelist) == 0 {
		if m.debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Whitelist is empty, blocking request\n")
		}
		return false
	}

	// Extended debug info for whitelist
	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Maintenance whitelist entries: %d items\n", len(whitelist))
		for i, entry := range whitelist {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck]   Whitelist[%d]: %s\n", i, entry)
		}
	}

	// Check for wildcard first
	for _, entry := range whitelist {
		if entry == "*" {
			if m.debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Wildcard (*) found in whitelist, allowing request\n")
			}
			return true
		}
	}

	// Get ALL client IPs from all headers
	clientIPs := getAllClientIPs(req, m.debug)

	// No IPs found at all
	if len(clientIPs) == 0 {
		if m.debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Could not determine any client IPs, blocking request\n")
		}
		return false
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Client IPs for whitelist check: %v\n", clientIPs)
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Beginning whitelist evaluation for %d IPs\n", len(clientIPs))
	}

	// Check each client IP against the whitelist
	// If ANY IP matches, allow the request
	for _, clientIP := range clientIPs {
		if clientIP == "" {
			continue
		}

		for _, whitelistEntry := range whitelist {
			// Exact match
			if whitelistEntry == clientIP {
				if m.debug {
					fmt.Fprintf(os.Stdout, "[MaintenanceCheck] IP '%s' matches whitelist entry '%s', allowing request\n", clientIP, whitelistEntry)
				}
				return true
			}

			// CIDR notation match
			if strings.Contains(whitelistEntry, "/") {
				if m.debug {
					fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Checking if IP '%s' is in CIDR range '%s'\n", clientIP, whitelistEntry)
				}

				match, err := isCIDRMatch(clientIP, whitelistEntry)
				if err != nil {
					if m.debug {
						fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Error checking CIDR match: %v\n", err)
					}
				} else if match {
					if m.debug {
						fmt.Fprintf(os.Stdout, "[MaintenanceCheck] IP '%s' is in CIDR range '%s', allowing request\n", clientIP, whitelistEntry)
					}
					return true
				} else if m.debug {
					fmt.Fprintf(os.Stdout, "[MaintenanceCheck] IP '%s' is NOT in CIDR range '%s'\n", clientIP, whitelistEntry)
				}
			}
		}
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] None of the client IPs %v matched whitelist, blocking request\n", clientIPs)
	}
	return false
}

func (m *MaintenanceCheck) isHostSkipped(host string) bool {
	// Guard against empty hosts
	if host == "" {
		return false
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Checking host '%s' against skipHosts: %v\n", host, m.skipHosts)
	}

	for _, skipHost := range m.skipHosts {
		// Skip empty entries
		if skipHost == "" {
			continue
		}

		// Check for wildcard domain pattern (*.example.com)
		if strings.HasPrefix(skipHost, "*.") {
			suffix := skipHost[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) {
				if m.debug {
					fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Host '%s' matches wildcard pattern '%s'\n", host, skipHost)
				}
				return true
			}
		} else if skipHost == host {
			if m.debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Host '%s' matches exact host '%s'\n", host, skipHost)
			}
			return true
		}
	}

	return false
}

func (m *MaintenanceCheck) isPrefixSkipped(path string) bool {
	// Guard against nil path
	if path == "" {
		return false
	}

	if m.debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Checking path '%s' against skipPrefixes: %v\n", path, m.skipPrefixes)
	}

	for _, prefix := range m.skipPrefixes {
		// Skip empty prefixes
		if prefix == "" {
			continue
		}

		if strings.HasPrefix(path, prefix) {
			if m.debug {
				fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Path '%s' matches prefix '%s'\n", path, prefix)
			}
			return true
		}
	}

	return false
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

// isCIDRMatch checks if an IP is contained within a CIDR range
func isCIDRMatch(ip, cidr string) (bool, error) {
	// Parse the CIDR notation
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false, fmt.Errorf("invalid CIDR notation %s: %v", cidr, err)
	}

	// Parse the IP
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false, fmt.Errorf("invalid IP address %s (cannot be parsed as IPv4 or IPv6)", ip)
	}

	// Check if the IP is the correct version (IPv4/IPv6) for the CIDR
	if (ipNet.IP.To4() == nil) != (parsedIP.To4() == nil) {
		return false, fmt.Errorf("IP version mismatch: CIDR %s is %s but IP %s is %s",
			cidr,
			ipVersionName(ipNet.IP),
			ip,
			ipVersionName(parsedIP))
	}

	// Check if the IP is contained in the CIDR range
	return ipNet.Contains(parsedIP), nil
}

// ipVersionName returns a string indicating whether an IP is IPv4 or IPv6
func ipVersionName(ip net.IP) string {
	if ip.To4() != nil {
		return "IPv4"
	}
	return "IPv6"
}
