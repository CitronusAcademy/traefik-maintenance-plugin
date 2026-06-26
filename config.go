package traefik_maintenance_plugin

import (
	"github.com/CitronusAcademy/traefik-maintenance-plugin/internal/maintenance"
)

type Config struct {
	EnvironmentEndpoints     map[string]string            `json:"environmentEndpoints,omitempty"`
	EnvironmentSecrets       map[string]EnvironmentSecret `json:"environmentSecrets,omitempty"`
	CacheDurationInSeconds   int                          `json:"cacheDurationInSeconds,omitempty"`
	SkipPrefixes             []string                     `json:"skipPrefixes,omitempty"`
	SkipHosts                []string                     `json:"skipHosts,omitempty"`
	AllowHTMLWhenMaintenance bool                         `json:"allowHTMLWhenMaintenance,omitempty"`
	AllowStaticExtensions    []string                     `json:"allowStaticExtensions,omitempty"`
	RequestTimeoutInSeconds  int                          `json:"requestTimeoutInSeconds,omitempty"`
	MaintenanceStatusCode    int                          `json:"maintenanceStatusCode,omitempty"`
	Debug                    bool                         `json:"debug,omitempty"`
	SecretHeader             string                       `json:"secretHeader,omitempty"`
	SecretHeaderValue        string                       `json:"secretHeaderValue,omitempty"`
	AllowedOrigins           []string                     `json:"allowedOrigins,omitempty"`
	CorsAllowAnyOrigin       bool                         `json:"corsAllowAnyOrigin,omitempty"`
	TrustedProxies           []string                     `json:"trustedProxies,omitempty"`
	StrictAssetMatching      bool                         `json:"strictAssetMatching,omitempty"`

	MaintenanceResponseBody     string `json:"maintenanceResponseBody,omitempty"`
	MaintenanceResponseFilePath string `json:"maintenanceResponseFilePath,omitempty"`
	MaintenanceContentType      string `json:"maintenanceContentType,omitempty"`
	RetryAfterSeconds           int    `json:"retryAfterSeconds,omitempty"`
	MaintenanceCacheControl     string `json:"maintenanceCacheControl,omitempty"`
}

type EnvironmentSecret struct {
	Header string `json:"header"`
	Value  string `json:"value"`
}

func CreateConfig() *Config {
	return &Config{
		EnvironmentEndpoints: map[string]string{
			"": maintenance.DefaultEndpoint,
		},
		EnvironmentSecrets: map[string]EnvironmentSecret{
			"": {Header: "X-Plugin-Secret", Value: ""},
		},
		CacheDurationInSeconds:   10,
		SkipPrefixes:             []string{},
		SkipHosts:                []string{},
		AllowHTMLWhenMaintenance: true,
		AllowStaticExtensions: []string{
			".js",
			".css",
			".svg",
			".ico",
			".png",
			".jpg",
			".jpeg",
			".gif",
			".webp",
			".woff",
			".woff2",
			".ttf",
			".map",
		},
		RequestTimeoutInSeconds: 5,
		MaintenanceStatusCode:   512,
		Debug:                   false,
		CorsAllowAnyOrigin:      true,
	}
}
