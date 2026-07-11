package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultHTTPAddr        = "127.0.0.1:8080"
	defaultShutdownTimeout = 5 * time.Second
)

var defaultAllowedOrigins = []string{
	"http://localhost:1420",
	"http://127.0.0.1:1420",
	"tauri://localhost",
	"http://tauri.localhost",
}

type Config struct {
	HTTPAddr        string
	ShutdownTimeout time.Duration
	AllowedOrigins  []string
}

func Load() (Config, error) {
	shutdownTimeout, err := durationFromEnv("ATLAS_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}

	return Config{
		HTTPAddr:        stringFromEnv("ATLAS_HTTP_ADDR", defaultHTTPAddr),
		ShutdownTimeout: shutdownTimeout,
		AllowedOrigins:  originsFromEnv("ATLAS_ALLOWED_ORIGINS", defaultAllowedOrigins),
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

func originsFromEnv(name string, fallback []string) []string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return append([]string(nil), fallback...)
	}

	var origins []string
	for _, origin := range strings.Split(value, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			origins = append(origins, origin)
		}
	}
	return origins
}
