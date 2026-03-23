package circuitbreaker

import (
	"errors"
	"sync"
	"time"
)

// ErrOpen is returned when the circuit breaker is open and requests
// are being rejected without contacting the upstream.
var ErrOpen = errors.New("circuit breaker is open")

type state int

const (
	stateClosed state = iota
	stateOpen
	stateHalfOpen
)

// Breaker implements a circuit breaker with three states: closed, open, half-open.
// It is safe for concurrent use.
type Breaker struct {
	mu          sync.Mutex
	state       state
	failures    int
	maxFailures int
	timeout     time.Duration
	openedAt    time.Time
}

// New creates a Breaker that opens after maxFailures consecutive failures
// and stays open for the given timeout before allowing a test request.
func NewBreaker(maxFailures int, timeout time.Duration) *Breaker {
	return &Breaker{
		state:       stateClosed,
		maxFailures: maxFailures,
		timeout:     timeout,
	}
}

// Allow checks whether a request should be allowed through.
// Returns nil if the request can proceed, or ErrOpen if the breaker is open.
//
// In the half-open state, only one request is allowed through (the test request).
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case stateOpen:
		if time.Since(b.openedAt) >= b.timeout {
			b.state = stateHalfOpen
			return nil
		}
		return ErrOpen
	case stateHalfOpen:
		return ErrOpen
	default:
		return nil
	}
}

// RecordSuccess records a successful request.
// If the breaker is half-open, this transitions it back to closed.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.failures = 0
	if b.state == stateHalfOpen {
		b.state = stateClosed
	}
}

// RecordFailure records a failed request.
// If failures reach maxFailures, the breaker transitions to open.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.failures++
	if b.state == stateHalfOpen || b.failures >= b.maxFailures {
		b.state = stateOpen
		b.openedAt = time.Now()
	}
}

// State returns the current state as a string. Used for logging/metrics.
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}
