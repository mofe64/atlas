package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultHTTPAddr              = "127.0.0.1:8080"
	defaultDatabaseURL           = "postgres://atlas:atlas@127.0.0.1:5432/atlas?sslmode=disable"
	defaultShutdownTimeout       = 5 * time.Second
	defaultSessionIdleTimeout    = 12 * time.Hour
	defaultSessionAbsoluteExpiry = 7 * 24 * time.Hour
	defaultSessionRetention      = 30 * 24 * time.Hour
)

var defaultAllowedOrigins = []string{
	"http://localhost:1420",
	"http://127.0.0.1:1420",
	"tauri://localhost",
	"http://tauri.localhost",
}

type Config struct {
	HTTPAddr              string
	DatabaseURL           string
	ShutdownTimeout       time.Duration
	SessionIdleTimeout    time.Duration
	SessionAbsoluteExpiry time.Duration
	SessionRetention      time.Duration
	AllowedOrigins        []string
	TrustedProxies        []string
}

func Load() (Config, error) {
	shutdownTimeout, err := durationFromEnv("ATLAS_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	idleTimeout, err := durationFromEnv("ATLAS_SESSION_IDLE_TIMEOUT", defaultSessionIdleTimeout)
	if err != nil {
		return Config{}, err
	}
	absoluteExpiry, err := durationFromEnv("ATLAS_SESSION_ABSOLUTE_TIMEOUT", defaultSessionAbsoluteExpiry)
	if err != nil {
		return Config{}, err
	}
	if idleTimeout >= absoluteExpiry {
		return Config{}, fmt.Errorf("ATLAS_SESSION_IDLE_TIMEOUT must be shorter than ATLAS_SESSION_ABSOLUTE_TIMEOUT")
	}
	retention, err := durationFromEnv("ATLAS_SESSION_RETENTION", defaultSessionRetention)
	if err != nil {
		return Config{}, err
	}
	return Config{
		HTTPAddr:              stringFromEnv("ATLAS_HTTP_ADDR", defaultHTTPAddr),
		DatabaseURL:           stringFromEnv("ATLAS_DATABASE_URL", defaultDatabaseURL),
		ShutdownTimeout:       shutdownTimeout,
		SessionIdleTimeout:    idleTimeout,
		SessionAbsoluteExpiry: absoluteExpiry,
		SessionRetention:      retention,
		AllowedOrigins:        listFromEnv("ATLAS_ALLOWED_ORIGINS", defaultAllowedOrigins),
		TrustedProxies:        listFromEnv("ATLAS_TRUSTED_PROXIES", nil),
	}, nil
}

func stringFromEnv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func durationFromEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration: %w", name, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return duration, nil
}

func listFromEnv(name string, fallback []string) []string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return append([]string(nil), fallback...)
	}

	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}
