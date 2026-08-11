// Package config resolves runtime settings from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	Addr string
	// AllowPrivateHosts disables the SSRF guard's address checks. It exists
	// for local development against localhost, and must stay false anywhere
	// the endpoint is reachable by anyone else.
	AllowPrivateHosts bool
	RequestTimeout    int // seconds
}

// Load resolves configuration, applying defaults.
//
// It takes a lookup function rather than calling os.Getenv, so tests can pass
// a map and run in parallel — t.Setenv panics in a parallel test, because the
// process environment is shared mutable state.
func Load(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}

	get := func(key, fallback string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return fallback
	}

	cfg := Config{Addr: get("ADDR", ":8081")}

	timeout := get("REQUEST_TIMEOUT_SECONDS", "15")
	n, err := strconv.Atoi(timeout)
	if err != nil {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT_SECONDS must be an integer, got %q", timeout)
	}
	if n <= 0 {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT_SECONDS must be positive, got %d", n)
	}
	cfg.RequestTimeout = n

	// Anything other than an explicit "true" leaves the guard on. A typo in
	// this variable must fail SAFE.
	cfg.AllowPrivateHosts = strings.EqualFold(get("ALLOW_PRIVATE_HOSTS", "false"), "true")

	return cfg, nil
}
