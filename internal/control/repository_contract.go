package control

import (
	"context"
	"errors"
	"time"
)

var (
	// Persistence errors are defined at the model boundary so HTTP adapters in
	// this package do not need to import their database implementation.
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("conflict")
	ErrInviteExpired       = errors.New("invite expired")
	ErrInviteConsumed      = errors.New("invite already consumed")
	ErrInviteRevoked       = errors.New("invite revoked")
	ErrDatabaseUnavailable = errors.New("database unavailable")
	ErrInvalidContext      = errors.New("invalid context")
)

// Repository is the context-aware control-plane persistence contract. The
// concrete database package aliases this definition for compatibility.
type Repository interface {
	CreateNetwork(context.Context, Network) error
	GetNetwork(context.Context, string) (Network, error)
	AddNode(context.Context, Node) error
	GetNode(context.Context, string) (Node, error)
	GetNodeNetworkIDs(context.Context, string) ([]string, error)
	TouchNode(context.Context, string, time.Time) error
	RemoveNode(context.Context, string) error
	AddMembership(context.Context, Membership) error
	RemoveMembership(context.Context, string, string) error
	CreateInvite(context.Context, Invite) error
	ConsumeInvite(context.Context, string, string, time.Time) (Invite, error)
	ConsumeInviteForNode(context.Context, string, string, time.Time, string) (Invite, error)
	ReplaceEndpoints(context.Context, string, []EndpointCandidate) error
	SetRelayAssignment(context.Context, RelayAssignment) error
	RemoveRelayAssignment(context.Context, string) error
	BuildSnapshot(context.Context, string, string) (NetworkSnapshot, error)
}

// TransactionalRepository executes a mutation callback against one atomic
// repository transaction.
type TransactionalRepository interface {
	Repository
	WithTransaction(context.Context, func(context.Context, Repository) error) error
}
