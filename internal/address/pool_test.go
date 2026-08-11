package address_test

import (
	"errors"
	"net"
	"testing"

	"github.com/Eser-s-Organization/ipv6mesh/internal/address"
)

func TestPoolParsesCanonicalIPv4AndSkipsNetworkAndBroadcast(t *testing.T) {
	pool, err := address.NewPool("10.42.0.0/30")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	candidates := pool.Candidates()
	if len(candidates) != 2 || candidates[0].String() != "10.42.0.1" || candidates[1].String() != "10.42.0.2" {
		t.Fatalf("candidates = %v, want [10.42.0.1 10.42.0.2]", candidates)
	}
	if !pool.Contains(net.ParseIP("10.42.0.1")) || !pool.Contains(net.ParseIP("10.42.0.0")) || pool.Contains(net.ParseIP("10.42.0.4")) {
		t.Fatal("pool Contains did not follow CIDR membership")
	}
}

func TestPoolRejectsIPv6AndHostBits(t *testing.T) {
	for _, cidr := range []string{"2001:db8::/64", "10.42.0.1/24", "not-a-cidr"} {
		if _, err := address.NewPool(cidr); !errors.Is(err, address.ErrInvalidPool) {
			t.Errorf("NewPool(%q) error = %v, want ErrInvalidPool", cidr, err)
		}
	}
}

func TestPoolNextReturnsTheFirstUnoccupiedHost(t *testing.T) {
	pool, err := address.NewPool("10.42.0.0/30")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	next, err := pool.Next([]net.IP{net.ParseIP("10.42.0.1")})
	if err != nil {
		t.Fatalf("next address: %v", err)
	}
	if next.String() != "10.42.0.2" {
		t.Fatalf("next address = %s, want 10.42.0.2", next)
	}
}

func TestPoolExhaustionIsTyped(t *testing.T) {
	pool, err := address.NewPool("10.42.0.0/30")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	_, err = pool.Next([]net.IP{net.ParseIP("10.42.0.1"), net.ParseIP("10.42.0.2")})
	if !errors.Is(err, address.ErrPoolExhausted) {
		t.Fatalf("exhaustion error = %v, want ErrPoolExhausted", err)
	}
	var exhausted *address.PoolExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("exhaustion error %T is not a PoolExhaustedError", err)
	}
}

func TestPoolWithNoUsableHostsExhausts(t *testing.T) {
	pool, err := address.NewPool("10.42.0.0/31")
	if err != nil {
		t.Fatalf("create /31 pool: %v", err)
	}
	if got := pool.Candidates(); len(got) != 0 {
		t.Fatalf("/31 candidates = %v, want none", got)
	}
	if _, err := pool.Next(); !errors.Is(err, address.ErrPoolExhausted) {
		t.Fatalf("/31 Next error = %v, want ErrPoolExhausted", err)
	}
}

func TestPoolContainsKeepsTheExactMaskForSlash32(t *testing.T) {
	pool, err := address.NewPool("10.42.0.2/32")
	if err != nil {
		t.Fatalf("create /32 pool: %v", err)
	}
	if !pool.Contains(net.ParseIP("10.42.0.2")) || pool.Contains(net.ParseIP("10.42.0.3")) {
		t.Fatal("/32 pool membership was widened to /31")
	}
}
