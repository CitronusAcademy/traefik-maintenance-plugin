package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"

	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/logx"
)

func startBackgroundRefresher() {
	sharedCache.RLock()
	running := sharedCache.refresherRunning
	sharedCache.RUnlock()
	if running {
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
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Started shared background refresher with interval of %v\n", cacheDuration)
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
					fmt.Fprintf(logx.Out, "[MaintenanceCheck] Shared background refresher stopped\n")
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

	// Safe to range after RUnlock today: environmentEndpoints is written once at
	// init and never mutated. If a second writer is ever added, hold the lock
	// across this range (or copy the keys under it) to avoid a concurrent-map read.
	for envSuffix := range environmentEndpoints {
		refreshMaintenanceStatusForEnvironment(envSuffix)
	}
}

func refreshMaintenanceStatusForEnvironment(envSuffix string) bool {
	lock := envLock(envSuffix)
	lock.Lock()
	defer lock.Unlock()

	sharedCache.RLock()
	client := sharedCache.client
	requestTimeout := sharedCache.requestTimeout
	userAgent := sharedCache.userAgent
	cacheDuration := sharedCache.cacheDuration
	debug := sharedCache.debug

	envCache, exists := sharedCache.environments[envSuffix]
	if !exists {
		envCache = &environmentCache{
			expiry: time.Now().Add(-1 * time.Minute),
		}
	}
	needsRefresh := time.Now().After(envCache.expiry)
	currentFailedAttempts := envCache.failedAttempts

	var secretHeader, secretHeaderValue string
	if envSecret, exists := sharedCache.environmentSecrets[envSuffix]; exists && envSecret.Value != "" {
		secretHeader = envSecret.Header
		secretHeaderValue = envSecret.Value
	} else if sharedCache.secretHeader != "" && sharedCache.secretHeaderValue != "" {
		secretHeader = sharedCache.secretHeader
		secretHeaderValue = sharedCache.secretHeaderValue
	}

	sharedCache.RUnlock()

	if !needsRefresh {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Environment '%s' cache is still valid, skipping refresh\n", envSuffix)
		}
		return true
	}

	endpoint := getEndpointForDomain(envSuffix)

	if debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Fetching maintenance status from '%s' for environment '%s'\n", endpoint, envSuffix)
	}

	// A nil client (cache torn down by Close) is not a fetch failure: skip
	// without applying backoff or touching the cached state.
	if client == nil {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] HTTP client is nil, skipping refresh\n")
		}
		return false
	}

	result, ok := fetchMaintenanceState(client, endpoint, userAgent, secretHeader, secretHeaderValue, requestTimeout, envSuffix, debug)
	if !ok {
		backoffTime := calculateBackoff(currentFailedAttempts)
		updateEnvironmentCache(envSuffix, nil, backoffTime, currentFailedAttempts+1, false)
		return false
	}

	updateEnvironmentCache(envSuffix, result, cacheDuration, 0, true)

	if debug {
		fmt.Fprintf(logx.Out, "[MaintenanceCheck] Successfully updated maintenance status for environment '%s': active=%v, whitelist count=%d\n",
			envSuffix, result.SystemConfig.Maintenance.IsActive, len(result.SystemConfig.Maintenance.Whitelist))
	}

	return true
}

// fetchMaintenanceState performs a single maintenance-status request against
// endpoint and returns the decoded response. The bool is false for any
// failure the caller should treat as a failed fetch — request build error,
// transport error, non-200 status, decode error, or a 200 whose body carries
// no maintenance object — at which point the caller applies backoff and keeps
// the prior cached state. The caller is responsible for the nil-client check.
// buildStatusRequest constructs the GET request for a maintenance-status fetch,
// setting the User-Agent and, when both are configured, the secret header.
func buildStatusRequest(ctx context.Context, endpoint, userAgent, secretHeader, secretHeaderValue, envSuffix string, debug bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", userAgent)

	if secretHeader != "" && secretHeaderValue != "" {
		req.Header.Set(secretHeader, secretHeaderValue)
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Added secret header '%s' for environment '%s'\n", secretHeader, envSuffix)
		}
	}

	return req, nil
}

func fetchMaintenanceState(client *http.Client, endpoint, userAgent, secretHeader, secretHeaderValue string, requestTimeout time.Duration, envSuffix string, debug bool) (*maintenanceResponse, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	req, err := buildStatusRequest(ctx, endpoint, userAgent, secretHeader, secretHeaderValue, envSuffix, debug)
	if err != nil {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Error creating request: %v\n", err)
		}
		return nil, false
	}

	resp, err := client.Do(req)
	if err != nil {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Error making request: %v\n", err)
		}
		return nil, false
	}

	// client.Do guarantees resp != nil and resp.Body != nil when err == nil.
	// Drain before Close so the keep-alive connection is returned to the pool.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		if cerr := resp.Body.Close(); cerr != nil && debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Error closing response body: %v\n", cerr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] API returned status code: %d\n", resp.StatusCode)
		}
		return nil, false
	}

	const maxResponseSize = 10 * 1024 * 1024
	limitedReader := http.MaxBytesReader(nil, resp.Body, maxResponseSize)

	var result maintenanceResponse
	decoder := json.NewDecoder(limitedReader)
	if err := decoder.Decode(&result); err != nil {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] Error parsing JSON: %v\n", err)
		}
		return nil, false
	}

	// A 200 whose body lacks system_config/maintenance (e.g. {}, an error
	// envelope, or an intermediary's status page) decodes to the zero value,
	// which is indistinguishable from "maintenance off" with value fields.
	// Treat an absent maintenance object as a failed fetch so prior state is
	// kept and the fetch is retried, rather than silently switching off.
	if result.SystemConfig == nil || result.SystemConfig.Maintenance == nil {
		if debug {
			fmt.Fprintf(logx.Out, "[MaintenanceCheck] 200 response for environment '%s' carried no maintenance state; treating as a failed fetch\n", envSuffix)
		}
		return nil, false
	}

	return &result, true
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

	// Add jitter of +/- 20% to avoid thundering herd problem.
	// math/rand's top-level funcs are auto-seeded and goroutine-safe (Go 1.20+).
	jitterFactor := 0.8 + 0.4*rand.Float64() //nolint:gosec // backoff jitter is not security-sensitive; no crypto entropy needed

	jitter := time.Duration(float64(backoff) * jitterFactor)

	// Cap maximum backoff at 1 hour
	maxBackoff := 1 * time.Hour
	if jitter > maxBackoff {
		return maxBackoff
	}

	return jitter
}
