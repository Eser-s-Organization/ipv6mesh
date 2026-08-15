package path

import (
	"errors"
	"testing"
	"time"
)

func TestSelectorUsesHysteresisForDirectRelayRecovery(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)}
	selector, err := NewSelector(Options{Clock: clock.Now, DirectFailureThreshold: 2, RecoverySuccessThreshold: 2}, Direct)
	if err != nil {
		t.Fatal(err)
	}

	if got := selector.Observe(Observation{DirectHealthy: false, RelayHealthy: true, MemberActive: true}); got != Suspect {
		t.Fatalf("one missed direct probe = %s, want suspect", got)
	}
	clock.Advance(time.Second)
	if got := selector.Observe(Observation{DirectHealthy: false, RelayHealthy: true, MemberActive: true}); got != Relay {
		t.Fatalf("two missed direct probes = %s, want relay", got)
	}
	clock.Advance(time.Second)
	if got := selector.Observe(Observation{DirectHealthy: true, RelayHealthy: true, MemberActive: true}); got != Relay {
		t.Fatalf("one direct recovery probe = %s, want relay hysteresis", got)
	}
	clock.Advance(time.Second)
	if got := selector.Observe(Observation{DirectHealthy: true, RelayHealthy: true, MemberActive: true}); got != Direct {
		t.Fatalf("two direct recovery probes = %s, want direct", got)
	}
}

func TestSelectorDisconnectsWhenMemberOrBothPathsAreUnavailable(t *testing.T) {
	selector, err := NewSelector(Options{DirectFailureThreshold: 2, RecoverySuccessThreshold: 2}, Direct)
	if err != nil {
		t.Fatal(err)
	}
	if got := selector.Observe(Observation{DirectHealthy: false, RelayHealthy: false, MemberActive: true}); got != Suspect {
		t.Fatalf("first total failure = %s, want suspect", got)
	}
	if got := selector.Observe(Observation{DirectHealthy: false, RelayHealthy: false, MemberActive: true}); got != Disconnected {
		t.Fatalf("second total failure = %s, want disconnected", got)
	}
	if got := selector.Observe(Observation{DirectHealthy: true, RelayHealthy: false, MemberActive: false}); got != Disconnected {
		t.Fatalf("inactive member state = %s, want disconnected", got)
	}
}

func TestSelectorRejectsOutOfOrderObservationsAndKeepsState(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)}
	selector, err := NewSelector(Options{Clock: clock.Now}, Direct)
	if err != nil {
		t.Fatal(err)
	}
	first := clock.Now()
	if got := selector.Observe(Observation{At: first, DirectHealthy: false, RelayHealthy: true, MemberActive: true}); got != Suspect {
		t.Fatalf("first state = %s", got)
	}
	_, err = selector.ObserveWithError(Observation{At: first.Add(-time.Second), DirectHealthy: true, RelayHealthy: true, MemberActive: true})
	if !errors.Is(err, ErrOutOfOrderObservation) {
		t.Fatalf("out-of-order error = %v", err)
	}
	if selector.State() != Suspect {
		t.Fatalf("out-of-order observation changed state to %s", selector.State())
	}
}

type fakeClock struct{ now time.Time }

func (clock *fakeClock) Now() time.Time              { return clock.now }
func (clock *fakeClock) Advance(delta time.Duration) { clock.now = clock.now.Add(delta) }
