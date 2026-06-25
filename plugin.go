package traefik_maintenance_plugin

import (
	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/maintenance"
)

// ResetSharedCacheForTesting resets shared maintenance state (test helper).
func ResetSharedCacheForTesting() { maintenance.ResetForTesting() }

// CloseSharedCache releases background-refresh resources.
func CloseSharedCache() { maintenance.Close() }
