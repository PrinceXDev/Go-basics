package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Tenant.Limit != 100 || cfg.APIKey.Limit != 40 || cfg.User.Limit != 20 {
		t.Errorf("unexpected default limits: %+v", cfg)
	}
	if cfg.Tenant.Window != time.Minute {
		t.Errorf("window = %s, want 1m", cfg.Tenant.Window)
	}
	if !cfg.FailOpen {
		t.Error("FailOpen should default to true")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("RATE_LIMIT_WINDOW", "30s")
	t.Setenv("RATE_LIMIT_USER", "5")
	t.Setenv("RATE_LIMIT_FAIL_OPEN", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.User.Limit != 5 || cfg.User.Window != 30*time.Second || cfg.FailOpen {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"missing database url", map[string]string{"DATABASE_URL": ""}},
		{"limit not a number", map[string]string{"RATE_LIMIT_USER": "lots"}},
		{"zero limit", map[string]string{"RATE_LIMIT_TENANT": "0"}},
		{"bad duration", map[string]string{"RATE_LIMIT_WINDOW": "soon"}},
		{"sub-second window", map[string]string{"RATE_LIMIT_WINDOW": "500ms"}},
		{"bad bool", map[string]string{"RATE_LIMIT_FAIL_OPEN": "maybe"}},
		{"zero db timeout", map[string]string{"DB_TIMEOUT": "0s"}},
		{"zero retry attempts", map[string]string{"DB_RETRY_ATTEMPTS": "0"}},
		{"zero breaker threshold", map[string]string{"BREAKER_THRESHOLD": "0"}},
		{"bad breaker cooldown", map[string]string{"BREAKER_COOLDOWN": "later"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://x")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}
