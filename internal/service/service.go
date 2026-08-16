// Package service contains the privileged local service's platform-neutral
// state machine. WireGuardNT and Windows route operations are injected later.
package service

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/identity"
	"github.com/Eser-s-Organization/ipv6mesh/internal/ipc"
)

var (
	ErrInvalidOptions = errors.New("service dependencies are incomplete")
)

type IdentityStore interface {
	LoadOrCreate() (identity.Identity, error)
}

type JoinRequest struct {
	Invite      string
	DisplayName string
	PublicKey   string
}

type JoinResult struct {
	NetworkID        string
	VirtualIPv4      string
	ConfigGeneration int64
}

type ControlClient interface {
	Join(context.Context, JoinRequest) (JoinResult, error)
	Leave(context.Context, string) error
}

type SnapshotClient interface {
	Snapshot(context.Context, string) (control.NetworkSnapshot, error)
}

type SnapshotApplier interface {
	Apply(context.Context, control.NetworkSnapshot) error
	Clear(context.Context) error
}

type EndpointSource interface {
	Discover(context.Context, uint16) ([]control.EndpointCandidate, error)
}

type EndpointReporter interface {
	Heartbeat(context.Context, []control.EndpointCandidate) error
}

type Adapter interface {
	Connect(context.Context, string) error
	Disconnect(context.Context, string) error
}

type Authorizer interface {
	Authorize(context.Context) error
}

type Options struct {
	Identity   IdentityStore
	Control    ControlClient
	Adapter    Adapter
	Reconciler SnapshotApplier
}

type Service struct {
	mu          sync.RWMutex
	operationMu sync.Mutex
	identity    identity.Identity
	options     Options
	started     bool
	joined      *JoinResult
	status      ipc.Status
}

func New(options Options) *Service {
	return &Service{options: options}
}

