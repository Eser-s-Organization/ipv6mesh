// Package path implements the local Direct/Relay path state machine. It does
// not choose cryptographic keys or modify routes; those operations stay in the
// WireGuard and relay adapters.
package path

import (
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidOptions        = errors.New("path selector options are invalid")
	ErrInvalidState          = errors.New("path selector state is invalid")
	ErrOutOfOrderObservation = errors.New("path observation is older than the previous observation")
)

type State string

const (
	Direct       State = "direct"
	Suspect      State = "suspect"
	Relay        State = "relay"
	Disconnected State = "disconnected"
)

type Observation struct {
	At            time.Time
	DirectHealthy bool
	RelayHealthy  bool
	MemberActive  bool
}

type Options struct {
	Clock                    func() time.Time
	DirectFailureThreshold   int
	RecoverySuccessThreshold int
}

type Selector struct {
	mu                       sync.Mutex
	clock                    func() time.Time
	directFailureThreshold   int
	recoverySuccessThreshold int
	state                    State
	directFailures           int
	directSuccesses          int
	lastAt                   time.Time
}

func NewSelector(options Options, initial State) (*Selector, error) {
	if !validState(initial) {
		return nil, ErrInvalidState
	}
	initial = State(strings.ToLower(string(initial)))
	clock := options.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if options.DirectFailureThreshold <= 0 {
		options.DirectFailureThreshold = 2
	}
	if options.RecoverySuccessThreshold <= 0 {
		options.RecoverySuccessThreshold = 2
	}
	return &Selector{clock: clock, directFailureThreshold: options.DirectFailureThreshold, recoverySuccessThreshold: options.RecoverySuccessThreshold, state: initial}, nil
}

func (selector *Selector) Observe(observation Observation) State {
	state, _ := selector.ObserveWithError(observation)
	return state
}

func (selector *Selector) ObserveWithError(observation Observation) (State, error) {
	if selector == nil {
		return Disconnected, ErrInvalidOptions
	}
	selector.mu.Lock()
	defer selector.mu.Unlock()
	at := observation.At
	if at.IsZero() {
		at = selector.clock().UTC()
	}
	if !selector.lastAt.IsZero() && at.Before(selector.lastAt) {
		return selector.state, ErrOutOfOrderObservation
	}
	selector.lastAt = at
	if !observation.MemberActive {
		selector.state = Disconnected
		selector.directFailures = 0
		selector.directSuccesses = 0
		return selector.state, nil
	}

	switch selector.state {
	case Direct:
		if observation.DirectHealthy {
			selector.directFailures = 0
			selector.directSuccesses = 0
			return selector.state, nil
		}
		selector.directSuccesses = 0
		selector.directFailures++
		if selector.directFailures < selector.directFailureThreshold {
			selector.state = Suspect
		} else if observation.RelayHealthy {
			selector.state = Relay
		} else {
			selector.state = Disconnected
		}
	case Suspect:
		if observation.DirectHealthy {
			selector.directFailures = 0
			selector.directSuccesses++
			if selector.directSuccesses >= selector.recoverySuccessThreshold {
				selector.state = Direct
			}
			return selector.state, nil
		}
		selector.directSuccesses = 0
		selector.directFailures++
		if selector.directFailures >= selector.directFailureThreshold {
			if observation.RelayHealthy {
				selector.state = Relay
			} else {
				selector.state = Disconnected
			}
		}
	case Relay:
		if observation.DirectHealthy {
			selector.directFailures = 0
			selector.directSuccesses++
			if selector.directSuccesses >= selector.recoverySuccessThreshold {
				selector.state = Direct
			}
			return selector.state, nil
		}
		selector.directSuccesses = 0
		selector.directFailures = 0
		if !observation.RelayHealthy {
			selector.state = Disconnected
		}
	case Disconnected:
		if observation.DirectHealthy {
			selector.directFailures = 0
			selector.directSuccesses++
			if selector.directSuccesses >= selector.recoverySuccessThreshold {
				selector.state = Direct
			} else {
				selector.state = Suspect
			}
		} else if observation.RelayHealthy {
			selector.directFailures = 0
			selector.directSuccesses = 0
			selector.state = Relay
		}
	default:
		return selector.state, ErrInvalidState
	}
	return selector.state, nil
}

func (selector *Selector) State() State {
	if selector == nil {
		return Disconnected
	}
	selector.mu.Lock()
	defer selector.mu.Unlock()
	return selector.state
}

func validState(state State) bool {
	switch State(strings.ToLower(string(state))) {
	case Direct, Suspect, Relay, Disconnected:
		return true
	default:
		return false
	}
}
