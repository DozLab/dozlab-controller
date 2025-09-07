package circuitbreaker

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// State represents the circuit breaker state (following dozlab-api state patterns)
type State int

const (
	StateClosed State = iota
	StateHalfOpen
	StateOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateHalfOpen:
		return "HALF_OPEN" 
	case StateOpen:
		return "OPEN"
	default:
		return "UNKNOWN"
	}
}

// Config holds circuit breaker configuration (following dozlab-api config patterns)
type Config struct {
	Name               string
	MaxRequests        uint32
	Interval           time.Duration
	Timeout            time.Duration
	ReadyToTrip        func(counts Counts) bool
	OnStateChange      func(name string, from State, to State)
	IsSuccessful       func(err error) bool
}

// Counts holds circuit breaker statistics (following dozlab-api metrics patterns)
type Counts struct {
	Requests             uint32
	TotalSuccesses       uint32
	TotalFailures        uint32
	ConsecutiveSuccesses uint32
	ConsecutiveFailures  uint32
}

// CircuitBreaker implements the circuit breaker pattern for external dependencies
type CircuitBreaker struct {
	name          string
	maxRequests   uint32
	interval      time.Duration
	timeout       time.Duration
	readyToTrip   func(counts Counts) bool
	isSuccessful  func(err error) bool
	onStateChange func(name string, from State, to State)

	mutex      sync.Mutex
	state      State
	generation uint64
	counts     Counts
	expiry     time.Time
}

// NewCircuitBreaker creates a new circuit breaker (following dozlab-api factory patterns)
func NewCircuitBreaker(config Config) *CircuitBreaker {
	cb := &CircuitBreaker{
		name:          config.Name,
		maxRequests:   config.MaxRequests,
		interval:      config.Interval,
		timeout:       config.Timeout,
		readyToTrip:   config.ReadyToTrip,
		isSuccessful:  config.IsSuccessful,
		onStateChange: config.OnStateChange,
	}

	// Set defaults if not provided
	if cb.maxRequests == 0 {
		cb.maxRequests = 1
	}
	if cb.interval == 0 {
		cb.interval = time.Duration(0)
	}
	if cb.timeout == 0 {
		cb.timeout = 60 * time.Second
	}
	if cb.readyToTrip == nil {
		cb.readyToTrip = func(counts Counts) bool {
			return counts.ConsecutiveFailures > 5
		}
	}
	if cb.isSuccessful == nil {
		cb.isSuccessful = func(err error) bool {
			return err == nil
		}
	}

	cb.toNewGeneration(time.Now())
	return cb
}

// Execute runs the given function within the circuit breaker (following dozlab-api execution pattern)
func (cb *CircuitBreaker) Execute(fn func() error) error {
	generation, err := cb.beforeRequest()
	if err != nil {
		return err
	}

	defer func() {
		if e := recover(); e != nil {
			cb.afterRequest(generation, false)
			panic(e)
		}
	}()

	result := fn()
	cb.afterRequest(generation, cb.isSuccessful(result))
	return result
}

// ExecuteWithContext runs the given function with context within the circuit breaker
func (cb *CircuitBreaker) ExecuteWithContext(ctx context.Context, fn func(context.Context) error) error {
	generation, err := cb.beforeRequest()
	if err != nil {
		return err
	}

	defer func() {
		if e := recover(); e != nil {
			cb.afterRequest(generation, false)
			panic(e)
		}
	}()

	result := fn(ctx)
	cb.afterRequest(generation, cb.isSuccessful(result))
	return result
}

// GetState returns the current state of the circuit breaker
func (cb *CircuitBreaker) GetState() State {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	
	now := time.Now()
	state, _ := cb.currentState(now)
	return state
}

// GetCounts returns the current counts
func (cb *CircuitBreaker) GetCounts() Counts {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	return cb.counts
}

// Reset resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	cb.toNewGeneration(time.Now())
}

// GetStats returns circuit breaker statistics (following dozlab-api stats pattern)
func (cb *CircuitBreaker) GetStats() map[string]interface{} {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	
	return map[string]interface{}{
		"name":                    cb.name,
		"state":                   cb.state.String(),
		"requests":                cb.counts.Requests,
		"total_successes":         cb.counts.TotalSuccesses,
		"total_failures":          cb.counts.TotalFailures,
		"consecutive_successes":   cb.counts.ConsecutiveSuccesses,
		"consecutive_failures":    cb.counts.ConsecutiveFailures,
		"generation":              cb.generation,
		"expiry":                  cb.expiry.Unix(),
	}
}