func (service *Service) Start(ctx context.Context) error {
	if service == nil || service.options.Identity == nil {
		return ErrInvalidOptions
	}
	service.operationMu.Lock()
	defer service.operationMu.Unlock()
	service.mu.RLock()
	started := service.started
	service.mu.RUnlock()
	if started {
		return nil
	}
	loaded, err := service.options.Identity.LoadOrCreate()
	if err != nil {
		return err
	}
	if strings.TrimSpace(loaded.PublicKeyValue()) == "" {
		return identity.ErrInvalidIdentity
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.started {
		return nil
	}
	service.identity = loaded
	service.started = true
	service.status = ipc.Status{PathState: ipc.PathStateDisconnected}
	return nil
}

// Shutdown releases local data-plane resources owned by the service without
// removing the node's control-plane membership. It is used when the Windows
// service receives Stop/Shutdown so WireGuard state, overlay addresses, and
// routes do not survive the process that created them.
func (service *Service) Shutdown(ctx context.Context) error {
	if service == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	service.operationMu.Lock()
	defer service.operationMu.Unlock()

	service.mu.RLock()
	started := service.started
	joined := service.joined
	pathState := service.status.PathState
	adapter := service.options.Adapter
	reconciler := service.options.Reconciler
	service.mu.RUnlock()
	if !started {
		return nil
	}

	var cleanupErr error
	if reconciler != nil {
		// A snapshot is applied during Join, before the status changes to
		// connected, so Clear must run even when pathState is disconnected.
		cleanupErr = reconciler.Clear(ctx)
	} else if joined != nil && pathState != ipc.PathStateDisconnected && adapter != nil {
		cleanupErr = adapter.Disconnect(ctx, joined.NetworkID)
	}

	service.mu.Lock()
	service.started = false
	service.joined = nil
	service.status = ipc.Status{PathState: ipc.PathStateDisconnected}
	service.mu.Unlock()
	return cleanupErr
}

func (service *Service) PublicKey() string {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.identity.PublicKeyValue()
}

func (service *Service) Handle(ctx context.Context, request ipc.Request) ipc.Response {
	if service == nil {
		return ipc.ErrorResponse(ipc.CodeInternal)
	}
	if err := contextErr(ctx); err != nil {
		return ipc.ErrorResponse(ipc.CodeInternal)
	}
	if request.Type == ipc.CommandStatus {
		service.mu.RLock()
		defer service.mu.RUnlock()
		if !service.started {
			return ipc.ErrorResponse(ipc.CodeNotStarted)
		}
		return ipc.SuccessResponse(service.status)
	}
	service.operationMu.Lock()
	defer service.operationMu.Unlock()
	service.mu.RLock()
	started := service.started
	service.mu.RUnlock()
	if !started {
		return ipc.ErrorResponse(ipc.CodeNotStarted)
	}
	switch request.Type {
	case ipc.CommandJoin:
		return service.join(ctx, request)
	case ipc.CommandLeave:
		return service.leave(ctx, request.NetworkID)
	case ipc.CommandConnect:
		return service.connect(ctx, request.NetworkID)
	case ipc.CommandDisconnect:
		return service.disconnect(ctx, request.NetworkID)
	default:
		return ipc.ErrorResponse(ipc.CodeInvalidRequest)
	}
}
func (service *Service) join(ctx context.Context, request ipc.Request) ipc.Response {
	service.mu.RLock()
	if service.joined != nil {
		service.mu.RUnlock()
		return ipc.ErrorResponse(CodeAlreadyJoined)
	}
	publicKey := service.identity.PublicKeyValue()
	controlClient := service.options.Control
	reconciler := service.options.Reconciler
	service.mu.RUnlock()
	if strings.TrimSpace(request.Invite) == "" || strings.TrimSpace(request.DisplayName) == "" || controlClient == nil {
		return ipc.ErrorResponse(ipc.CodeInvalidRequest)
	}
	result, err := controlClient.Join(ctx, JoinRequest{Invite: request.Invite, DisplayName: request.DisplayName, PublicKey: publicKey})
	if err != nil || result.NetworkID == "" || result.VirtualIPv4 == "" {
		return ipc.ErrorResponse(CodeControlFailed)
	}
	if reconciler != nil {
		snapshotClient, ok := controlClient.(SnapshotClient)
		if !ok {
			return ipc.ErrorResponse(CodeControlFailed)
		}
		snapshot, snapshotErr := snapshotClient.Snapshot(ctx, result.NetworkID)
		if snapshotErr != nil || reconciler.Apply(ctx, snapshot) != nil {
			_ = controlClient.Leave(ctx, result.NetworkID)
			return ipc.ErrorResponse(CodeAdapterFailed)
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.joined != nil {
		return ipc.ErrorResponse(CodeAlreadyJoined)
	}
	service.joined = &result
	service.status = ipc.Status{NetworkID: result.NetworkID, VirtualIPv4: result.VirtualIPv4, PathState: ipc.PathStateDisconnected, ConfigGeneration: result.ConfigGeneration}
	return ipc.SuccessResponse(service.status)
}

func (service *Service) leave(ctx context.Context, networkID string) ipc.Response {
	service.mu.RLock()
	if service.joined == nil {
		service.mu.RUnlock()
		return ipc.ErrorResponse(CodeNotJoined)
	}
	joined := *service.joined
	pathState := service.status.PathState
	adapter := service.options.Adapter
	reconciler := service.options.Reconciler
	controlClient := service.options.Control
	service.mu.RUnlock()
	if networkID != joined.NetworkID {
		return ipc.ErrorResponse(ipc.CodeWrongNetwork)
	}
	if pathState != ipc.PathStateDisconnected && adapter != nil {
		if err := adapter.Disconnect(ctx, networkID); err != nil {
			return ipc.ErrorResponse(CodeAdapterFailed)
		}
	}
	if reconciler != nil {
		if err := reconciler.Clear(ctx); err != nil {
			return ipc.ErrorResponse(CodeAdapterFailed)
		}
	}
	if controlClient == nil {
		return ipc.ErrorResponse(CodeControlFailed)
	}
	if err := controlClient.Leave(ctx, networkID); err != nil {
		return ipc.ErrorResponse(CodeControlFailed)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.joined = nil
	service.status = ipc.Status{PathState: ipc.PathStateDisconnected}
	return ipc.SuccessResponse(service.status)
}

func (service *Service) connect(ctx context.Context, networkID string) ipc.Response {
	service.mu.RLock()
	if service.joined == nil {
		service.mu.RUnlock()
		return ipc.ErrorResponse(CodeNotJoined)
	}
	joinedNetworkID := service.joined.NetworkID
	adapter := service.options.Adapter
	reconciler := service.options.Reconciler
	controlClient := service.options.Control
	service.mu.RUnlock()
	if networkID != joinedNetworkID {
		return ipc.ErrorResponse(ipc.CodeWrongNetwork)
	}
	if reconciler != nil {
		snapshotClient, ok := controlClient.(SnapshotClient)
		if !ok {
			return ipc.ErrorResponse(CodeControlFailed)
		}
		snapshot, err := snapshotClient.Snapshot(ctx, networkID)
		if err != nil || reconciler.Apply(ctx, snapshot) != nil {
			return ipc.ErrorResponse(CodeAdapterFailed)
		}
	} else if adapter == nil {
		return ipc.ErrorResponse(CodeAdapterFailed)
	} else if err := adapter.Connect(ctx, networkID); err != nil {
		return ipc.ErrorResponse(CodeAdapterFailed)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.status.PathState = ipc.PathStateDirect
	return ipc.SuccessResponse(service.status)
}

func (service *Service) disconnect(ctx context.Context, networkID string) ipc.Response {
	service.mu.RLock()
	if service.joined == nil {
		service.mu.RUnlock()
		return ipc.ErrorResponse(CodeNotJoined)
	}
	joinedNetworkID := service.joined.NetworkID
	adapter := service.options.Adapter
	reconciler := service.options.Reconciler
	service.mu.RUnlock()
	if networkID != joinedNetworkID {
		return ipc.ErrorResponse(ipc.CodeWrongNetwork)
	}
	if reconciler != nil {
		if err := reconciler.Clear(ctx); err != nil {
			return ipc.ErrorResponse(CodeAdapterFailed)
		}
	} else if adapter == nil {
		return ipc.ErrorResponse(CodeAdapterFailed)
	} else if err := adapter.Disconnect(ctx, networkID); err != nil {
		return ipc.ErrorResponse(CodeAdapterFailed)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.status.PathState = ipc.PathStateDisconnected
	return ipc.SuccessResponse(service.status)
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

const (
	CodeAlreadyJoined = "already_joined"
	CodeNotJoined     = "not_joined"
	CodeControlFailed = "control_failed"
	CodeAdapterFailed = "adapter_failed"
)

// Handler applies authorization and translates strict JSON into service
// responses. It never exposes the private key or the original invite.
type Handler struct {
	service    *Service
	authorizer Authorizer
}

func NewHandler(service *Service, authorizer Authorizer) *Handler {
	return &Handler{service: service, authorizer: authorizer}
}

func (handler *Handler) HandleJSON(ctx context.Context, data []byte) ([]byte, error) {
	request, err := ipc.DecodeRequest(data)
	if err != nil {
		return ipc.MarshalResponse(ipc.ErrorResponse(ipc.CodeInvalidRequest))
	}
	if handler == nil || handler.service == nil || handler.authorizer == nil {
		return ipc.MarshalResponse(ipc.ErrorResponse(ipc.CodeUnauthorized))
	}
	if err := handler.authorizer.Authorize(ctx); err != nil {
		return ipc.MarshalResponse(ipc.ErrorResponse(ipc.CodeUnauthorized))
	}
	return ipc.MarshalResponse(handler.service.Handle(ctx, request))
}
