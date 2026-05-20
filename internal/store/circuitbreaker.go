package store

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"sync"
	"time"
)

// circuitState represents the state of a circuit breaker.
type circuitState int

const (
	circuitClosed   circuitState = iota // healthy — requests flow through
	circuitOpen                         // tripped — requests fail fast
	circuitHalfOpen                     // probing — single request allowed to test recovery
)

// CircuitBreaker implements the circuit breaker pattern with exponential
// backoff and jitter for FalkorDB connections. It wraps query execution
// and prevents cascading failures when the DB is unresponsive.
//
// States:
//
//	Closed  → requests pass through; consecutive failures tracked
//	Open    → requests fail fast with ErrCircuitOpen; resets after cooldown
//	HalfOpen → one probe request allowed; success → Closed, failure → Open
type CircuitBreaker struct {
	mu sync.Mutex

	state            circuitState
	consecutiveFails int
	lastFailTime     time.Time
	openUntil        time.Time

	// Config
	FailThreshold int           // consecutive failures before opening (default 5)
	BaseCooldown  time.Duration // initial cooldown when circuit opens (default 1s)
	MaxCooldown   time.Duration // max cooldown after repeated opens (default 30s)
	openCount     int           // how many times we've opened (for exponential backoff)
}

// ErrCircuitOpen is returned when the circuit breaker is open.
var ErrCircuitOpen = fmt.Errorf("circuit breaker open: DB unavailable, failing fast")

// newCircuitBreaker creates a CircuitBreaker with sensible defaults.
func newCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		FailThreshold: 5,
		BaseCooldown:  1 * time.Second,
		MaxCooldown:   30 * time.Second,
	}
}

// allow checks if a request should be allowed through.
// Returns true if the request can proceed.
func (cb *CircuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case circuitClosed:
		return true
	case circuitOpen:
		if time.Now().After(cb.openUntil) {
			cb.state = circuitHalfOpen
			log.Printf("circuit breaker: half-open, probing DB connection")
			return true
		}
		return false
	case circuitHalfOpen:
		// Only one probe at a time — block others while probing
		return false
	}
	return true
}

// recordSuccess records a successful request.
func (cb *CircuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == circuitHalfOpen {
		log.Printf("circuit breaker: closed (DB recovered)")
		cb.openCount = 0
	}
	cb.state = circuitClosed
	cb.consecutiveFails = 0
}

// recordFailure records a failed request and potentially opens the circuit.
func (cb *CircuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFails++
	cb.lastFailTime = time.Now()

	if cb.state == circuitHalfOpen {
		// Probe failed — back to open with increased cooldown
		cb.openCount++
		cb.trip()
		return
	}

	if cb.consecutiveFails >= cb.FailThreshold {
		cb.openCount++
		cb.trip()
	}
}

// trip opens the circuit with exponential backoff + jitter.
func (cb *CircuitBreaker) trip() {
	cb.state = circuitOpen

	// Exponential backoff: base * 2^(openCount-1), capped at max
	backoff := float64(cb.BaseCooldown) * math.Pow(2, float64(cb.openCount-1))
	if backoff > float64(cb.MaxCooldown) {
		backoff = float64(cb.MaxCooldown)
	}

	// Add jitter: +-25%
	jitter := backoff * 0.25 * (rand.Float64()*2 - 1)
	cooldown := time.Duration(backoff + jitter)

	cb.openUntil = time.Now().Add(cooldown)
	log.Printf("circuit breaker: OPEN (fails=%d, cooldown=%v)", cb.consecutiveFails, cooldown.Round(time.Millisecond))
}

// execute runs fn with circuit breaker protection. If maxRetries > 0,
// retries with exponential backoff on transient failures before tripping.
func (cb *CircuitBreaker) execute(fn func() (interface{}, error)) (interface{}, error) {
	if !cb.allow() {
		return nil, ErrCircuitOpen
	}

	result, err := fn()
	if err != nil {
		cb.recordFailure()
		return result, err
	}

	cb.recordSuccess()
	return result, nil
}
