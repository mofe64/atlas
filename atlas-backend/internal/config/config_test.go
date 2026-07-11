package config

import (
	"reflect"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ATLAS_HTTP_ADDR", "")
	t.Setenv("ATLAS_SHUTDOWN_TIMEOUT", "")
	t.Setenv("ATLAS_ALLOWED_ORIGINS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("ShutdownTimeout = %s", cfg.ShutdownTimeout)
	}
	if !reflect.DeepEqual(cfg.AllowedOrigins, defaultAllowedOrigins) {
		t.Fatalf("AllowedOrigins = %#v", cfg.AllowedOrigins)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("ATLAS_HTTP_ADDR", "127.0.0.1:9090")
	t.Setenv("ATLAS_SHUTDOWN_TIMEOUT", "2s")
	t.Setenv("ATLAS_ALLOWED_ORIGINS", "https://atlas.example, https://ops.example")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:9090" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.ShutdownTimeout != 2*time.Second {
		t.Fatalf("ShutdownTimeout = %s", cfg.ShutdownTimeout)
	}
	wantOrigins := []string{"https://atlas.example", "https://ops.example"}
	if !reflect.DeepEqual(cfg.AllowedOrigins, wantOrigins) {
		t.Fatalf("AllowedOrigins = %#v, want %#v", cfg.AllowedOrigins, wantOrigins)
	}
}

func TestLoadRejectsInvalidShutdownTimeout(t *testing.T) {
	t.Setenv("ATLAS_SHUTDOWN_TIMEOUT", "eventually")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want an invalid duration error")
	}
}
