// Package address provides stateless virtual IPv4 pool traversal. Allocation
// ownership remains in the repository uniqueness constraint rather than in
// this package.
package address

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
)

var (
	ErrInvalidPool   = errors.New("invalid IPv4 pool")
	ErrPoolExhausted = errors.New("IPv4 pool exhausted")
)

// PoolExhaustedError preserves the pool that had no usable host while still
// matching ErrPoolExhausted through errors.Is.
type PoolExhaustedError struct {
	CIDR string
}

func (e *PoolExhaustedError) Error() string {
	if e == nil || e.CIDR == "" {
		return ErrPoolExhausted.Error()
	}
	return fmt.Sprintf("%s: %s", ErrPoolExhausted, e.CIDR)
}

func (e *PoolExhaustedError) Unwrap() error { return ErrPoolExhausted }

// Pool is an immutable canonical IPv4 CIDR and its usable host range.
type Pool struct {
	cidr        string
	network     uint32
	prefix      int
	first       uint32
	last        uint32
	usableCount uint64
	valid       bool
}

// NewPool parses a canonical IPv4 CIDR. IPv6 CIDRs, invalid CIDRs, and CIDRs
// with host bits are rejected.
func NewPool(cidr string) (*Pool, error) {
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPool, err)
	}
	ip4 := ip.To4()
	networkIP := network.IP.To4()
	if ip4 == nil || networkIP == nil {
		return nil, fmt.Errorf("%w: pool must be IPv4", ErrInvalidPool)
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || !ip4.Equal(networkIP) {
		return nil, fmt.Errorf("%w: pool must be a canonical IPv4 network", ErrInvalidPool)
	}
	networkValue := binary.BigEndian.Uint32(networkIP)
	size := uint64(1) << uint(32-ones)
	pool := &Pool{
		cidr:    network.String(),
		network: networkValue,
		prefix:  ones,
		valid:   true,
	}
	if size > 2 {
		pool.first = networkValue + 1
		pool.last = networkValue + uint32(size-2)
		pool.usableCount = size - 2
	}
	return pool, nil
}

// Parse is a descriptive alias for NewPool.
func Parse(cidr string) (*Pool, error) { return NewPool(cidr) }

// CIDR returns the normalized network notation.
func (pool *Pool) CIDR() string {
	if pool == nil {
		return ""
	}
	return pool.cidr
}

func (pool *Pool) String() string { return pool.CIDR() }

// Size returns the number of usable host addresses.
func (pool *Pool) Size() uint64 {
	if pool == nil || !pool.valid {
		return 0
	}
	return pool.usableCount
}

// Contains reports CIDR membership. Network and broadcast addresses are
// contained by the CIDR but are intentionally absent from Candidates/Next.
func (pool *Pool) Contains(ip net.IP) bool {
	if pool == nil || !pool.valid || ip == nil || ip.To4() == nil {
		return false
	}
	value := binary.BigEndian.Uint32(ip.To4())
	ones := uint32(pool.prefix)
	mask := uint32(0)
	if ones > 0 {
		mask = ^uint32(0) << (32 - ones)
	}
	return value&mask == pool.network&mask
}

// Usable reports whether an address is a candidate that can be allocated.
func (pool *Pool) Usable(ip net.IP) bool {
	if pool == nil || !pool.valid || ip == nil || ip.To4() == nil || pool.usableCount == 0 {
		return false
	}
	value := binary.BigEndian.Uint32(ip.To4())
	return value >= pool.first && value <= pool.last
}

// ForEachCandidate visits usable addresses in stable ascending order without
// materializing a potentially large pool.
func (pool *Pool) ForEachCandidate(visit func(net.IP) error) error {
	if pool == nil || !pool.valid {
		return ErrInvalidPool
	}
	if visit == nil {
		return ErrInvalidPool
	}
	if pool.usableCount == 0 {
		return nil
	}
	for value := pool.first; ; value++ {
		ip := make(net.IP, net.IPv4len)
		binary.BigEndian.PutUint32(ip, value)
		if err := visit(ip); err != nil {
			return err
		}
		if value == pool.last {
			break
		}
	}
	return nil
}

