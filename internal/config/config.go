package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

var ErrNotIntGatewayPort = errors.New("GATEWAY_PORT is not integer value")
var ErrEmptyUpstreamAURL = errors.New("UPSTREAM_A_URL is empty")
var ErrEmptyUpstreamBURL = errors.New("UPSTREAM_B_URL is empty")

// Config holds all gateway configuration loaded from environment variables.
// Required fields must be set or Load returns an error.
// Fields with defaults are optional in the environment.
type Config struct {
	Port         int    // GATEWAY_PORT (default: 8080)
	UpstreamAURL string // UPSTREAM_A_URL (required)
	UpstreamBURL string // UPSTREAM_B_URL (required)
}

// Load reads configuration from environment variables.
// It returns an error if any required variable is missing or invalid.
func Load() (*Config, error) {
	var gatewayPort int
	var err error

	gatewayPortStr := os.Getenv("GATEWAY_PORT")
	if gatewayPortStr == "" {
		gatewayPort = 8080
	} else {
		gatewayPort, err = strconv.Atoi(gatewayPortStr)
		if err != nil {
			return nil, fmt.Errorf("config validation failed: %w", ErrNotIntGatewayPort)
		}
	}
	upstreamAURL := os.Getenv("UPSTREAM_A_URL")
	upstreamBURL := os.Getenv("UPSTREAM_B_URL")
	switch {
	case upstreamAURL == "":
		return nil, fmt.Errorf("config validation failed: %w", ErrEmptyUpstreamAURL)
	case upstreamBURL == "":
		return nil, fmt.Errorf("config validation failed: %w", ErrEmptyUpstreamBURL)
	}

	return &Config{gatewayPort, upstreamAURL, upstreamBURL}, nil
}