// Private methods

func (cb *CircuitBreaker) beforeRequest() (uint64, error) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	now := time.Now()
	state, generation := cb.currentState(now)

	if state == StateOpen {
		return generation, fmt.Errorf("circuit breaker '%s' is open", cb.name)
	} else if state == StateHalfOpen && cb.counts.Requests >= cb.maxRequests {
		return generation, fmt.Errorf("circuit breaker '%s' is half-open with too many requests", cb.name)
	}

	cb.counts.Requests++
	return generation, nil
}

func (cb *CircuitBreaker) afterRequest(before uint64, success bool) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	now := time.Now()
	state, generation := cb.currentState(now)
	if generation != before {
		return
	}

	if success {
		cb.onSuccess(state, now)
	} else {
		cb.onFailure(state, now)
	}
}

func (cb *CircuitBreaker) onSuccess(state State, now time.Time) {
	cb.counts.TotalSuccesses++
	cb.counts.ConsecutiveSuccesses++
	cb.counts.ConsecutiveFailures = 0

	if state == StateHalfOpen {
		cb.setState(StateClosed, now)
	}
}

func (cb *CircuitBreaker) onFailure(state State, now time.Time) {
	cb.counts.TotalFailures++
	cb.counts.ConsecutiveFailures++
	cb.counts.ConsecutiveSuccesses = 0

	if cb.readyToTrip(cb.counts) {
		cb.setState(StateOpen, now)
	}
}

func (cb *CircuitBreaker) currentState(now time.Time) (State, uint64) {
	switch cb.state {
	case StateClosed:
		if !cb.expiry.IsZero() && cb.expiry.Before(now) {
			cb.toNewGeneration(now)
		}
	case StateOpen:
		if cb.expiry.Before(now) {
			cb.setState(StateHalfOpen, now)
		}
	}
	return cb.state, cb.generation
}

func (cb *CircuitBreaker) setState(state State, now time.Time) {
	if cb.state == state {
		return
	}

	prev := cb.state
	cb.state = state

	cb.toNewGeneration(now)

	if cb.onStateChange != nil {
		cb.onStateChange(cb.name, prev, state)
	}
}

func (cb *CircuitBreaker) toNewGeneration(now time.Time) {
	cb.generation++
	cb.counts = Counts{}

	var zero time.Time
	switch cb.state {
	case StateClosed:
		if cb.interval == 0 {
			cb.expiry = zero
		} else {
			cb.expiry = now.Add(cb.interval)
		}
	case StateOpen:
		cb.expiry = now.Add(cb.timeout)
	default: // StateHalfOpen
		cb.expiry = zero
	}
}

// Manager manages multiple circuit breakers (following dozlab-api manager patterns)
type Manager struct {
	breakers map[string]*CircuitBreaker
	mutex    sync.RWMutex
}

// NewManager creates a new circuit breaker manager
func NewManager() *Manager {
	return &Manager{
		breakers: make(map[string]*CircuitBreaker),
	}
}

// Register registers a circuit breaker with the manager
func (m *Manager) Register(name string, config Config) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	
	config.Name = name
	m.breakers[name] = NewCircuitBreaker(config)
}

// Execute executes a function through the named circuit breaker
func (m *Manager) Execute(name string, fn func() error) error {
	m.mutex.RLock()
	cb, exists := m.breakers[name]
	m.mutex.RUnlock()
	
	if !exists {
		return fmt.Errorf("circuit breaker '%s' not found", name)
	}
	
	return cb.Execute(fn)
}

// GetState returns the state of the named circuit breaker
func (m *Manager) GetState(name string) (State, error) {
	m.mutex.RLock()
	cb, exists := m.breakers[name]
	m.mutex.RUnlock()
	
	if !exists {
		return StateClosed, fmt.Errorf("circuit breaker '%s' not found", name)
	}
	
	return cb.GetState(), nil
}

// GetStats returns statistics for all circuit breakers
func (m *Manager) GetAllStats() map[string]map[string]interface{} {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	
	stats := make(map[string]map[string]interface{})
	for name, cb := range m.breakers {
		stats[name] = cb.GetStats()
	}
	return stats
}

// Reset resets the named circuit breaker
func (m *Manager) Reset(name string) error {
	m.mutex.RLock()
	cb, exists := m.breakers[name]
	m.mutex.RUnlock()
	
	if !exists {
		return fmt.Errorf("circuit breaker '%s' not found", name)
	}
	
	cb.Reset()
	return nil
}