// Candidates materializes the stable candidate order for ordinary-sized
// pools. For very large CIDRs callers should use ForEachCandidate instead.
func (pool *Pool) Candidates() []net.IP {
	if pool == nil || !pool.valid || pool.usableCount == 0 || pool.usableCount > 1<<20 {
		return []net.IP{}
	}
	candidates := make([]net.IP, 0, int(pool.usableCount))
	_ = pool.ForEachCandidate(func(ip net.IP) error {
		candidates = append(candidates, ip)
		return nil
	})
	return candidates
}

// CandidatesWithError is the error-returning form for callers that want to
// distinguish an invalid zero Pool from an empty valid pool.
func (pool *Pool) CandidatesWithError() ([]net.IP, error) {
	if pool == nil || !pool.valid {
		return nil, ErrInvalidPool
	}
	candidates := pool.Candidates()
	if pool.usableCount > 1<<20 {
		return nil, fmt.Errorf("%w: use ForEachCandidate for large pools", ErrInvalidPool)
	}
	return candidates, nil
}

// Next returns the first candidate not present in occupied. The variadic
// form accepts common caller representations (map[string]struct{}, []net.IP,
// []string, or a single net.IP) while keeping the pool itself stateless.
func (pool *Pool) Next(occupied ...any) (net.IP, error) {
	if pool == nil || !pool.valid {
		return nil, ErrInvalidPool
	}
	used := make(map[uint32]struct{})
	for _, value := range occupied {
		addOccupied(used, value)
	}
	var result net.IP
	err := pool.ForEachCandidate(func(candidate net.IP) error {
		value := binary.BigEndian.Uint32(candidate.To4())
		if _, exists := used[value]; !exists {
			result = append(net.IP(nil), candidate...)
			return errStop
		}
		return nil
	})
	if !errors.Is(err, errStop) && err != nil {
		return nil, err
	}
	if result == nil {
		return nil, &PoolExhaustedError{CIDR: pool.cidr}
	}
	return result, nil
}

// NextAvailable is a typed convenience form for callers with a net.IP slice.
func (pool *Pool) NextAvailable(occupied []net.IP) (net.IP, error) {
	return pool.Next(occupied)
}

// RandomNext returns a randomly selected usable address, starting at a
// cryptographically random position and wrapping around the pool until it
// finds an unoccupied address. The caller still owns the final uniqueness
// check: concurrent allocators must handle a repository conflict and retry.
// A nil random reader uses crypto/rand.Reader.
func (pool *Pool) RandomNext(random io.Reader, occupied ...any) (net.IP, error) {
	if pool == nil || !pool.valid {
		return nil, ErrInvalidPool
	}
	if pool.usableCount == 0 {
		return nil, &PoolExhaustedError{CIDR: pool.cidr}
	}
	if random == nil {
		random = rand.Reader
	}
	used := make(map[uint32]struct{})
	for _, value := range occupied {
		addOccupied(used, value)
	}
	limit := new(big.Int).SetUint64(pool.usableCount)
	start, err := rand.Int(random, limit)
	if err != nil {
		return nil, fmt.Errorf("select random IPv4 address: %w", err)
	}
	startOffset := start.Uint64()
	for offset := uint64(0); offset < pool.usableCount; offset++ {
		candidateOffset := (startOffset + offset) % pool.usableCount
		value := pool.first + uint32(candidateOffset)
		if _, exists := used[value]; exists {
			continue
		}
		candidate := make(net.IP, net.IPv4len)
		binary.BigEndian.PutUint32(candidate, value)
		return candidate, nil
	}
	return nil, &PoolExhaustedError{CIDR: pool.cidr}
}

var errStop = errors.New("stop candidate traversal")

func addOccupied(used map[uint32]struct{}, value any) {
	switch typed := value.(type) {
	case net.IP:
		if ip := typed.To4(); ip != nil {
			used[binary.BigEndian.Uint32(ip)] = struct{}{}
		}
	case string:
		if ip := net.ParseIP(strings.TrimSpace(typed)); ip != nil && ip.To4() != nil {
			used[binary.BigEndian.Uint32(ip.To4())] = struct{}{}
		}
	case []net.IP:
		for _, ip := range typed {
			addOccupied(used, ip)
		}
	case []string:
		for _, ip := range typed {
			addOccupied(used, ip)
		}
	case map[string]struct{}:
		for ip := range typed {
			addOccupied(used, ip)
		}
	case map[string]bool:
		for ip, present := range typed {
			if present {
				addOccupied(used, ip)
			}
		}
	}
}
