package traefik_maintenance_plugin

import (
	"time"
)

const defaultMaintenanceEndpoint = "http://maintenance-service/v1/configurations/"

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
}

type EnvironmentSecret struct {
	Header string `json:"header"`
	Value  string `json:"value"`
}

func CreateConfig() *Config {
	return &Config{
		EnvironmentEndpoints: map[string]string{
			"": defaultMaintenanceEndpoint,
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
	isActive       bool
	whitelist      []string
	expiry         time.Time
	failedAttempts int
}
