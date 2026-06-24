package traefik_maintenance_plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"time"
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
		envCache = &EnvironmentCache{
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

	// client.Do guarantees resp != nil and resp.Body != nil when err == nil.
	// Drain before Close so the keep-alive connection is returned to the pool.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		if cerr := resp.Body.Close(); cerr != nil && debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Error closing response body: %v\n", cerr)
		}
	}()

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

	// A 200 whose body lacks system_config/maintenance (e.g. {}, an error
	// envelope, or an intermediary's status page) decodes to the zero value,
	// which is indistinguishable from "maintenance off" with value fields.
	// Treat an absent maintenance object as a failed fetch so prior state is
	// kept and the fetch is retried, rather than silently switching off.
	if result.SystemConfig == nil || result.SystemConfig.Maintenance == nil {
		if debug {
			fmt.Fprintf(os.Stdout, "[MaintenanceCheck] 200 response for environment '%s' carried no maintenance state; treating as a failed fetch\n", envSuffix)
		}

		backoffTime := calculateBackoff(currentFailedAttempts)
		updateEnvironmentCache(envSuffix, nil, backoffTime, currentFailedAttempts+1, false)
		return false
	}

	updateEnvironmentCache(envSuffix, &result, cacheDuration, 0, true)

	if debug {
		fmt.Fprintf(os.Stdout, "[MaintenanceCheck] Successfully updated maintenance status for environment '%s': active=%v, whitelist count=%d\n",
			envSuffix, result.SystemConfig.Maintenance.IsActive, len(result.SystemConfig.Maintenance.Whitelist))
	}

	return true
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
	jitterFactor := 0.8 + 0.4*rand.Float64()

	jitter := time.Duration(float64(backoff) * jitterFactor)

	// Cap maximum backoff at 1 hour
	maxBackoff := 1 * time.Hour
	if jitter > maxBackoff {
		return maxBackoff
	}

	return jitter
}
