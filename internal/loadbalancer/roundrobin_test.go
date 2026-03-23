package loadbalancer_test

import (
	"testing"

	"go-api-gateway/internal/loadbalancer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockChecker implements loadbalancer.HealthChecker for tests.
type mockChecker struct {
	healthy map[string]bool
}

func (m *mockChecker) IsHealthy(url string) bool {
	return m.healthy[url]
}

func TestRoundRobin_CyclesThroughURLs(t *testing.T) {
	checker := &mockChecker{healthy: map[string]bool{
		"http://a": true,
		"http://b": true,
		"http://c": true,
	}}
	rr := loadbalancer.NewRoundRobin([]string{"http://a", "http://b", "http://c"}, checker)
	results := make([]string, 6)

	for i := range 6 {
		url, err := rr.Next()
		require.NoError(t, err)
		results[i] = url
	}

	assert.Equal(t, []string{"http://a", "http://b", "http://c", "http://a", "http://b", "http://c"}, results)
}

func TestRoundRobin_SkipsUnhealthy(t *testing.T) {
	checker := &mockChecker{healthy: map[string]bool{
		"http://a": true,
		"http://b": false,
		"http://c": true,
	}}
	rr := loadbalancer.NewRoundRobin([]string{"http://a", "http://b", "http://c"}, checker)
	results := make([]string, 4)

	for i := range 4 {
		url, err := rr.Next()
		require.NoError(t, err)
		results[i] = url
	}

	assert.Equal(t, []string{"http://a", "http://c", "http://a", "http://c"}, results)
}

func TestRoundRobin_AllUnhealthy(t *testing.T) {
	checker := &mockChecker{healthy: map[string]bool{
		"http://a": false,
		"http://b": false,
		"http://c": false,
	}}
	rr := loadbalancer.NewRoundRobin([]string{"http://a", "http://b", "http://c"}, checker)
	url, err := rr.Next()

	require.ErrorIs(t, err, loadbalancer.ErrNoHealthyUpstreams)
	assert.Equal(t, "", url)
}

func TestRoundRobin_SingleHealthyBackend(t *testing.T) {
	checker := &mockChecker{healthy: map[string]bool{
		"http://a": false,
		"http://b": false,
		"http://c": true,
	}}
	rr := loadbalancer.NewRoundRobin([]string{"http://a", "http://b", "http://c"}, checker)

	for range 5 {
		url, err := rr.Next()
		require.NoError(t, err)
		assert.Equal(t, "http://c", url)
	}
}

func TestRoundRobin_RecoveryMidStream(t *testing.T) {
	checker := &mockChecker{healthy: map[string]bool{
		"http://a": true,
		"http://b": false,
		"http://c": true,
	}}
	rr := loadbalancer.NewRoundRobin([]string{"http://a", "http://b", "http://c"}, checker)
	results := make([]string, 4)

	for i := range 4 {
		url, err := rr.Next()
		require.NoError(t, err)
		results[i] = url
	}

	assert.Equal(t, []string{"http://a", "http://c", "http://a", "http://c"}, results)

	checker.healthy["http://b"] = true
	clear(results)

	for i := range 4 {
		url, err := rr.Next()
		require.NoError(t, err)
		results[i] = url
	}

	assert.Equal(t, []string{"http://a", "http://b", "http://c", "http://a"}, results)
}
