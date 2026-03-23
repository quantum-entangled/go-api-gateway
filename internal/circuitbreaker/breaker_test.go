package circuitbreaker_test

import (
	"testing"
	"time"

	"go-api-gateway/internal/circuitbreaker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBreaker_StartsInClosedState(t *testing.T) {
	maxFailures := 3
	cb := circuitbreaker.NewBreaker(maxFailures, 1*time.Second)

	require.NoError(t, cb.Allow())
	assert.Equal(t, "closed", cb.State())
}

func TestBreaker_OpensAfterMaxFailures(t *testing.T) {
	maxFailures := 3
	cb := circuitbreaker.NewBreaker(maxFailures, 1*time.Second)

	for range maxFailures {
		cb.RecordFailure()
	}

	assert.Equal(t, "open", cb.State())
	assert.ErrorIs(t, cb.Allow(), circuitbreaker.ErrOpen)
}

func TestBreaker_DoesNotOpenBelowThreshold(t *testing.T) {
	maxFailures := 3
	cb := circuitbreaker.NewBreaker(maxFailures, 10*time.Millisecond)

	for range maxFailures - 1 {
		cb.RecordFailure()
	}

	require.NoError(t, cb.Allow())
	assert.Equal(t, "closed", cb.State())
}

func TestBreaker_SuccessResetsFailureCount(t *testing.T) {
	maxFailures := 3
	cb := circuitbreaker.NewBreaker(maxFailures, 10*time.Millisecond)

	for range maxFailures - 1 {
		cb.RecordFailure()
	}
	cb.RecordSuccess()
	for range maxFailures - 1 {
		cb.RecordFailure()
	}

	require.NoError(t, cb.Allow())
	assert.Equal(t, "closed", cb.State())
}

func TestBreaker_TransitionsToHalfOpenAfterTimeout(t *testing.T) {
	maxFailures := 3
	cb := circuitbreaker.NewBreaker(maxFailures, 10*time.Millisecond)

	for range maxFailures {
		cb.RecordFailure()
	}

	time.Sleep(15 * time.Millisecond)

	require.NoError(t, cb.Allow())
	assert.Equal(t, "half-open", cb.State())
}

func TestBreaker_HalfOpenSuccessCloses(t *testing.T) {
	maxFailures := 3
	cb := circuitbreaker.NewBreaker(maxFailures, 10*time.Millisecond)

	for range maxFailures {
		cb.RecordFailure()
	}

	time.Sleep(15 * time.Millisecond)
	cb.Allow()
	cb.RecordSuccess()

	require.NoError(t, cb.Allow())
	assert.Equal(t, "closed", cb.State())
}

func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	maxFailures := 3
	cb := circuitbreaker.NewBreaker(maxFailures, 10*time.Millisecond)

	for range maxFailures {
		cb.RecordFailure()
	}

	time.Sleep(15 * time.Millisecond)
	cb.Allow()
	cb.RecordFailure()

	assert.Equal(t, "open", cb.State())
	assert.ErrorIs(t, cb.Allow(), circuitbreaker.ErrOpen)
}

func TestBreaker_HalfOpenRejectsSecondRequest(t *testing.T) {
	maxFailures := 3
	cb := circuitbreaker.NewBreaker(maxFailures, 10*time.Millisecond)

	for range maxFailures {
		cb.RecordFailure()
	}

	time.Sleep(15 * time.Millisecond)
	cb.Allow()

	assert.Equal(t, "half-open", cb.State())
	assert.ErrorIs(t, cb.Allow(), circuitbreaker.ErrOpen)
}
