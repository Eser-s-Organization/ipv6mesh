// Package enrollment implements the transactional invite-to-membership
// control-plane flow. It never accepts or returns private key material.
package enrollment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/address"
	"github.com/Eser-s-Organization/ipv6mesh/internal/auth"
	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/db"
)

var (
	ErrInvalidInviteToken = errors.New("invalid invite token")
	ErrInvalidRequest     = errors.New("invalid enrollment request")
)

// InviteReference is the safe parsed form of an invite. It contains the
// lookup ID and verifier only; the raw secret is not retained.
type InviteReference struct {
	ID        string
	TokenHash string
}

// ParseInviteToken parses the one-time token format <inviteID>.<secret> and
// hashes the complete raw token for repository lookup.
func ParseInviteToken(raw string) (InviteReference, error) {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return InviteReference{}, errors.Join(ErrInvalidInviteToken, control.ErrValidation)
	}
	separator := strings.IndexByte(raw, '.')
	if separator <= 0 || separator == len(raw)-1 || strings.IndexByte(raw[separator+1:], '.') >= 0 {
		return InviteReference{}, errors.Join(ErrInvalidInviteToken, control.ErrValidation)
	}
	return InviteReference{ID: raw[:separator], TokenHash: auth.HashToken(raw)}, nil
}

// Request contains the node identity and client metadata supplied during
// enrollment. NodeID is optional; an ID is generated when it is empty.
type Request struct {
	InviteToken   string
	Invite        string
	NodeID        string
	DisplayName   string
	PublicKey     string
	Platform      string
	ClientVersion string
}

// EnrollmentRequest is a descriptive alias for Request.
type EnrollmentRequest = Request

// Result contains the newly persisted control-plane objects and the claims
// needed to issue a node session. No private key is present.
type Result struct {
	Node             control.Node
	Membership       control.Membership
	Network          control.Network
	Subject          string
	NetworkID        string
	SessionSubject   string
	SessionNetworkID string
}

// InviteTokenParser allows tests or a deployment adapter to inject token
// parsing without changing the transactional service.
type InviteTokenParser func(string) (InviteReference, error)

// ServiceOptions contains deterministic dependencies for enrollment tests.
type ServiceOptions struct {
	Clock            func() time.Time
	NewID            func() string
	IDGenerator      func() string
	ParseInviteToken InviteTokenParser
	TokenParser      InviteTokenParser
}

// Service coordinates enrollment against a transactional repository.
type Service struct {
	repository db.TransactionalRepository
	clock      func() time.Time
	newID      func() string
	parseToken InviteTokenParser
}

// NewService creates an enrollment service with production dependencies.
func NewService(repository db.TransactionalRepository, options ...ServiceOptions) *Service {
	if len(options) > 0 {
		return NewServiceWithOptions(repository, options[0])
	}
	return NewServiceWithOptions(repository, ServiceOptions{})
}

// NewServiceWithOptions creates an enrollment service with injectable clock,
// ID generator, and invite parser.
func NewServiceWithOptions(repository db.TransactionalRepository, options ServiceOptions) *Service {
	clock := options.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	newID := options.NewID
	if newID == nil {
		newID = options.IDGenerator
	}
	if newID == nil {
		newID = randomID
	}
	parseToken := options.ParseInviteToken
	if parseToken == nil {
		parseToken = options.TokenParser
	}
	if parseToken == nil {
		parseToken = ParseInviteToken
	}
	return &Service{repository: repository, clock: clock, newID: newID, parseToken: parseToken}
}

// Enroll performs node creation, invite consumption, and random address
// assignment in one repository transaction. The selected address is persisted
// by the repository, so it remains stable for the life of the membership.
func (service *Service) Enroll(ctx context.Context, request Request) (Result, error) {
	if service == nil || service.repository == nil {
		return Result{}, db.ErrDatabaseUnavailable
	}
	rawToken := request.InviteToken
	if rawToken == "" {
		rawToken = request.Invite
	}
	reference, err := service.parseToken(rawToken)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(request.DisplayName) == "" ||
		strings.TrimSpace(request.PublicKey) == "" ||
		strings.TrimSpace(request.Platform) == "" ||
		strings.TrimSpace(request.ClientVersion) == "" {
		return Result{}, errors.Join(ErrInvalidRequest, control.ErrValidation)
	}
	nodeID := strings.TrimSpace(request.NodeID)
	if nodeID == "" {
		nodeID = strings.TrimSpace(service.newID())
	}
	if nodeID == "" {
		return Result{}, errors.Join(ErrInvalidRequest, control.ErrValidation)
	}
	now := service.clock().UTC()
	var result Result
	err = service.repository.WithTransaction(ctx, func(transactionContext context.Context, transaction db.Repository) error {
		node := control.Node{
			ID:            nodeID,
			DisplayName:   request.DisplayName,
			PublicKey:     request.PublicKey,
			Platform:      request.Platform,
			ClientVersion: request.ClientVersion,
			LastSeen:      now,
		}
		if err := transaction.AddNode(transactionContext, node); err != nil {
			return err
		}
		invite, err := transaction.ConsumeInviteForNode(transactionContext, reference.ID, reference.TokenHash, now, node.ID)
		if err != nil {
			return err
		}
		network, err := transaction.GetNetwork(transactionContext, invite.NetworkID)
		if err != nil {
			return err
		}
		pool, err := address.NewPool(network.IPv4Pool)
		if err != nil {
			return err
		}
		membership := control.Membership{
			NetworkID: network.ID,
			NodeID:    node.ID,
			Role:      control.RoleMember,
			Status:    control.MembershipActive,
		}
		allocated := false
		var allocationErr error
		occupied := make([]net.IP, 0)
		for attempts := uint64(0); attempts < pool.Size(); attempts++ {
			candidateIP, err := pool.RandomNext(nil, occupied)
			if err != nil {
				allocationErr = err
				break
			}
			membership.VirtualIPv4 = candidateIP
			addErr := transaction.AddMembership(transactionContext, membership)
			if addErr == nil {
				allocated = true
				break
			}
			if errors.Is(addErr, db.ErrConflict) {
				occupied = append(occupied, candidateIP)
				continue
			}
			allocationErr = addErr
			break
		}
		if allocationErr != nil && !errors.Is(allocationErr, address.ErrPoolExhausted) {
			return allocationErr
		}
		if !allocated {
			return &address.PoolExhaustedError{CIDR: pool.CIDR()}
		}
		currentNetwork, err := transaction.GetNetwork(transactionContext, network.ID)
		if err != nil {
			return err
		}
		result = Result{
			Node:             node,
			Membership:       membership,
			Network:          currentNetwork,
			Subject:          node.ID,
			NetworkID:        network.ID,
			SessionSubject:   node.ID,
			SessionNetworkID: network.ID,
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// Register is a compatibility alias for callers that use registration
// terminology.
func (service *Service) Register(ctx context.Context, request Request) (Result, error) {
	return service.Enroll(ctx, request)
}

func randomID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("node-%d", time.Now().UTC().UnixNano())
	}
	return "node-" + hex.EncodeToString(value)
}
