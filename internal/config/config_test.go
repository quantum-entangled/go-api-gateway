package config

import (
	"errors"
	"testing"
)

// This test demonstrates t.Setenv — it sets an env var for the duration
// of the test only, and automatically restores the original value when
// the test finishes. No cleanup needed.
func TestLoad_AllSet(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "9090")
	t.Setenv("UPSTREAM_A_URL", "http://localhost:8081")
	t.Setenv("UPSTREAM_B_URL", "http://localhost:8082")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d, want 9090", cfg.Port)
	}
	if cfg.UpstreamAURL != "http://localhost:8081" {
		t.Errorf("UpstreamAURL = %q, want %q", cfg.UpstreamAURL, "http://localhost:8081")
	}
	if cfg.UpstreamBURL != "http://localhost:8082" {
		t.Errorf("UpstreamBURL = %q, want %q", cfg.UpstreamBURL, "http://localhost:8082")
	}
}

func TestLoad_DefaultPort(t *testing.T) {
	t.Setenv("UPSTREAM_A_URL", "http://localhost:8081")
	t.Setenv("UPSTREAM_B_URL", "http://localhost:8082")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Fatalf("port = %d, want = 8080", cfg.Port)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	_, err := Load()
	if !errors.Is(err, ErrEmptyUpstreamAURL) {
		t.Errorf("expected ErrEmptyUpstreamAURL, got %v", err)
	}

	t.Setenv("UPSTREAM_A_URL", "https://localhost:8081")
	_, err = Load()
	if !errors.Is(err, ErrEmptyUpstreamBURL) {
		t.Errorf("expected ErrEmptyUpstreamBURL, got %v", err)
	}
}

func TestLoad_InvalidPort(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "abc")
	t.Setenv("UPSTREAM_A_URL", "http://localhost:8081")
	t.Setenv("UPSTREAM_B_URL", "http://localhost:8082")

	_, err := Load()
	if !errors.Is(err, ErrNotIntGatewayPort) {
		t.Errorf("expected ErrNotIntGatewayPort, but got %v", err)
	}
}
