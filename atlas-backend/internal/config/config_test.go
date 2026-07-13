package config

import (
	"reflect"
	"testing"
	"time"
)

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"ATLAS_HTTP_ADDR", "ATLAS_DATABASE_URL", "ATLAS_SHUTDOWN_TIMEOUT",
		"ATLAS_SESSION_IDLE_TIMEOUT", "ATLAS_SESSION_ABSOLUTE_TIMEOUT",
		"ATLAS_SESSION_RETENTION",
		"ATLAS_ALLOWED_ORIGINS", "ATLAS_TRUSTED_PROXIES",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearConfigEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:8080" || cfg.DatabaseURL != defaultDatabaseURL {
		t.Fatalf("unexpected addresses: HTTP=%q database=%q", cfg.HTTPAddr, cfg.DatabaseURL)
	}
	if cfg.ShutdownTimeout != 5*time.Second || cfg.SessionIdleTimeout != 12*time.Hour {
		t.Fatalf("unexpected timeouts: %#v", cfg)
	}
	if cfg.SessionAbsoluteExpiry != 7*24*time.Hour {
		t.Fatalf("unexpected session defaults: %#v", cfg)
	}
	if cfg.SessionRetention != 30*24*time.Hour {
		t.Fatalf("SessionRetention = %s", cfg.SessionRetention)
	}
	if !reflect.DeepEqual(cfg.AllowedOrigins, defaultAllowedOrigins) || cfg.TrustedProxies != nil {
		t.Fatalf("unexpected network defaults: %#v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("ATLAS_HTTP_ADDR", "127.0.0.1:9090")
	t.Setenv("ATLAS_DATABASE_URL", "postgres://example")
	t.Setenv("ATLAS_SHUTDOWN_TIMEOUT", "2s")
	t.Setenv("ATLAS_SESSION_IDLE_TIMEOUT", "1h")
	t.Setenv("ATLAS_SESSION_ABSOLUTE_TIMEOUT", "24h")
	t.Setenv("ATLAS_SESSION_RETENTION", "48h")
	t.Setenv("ATLAS_ALLOWED_ORIGINS", "https://atlas.example, https://ops.example")
	t.Setenv("ATLAS_TRUSTED_PROXIES", "10.0.0.0/8, 127.0.0.1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:9090" || cfg.DatabaseURL != "postgres://example" {
		t.Fatalf("unexpected addresses: %#v", cfg)
	}
	if cfg.ShutdownTimeout != 2*time.Second || cfg.SessionIdleTimeout != time.Hour || cfg.SessionAbsoluteExpiry != 24*time.Hour {
		t.Fatalf("unexpected timeouts: %#v", cfg)
	}
	if cfg.SessionRetention != 48*time.Hour {
		t.Fatalf("SessionRetention = %s", cfg.SessionRetention)
	}
	wantOrigins := []string{"https://atlas.example", "https://ops.example"}
	wantProxies := []string{"10.0.0.0/8", "127.0.0.1"}
	if !reflect.DeepEqual(cfg.AllowedOrigins, wantOrigins) || !reflect.DeepEqual(cfg.TrustedProxies, wantProxies) {
		t.Fatalf("unexpected lists: %#v", cfg)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "duration", key: "ATLAS_SHUTDOWN_TIMEOUT", value: "eventually"},
		{name: "idle longer than absolute", key: "ATLAS_SESSION_IDLE_TIMEOUT", value: "200h"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
		})
	}
}
