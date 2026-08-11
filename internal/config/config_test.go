package config_test

import (
	"strings"
	"testing"

	"github.com/nehsa-net/webscraper-microservice-go-gin/internal/config"
)

func envMap(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(envMap(map[string]string{}))
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.Addr != ":8081" {
		t.Errorf("Addr = %q, want :8081 (the sibling weather service uses 8080)", cfg.Addr)
	}
	if cfg.RequestTimeout != 15 {
		t.Errorf("RequestTimeout = %d, want 15", cfg.RequestTimeout)
	}
	// The security-relevant default. If this ever flips, the service becomes an
	// SSRF proxy for whoever can reach it.
	if cfg.AllowPrivateHosts {
		t.Error("AllowPrivateHosts defaults to true; it must default to false")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(envMap(map[string]string{
		"ADDR":                    ":9999",
		"REQUEST_TIMEOUT_SECONDS": "3",
		"ALLOW_PRIVATE_HOSTS":     "true",
	}))
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.Addr != ":9999" {
		t.Errorf("Addr = %q, want :9999", cfg.Addr)
	}
	if cfg.RequestTimeout != 3 {
		t.Errorf("RequestTimeout = %d, want 3", cfg.RequestTimeout)
	}
	if !cfg.AllowPrivateHosts {
		t.Error("AllowPrivateHosts = false, want true when explicitly set")
	}
}

// A typo in a security switch must fail SAFE. Every value below leaves the
// guard on, because none of them is exactly "true".
func TestAllowPrivateHostsFailsSafe(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "yes", "1", "on", "tru", "false", "no", "0"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			cfg, err := config.Load(envMap(map[string]string{"ALLOW_PRIVATE_HOSTS": value}))
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if cfg.AllowPrivateHosts {
				t.Errorf("ALLOW_PRIVATE_HOSTS=%q enabled private hosts; only an exact \"true\" may", value)
			}
		})
	}
}

func TestAllowPrivateHostsAcceptsAnyCasingOfTrue(t *testing.T) {
	t.Parallel()

	// Surrounding whitespace is trimmed before the comparison: a trailing
	// space in a Kubernetes manifest is a typo, not an intent to disable, and
	// silently ignoring an explicit "true" would be the more surprising
	// behaviour of the two.
	for _, value := range []string{"true", "True", "TRUE", "  true  ", "TRUE "} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			cfg, err := config.Load(envMap(map[string]string{"ALLOW_PRIVATE_HOSTS": value}))
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if !cfg.AllowPrivateHosts {
				t.Errorf("ALLOW_PRIVATE_HOSTS=%q did not enable private hosts", value)
			}
		})
	}
}

func TestLoadValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{name: "non-numeric timeout", env: map[string]string{"REQUEST_TIMEOUT_SECONDS": "soon"}, wantErr: "must be an integer"},
		{name: "zero timeout", env: map[string]string{"REQUEST_TIMEOUT_SECONDS": "0"}, wantErr: "must be positive"},
		{name: "negative timeout", env: map[string]string{"REQUEST_TIMEOUT_SECONDS": "-5"}, wantErr: "must be positive"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(envMap(tc.env))

			if err == nil {
				t.Fatalf("Load() succeeded, want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
