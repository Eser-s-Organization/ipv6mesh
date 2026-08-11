// Package service contains the privileged local service's platform-neutral
// state machine. WireGuardNT and Windows route operations are injected later.
package service

import (
	"context"
	"errors"
	"strings"
	"sync"

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

type Adapter interface {
	Connect(context.Context, string) error
	Disconnect(context.Context, string) error
}

type Authorizer interface {
	Authorize(context.Context) error
}

type Options struct {
	Identity IdentityStore
	Control  ControlClient
	Adapter  Adapter
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
	service.operationMu.Lock()
	defer service.operationMu.Unlock()
	service.mu.RLock()
	started := service.started
	service.mu.RUnlock()
	if !started {
		return ipc.ErrorResponse(ipc.CodeNotStarted)
	}
	switch request.Type {
	case ipc.CommandStatus:
		return ipc.SuccessResponse(service.statusSnapshot())
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
func (service *Service) statusSnapshot() ipc.Status {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.status
}

func (service *Service) join(ctx context.Context, request ipc.Request) ipc.Response {
	service.mu.RLock()
	if service.joined != nil {
		service.mu.RUnlock()
		return ipc.ErrorResponse(CodeAlreadyJoined)
	}
	publicKey := service.identity.PublicKeyValue()
	control := service.options.Control
	service.mu.RUnlock()
	if strings.TrimSpace(request.Invite) == "" || strings.TrimSpace(request.DisplayName) == "" || control == nil {
		return ipc.ErrorResponse(ipc.CodeInvalidRequest)
	}
	result, err := control.Join(ctx, JoinRequest{Invite: request.Invite, DisplayName: request.DisplayName, PublicKey: publicKey})
	if err != nil || result.NetworkID == "" || result.VirtualIPv4 == "" {
		return ipc.ErrorResponse(CodeControlFailed)
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
	control := service.options.Control
	service.mu.RUnlock()
	if networkID != joined.NetworkID {
		return ipc.ErrorResponse(ipc.CodeWrongNetwork)
	}
	if pathState != ipc.PathStateDisconnected && adapter != nil {
		if err := adapter.Disconnect(ctx, networkID); err != nil {
			return ipc.ErrorResponse(CodeAdapterFailed)
		}
	}
	if control == nil {
		return ipc.ErrorResponse(CodeControlFailed)
	}
	if err := control.Leave(ctx, networkID); err != nil {
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
	service.mu.RUnlock()
	if networkID != joinedNetworkID {
		return ipc.ErrorResponse(ipc.CodeWrongNetwork)
	}
	if adapter == nil {
		return ipc.ErrorResponse(CodeAdapterFailed)
	}
	if err := adapter.Connect(ctx, networkID); err != nil {
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
	service.mu.RUnlock()
	if networkID != joinedNetworkID {
		return ipc.ErrorResponse(ipc.CodeWrongNetwork)
	}
	if adapter == nil {
		return ipc.ErrorResponse(CodeAdapterFailed)
	}
	if err := adapter.Disconnect(ctx, networkID); err != nil {
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
