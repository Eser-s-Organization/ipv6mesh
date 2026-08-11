package control

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/address"
	"github.com/Eser-s-Organization/ipv6mesh/internal/auth"
	"github.com/gorilla/websocket"
)

const (
	defaultSessionTTL                 = time.Hour
	defaultInviteTTL                  = 24 * time.Hour
	defaultBodyLimit                  = 1 << 20
	defaultEnrollmentRecoveryAttempts = 3
	defaultEnrollmentRecoveryTimeout  = 2 * time.Second
	defaultEnrollmentRecoveryDelay    = 10 * time.Millisecond
)

var (
	errInvalidJSON               = errors.New("invalid JSON request")
	ErrInvalidInviteToken        = errors.New("invalid invite token")
	ErrInvalidRequest            = errors.New("invalid enrollment request")
	ErrCommitUnknown             = errors.New("enrollment commit status is uncertain")
	ErrEnrollmentRecoveryPending = ErrCommitUnknown
)

// HandlerOptions configures the standard-library HTTP control-plane handler.
// A supplied SessionStore and dependency functions make HTTP tests fully
// deterministic without weakening production defaults.
type HandlerOptions struct {
	BootstrapToken             string
	BootstrapSubject           string
	SessionTTL                 time.Duration
	InviteTTL                  time.Duration
	SessionStore               *auth.SessionStore
	Clock                      func() time.Time
	NewID                      func() string
	IDGenerator                func() string
	TokenRandom                io.Reader
	MaxBodyBytes               int64
	CheckOrigin                func(*http.Request) bool
	EnrollmentRecoveryAttempts int
	EnrollmentRecoveryTimeout  time.Duration
	EnrollmentRecoveryDelay    time.Duration
}

// Handler serves only control-plane resources. It does not carry VPN data or
// private key material.
type Handler struct {
	repository                 TransactionalRepository
	sessions                   *auth.SessionStore
	bootstrapToken             string
	bootstrapSubject           string
	inviteTTL                  time.Duration
	clock                      func() time.Time
	newID                      func() string
	tokenRandom                io.Reader
	maxBodyBytes               int64
	checkOrigin                func(*http.Request) bool
	enrollmentRecoveryAttempts int
	enrollmentRecoveryTimeout  time.Duration
	enrollmentRecoveryDelay    time.Duration
}

// NewHandler constructs a control-plane HTTP handler around an existing
// transactional repository. It does not open a database connection.
func NewHandler(repository TransactionalRepository, options HandlerOptions) *Handler {
	clock := options.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	sessionTTL := options.SessionTTL
	if sessionTTL <= 0 {
		sessionTTL = defaultSessionTTL
	}
	sessions := options.SessionStore
	if sessions == nil {
		sessions = auth.NewSessionStoreWithOptions(auth.SessionStoreOptions{TTL: sessionTTL, Now: clock})
	}
	newID := options.NewID
	if newID == nil {
		newID = options.IDGenerator
	}
	if newID == nil {
		newID = randomIdentifier
	}
	inviteTTL := options.InviteTTL
	if inviteTTL <= 0 {
		inviteTTL = defaultInviteTTL
	}
	tokenRandom := options.TokenRandom
	if tokenRandom == nil {
		tokenRandom = rand.Reader
	}
	bodyLimit := options.MaxBodyBytes
	if bodyLimit <= 0 {
		bodyLimit = defaultBodyLimit
	}
	checkOrigin := options.CheckOrigin
	if checkOrigin == nil {
		checkOrigin = sameOrigin
	}
	recoveryAttempts := options.EnrollmentRecoveryAttempts
	if recoveryAttempts <= 0 {
		recoveryAttempts = defaultEnrollmentRecoveryAttempts
	}
	recoveryTimeout := options.EnrollmentRecoveryTimeout
	if recoveryTimeout <= 0 {
		recoveryTimeout = defaultEnrollmentRecoveryTimeout
	}
	recoveryDelay := options.EnrollmentRecoveryDelay
	if recoveryDelay <= 0 {
		recoveryDelay = defaultEnrollmentRecoveryDelay
	}
	bootstrapSubject := options.BootstrapSubject
	if bootstrapSubject == "" {
		bootstrapSubject = "bootstrap-admin"
	}
	return &Handler{
		repository:                 repository,
		sessions:                   sessions,
		bootstrapToken:             options.BootstrapToken,
		bootstrapSubject:           bootstrapSubject,
		inviteTTL:                  inviteTTL,
		clock:                      clock,
		newID:                      newID,
		tokenRandom:                tokenRandom,
		maxBodyBytes:               bodyLimit,
		checkOrigin:                checkOrigin,
		enrollmentRecoveryAttempts: recoveryAttempts,
		enrollmentRecoveryTimeout:  recoveryTimeout,
		enrollmentRecoveryDelay:    recoveryDelay,
	}
}

// NewServer is a compatibility alias for callers that prefer server naming.
func NewServer(repository TransactionalRepository, options HandlerOptions) *Handler {
	return NewHandler(repository, options)
}

type principal struct {
	session   auth.Session
	bootstrap bool
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Request-ID", requestID(request))
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/v1/networks":
		handler.createNetwork(writer, request)
	case request.Method == http.MethodPost && hasPathSuffix(request.URL.Path, "/invites"):
		handler.createInvite(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/v1/enrollments":
		handler.enroll(writer, request)
	case request.Method == http.MethodGet && hasPathSuffix(request.URL.Path, "/snapshot"):
		handler.snapshot(writer, request)
	case request.Method == http.MethodPost && hasPathSuffix(request.URL.Path, "/heartbeat"):
		handler.heartbeat(writer, request)
	case request.Method == http.MethodPost && hasPathSuffix(request.URL.Path, "/leave"):
		handler.leaveNode(writer, request)
	case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/v1/nodes/"):
		handler.deleteNode(writer, request)
	case request.URL.Path == "/v1/events":
		handler.events(writer, request)
	default:
		writeAPIError(writer, http.StatusNotFound, ErrNotFound)
	}
}

func hasPathSuffix(path, suffix string) bool {
	if !strings.HasPrefix(path, "/v1/") || strings.HasSuffix(path, "/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return len(parts) == 4 && parts[0] == "v1" && parts[1] != "" && parts[3] == strings.TrimPrefix(suffix, "/")
}

func resourceID(path, collection, suffix string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != collection || parts[2] == "" || parts[3] != suffix {
		return "", false
	}
	id, err := url.PathUnescape(parts[2])
	return id, err == nil && id != ""
}

func (handler *Handler) authenticate(request *http.Request) (principal, error) {
	token, err := auth.ParseBearerHeader(request.Header.Get("Authorization"))
	if err != nil {
		return principal{}, err
	}
	if auth.ValidateBootstrapToken(handler.bootstrapToken, token) == nil {
		return principal{session: auth.Session{Subject: handler.bootstrapSubject, Role: auth.RoleAdmin}, bootstrap: true}, nil
	}
	session, err := handler.sessions.Authenticate(token)
	if err != nil {
		return principal{}, err
	}
	return principal{session: session}, nil
}

func (handler *Handler) requireAdmin(request *http.Request) (principal, error) {
	p, err := handler.authenticate(request)
	if err != nil {
		return principal{}, err
	}
	if p.session.Role != auth.RoleAdmin {
		return principal{}, auth.ErrInsufficientPermission
	}
	return p, nil
}

func (handler *Handler) authorizeNetwork(principal principal, networkID string) error {
	return handler.sessions.AuthorizeNetwork(principal.session, networkID)
}

// authorizeAdminNodeScope proves that a scoped administrator's target node is
// a member only of the administrator's network. If the repository cannot
// provide that proof, the operation is denied rather than guessed.
func (handler *Handler) authorizeAdminNodeScope(ctx context.Context, principal principal, nodeID string) error {
	if principal.session.Role != auth.RoleAdmin {
		return auth.ErrInsufficientPermission
	}
	if strings.TrimSpace(principal.session.NetworkID) == "" {
		return nil
	}
	networkIDs, err := handler.repository.GetNodeNetworkIDs(ctx, nodeID)
	if err != nil {
		return err
	}
	if len(networkIDs) == 0 {
		return auth.ErrInsufficientPermission
	}
	for _, networkID := range networkIDs {
		if networkID != principal.session.NetworkID {
			return auth.ErrWrongNetwork
		}
	}
	return handler.authorizeNetwork(principal, principal.session.NetworkID)
}

func (handler *Handler) createNetwork(writer http.ResponseWriter, request *http.Request) {
	principal, err := handler.requireAdmin(request)
	if err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	if strings.TrimSpace(principal.session.NetworkID) != "" {
		writeAPIError(writer, http.StatusForbidden, auth.ErrInsufficientPermission)
		return
	}
	var body struct {
		Name string `json:"name"`
		Pool string `json:"pool"`
	}
	if err := decodeJSON(writer, request, handler.maxBodyBytes, &body); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeAPIError(writer, http.StatusUnprocessableEntity, ErrValidation)
		return
	}
	if _, err := address.NewPool(body.Pool); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	network := Network{
		ID:            handler.newID(),
		Name:          body.Name,
		IPv4Pool:      body.Pool,
		OwnerID:       principal.session.Subject,
		ConfigVersion: 1,
		CreatedAt:     handler.clock().UTC(),
	}
	if err := handler.repository.CreateNetwork(request.Context(), network); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	writeJSON(writer, http.StatusCreated, makeNetworkResponse(network))
}

func (handler *Handler) createInvite(writer http.ResponseWriter, request *http.Request) {
	principal, err := handler.requireAdmin(request)
	if err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	networkID, ok := resourceID(request.URL.Path, "networks", "invites")
	if !ok {
		writeAPIError(writer, http.StatusNotFound, ErrNotFound)
		return
	}
	if err := handler.authorizeNetwork(principal, networkID); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	network, err := handler.repository.GetNetwork(request.Context(), networkID)
	if err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	var body struct {
		ExpiresIn string `json:"expires_in"`
	}
	if err := decodeJSON(writer, request, handler.maxBodyBytes, &body); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	duration, err := time.ParseDuration(body.ExpiresIn)
	if err != nil || duration <= 0 {
		writeAPIError(writer, http.StatusUnprocessableEntity, ErrValidation)
		return
	}
	inviteID := handler.newID()
	secret := make([]byte, 32)
	if _, err := io.ReadFull(handler.tokenRandom, secret); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	rawToken := inviteID + "." + base64.RawURLEncoding.EncodeToString(secret)
	now := handler.clock().UTC()
	invite := Invite{
		ID:        inviteID,
		NetworkID: network.ID,
		TokenHash: auth.HashToken(rawToken),
		ExpiresAt: now.Add(duration),
		CreatedAt: now,
	}
	if err := handler.repository.CreateInvite(request.Context(), invite); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	writeJSON(writer, http.StatusCreated, inviteResponse{InviteID: invite.ID, NetworkID: invite.NetworkID, Token: rawToken, ExpiresAt: invite.ExpiresAt})
}

func (handler *Handler) enroll(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	var body struct {
		Invite        string `json:"invite"`
		NodeID        string `json:"node_id"`
		DisplayName   string `json:"display_name"`
		PublicKey     string `json:"public_key"`
		Platform      string `json:"platform"`
		ClientVersion string `json:"client_version"`
	}
	if err := decodeJSON(writer, request, handler.maxBodyBytes, &body); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	result, err := handler.enrollControl(request.Context(), enrollmentRequest{
		InviteToken:   body.Invite,
		NodeID:        body.NodeID,
		DisplayName:   body.DisplayName,
		PublicKey:     body.PublicKey,
		Platform:      body.Platform,
		ClientVersion: body.ClientVersion,
	})
	if err != nil {
		if errors.Is(err, ErrEnrollmentRecoveryPending) && result.SessionToken != "" {
			writer.Header().Set("Retry-After", "1")
			writeJSON(writer, http.StatusServiceUnavailable, enrollmentRecoveryResponse{
				Error:        "enrollment_recovery_pending",
				Retryable:    true,
				Node:         makeNodeResponse(result.Node),
				Membership:   makeMembershipResponse(result.Membership),
				Network:      makeNetworkResponse(result.Network),
				Session:      sessionResponse{Token: result.SessionToken, Subject: result.Session.Subject, NetworkID: result.Session.NetworkID, ExpiresAt: result.Session.ExpiresAt},
				SessionToken: result.SessionToken,
			})
			return
		}
		writeAPIError(writer, statusForError(err), err)
		return
	}
	writeJSON(writer, http.StatusCreated, enrollmentResponse{
		Node:         makeNodeResponse(result.Node),
		Membership:   makeMembershipResponse(result.Membership),
		Network:      makeNetworkResponse(result.Network),
		Session:      sessionResponse{Token: result.SessionToken, Subject: result.Session.Subject, NetworkID: result.Session.NetworkID, ExpiresAt: result.Session.ExpiresAt},
		SessionToken: result.SessionToken,
	})
}

type enrollmentRequest struct {
	InviteToken   string
	NodeID        string
	DisplayName   string
	PublicKey     string
	Platform      string
	ClientVersion string
}

type enrollmentResult struct {
	Node         Node
	Membership   Membership
	Network      Network
	Subject      string
	NetworkID    string
	Session      auth.Session
	SessionToken string
}

func (handler *Handler) enrollControl(ctx context.Context, request enrollmentRequest) (enrollmentResult, error) {
	if strings.TrimSpace(request.InviteToken) == "" ||
		strings.TrimSpace(request.DisplayName) == "" ||
		strings.TrimSpace(request.PublicKey) == "" ||
		strings.TrimSpace(request.Platform) == "" ||
		strings.TrimSpace(request.ClientVersion) == "" {
		return enrollmentResult{}, errors.Join(ErrInvalidRequest, ErrValidation)
	}
	separator := strings.IndexByte(request.InviteToken, '.')
	if separator <= 0 || separator == len(request.InviteToken)-1 ||
		strings.IndexByte(request.InviteToken[separator+1:], '.') >= 0 ||
		strings.ContainsAny(request.InviteToken, " \t\r\n") {
		return enrollmentResult{}, errors.Join(ErrInvalidInviteToken, ErrValidation)
	}
	inviteID := request.InviteToken[:separator]
	tokenHash := auth.HashToken(request.InviteToken)
	nodeID := strings.TrimSpace(request.NodeID)
	if nodeID == "" {
		nodeID = strings.TrimSpace(handler.newID())
	}
	if nodeID == "" {
		return enrollmentResult{}, errors.Join(ErrInvalidRequest, ErrValidation)
	}
	now := handler.clock().UTC()
	var result enrollmentResult
	var issuedToken string
	err := handler.repository.WithTransaction(ctx, func(transactionContext context.Context, transaction Repository) error {
		node := Node{ID: nodeID, DisplayName: request.DisplayName, PublicKey: request.PublicKey, Platform: request.Platform, ClientVersion: request.ClientVersion, LastSeen: now}
		if err := transaction.AddNode(transactionContext, node); err != nil {
			return err
		}
		invite, err := transaction.ConsumeInviteForNode(transactionContext, inviteID, tokenHash, now, node.ID)
		if err != nil {
			return err
		}
		network, err := transaction.GetNetwork(transactionContext, invite.NetworkID)
		if err != nil {
			return err
		}
		if handler.sessions.IsNetworkRevoked(network.ID) {
			return auth.ErrRevokedNetwork
		}
		pool, err := address.NewPool(network.IPv4Pool)
		if err != nil {
			return err
		}
		membership := Membership{NetworkID: network.ID, NodeID: node.ID, Role: RoleMember, Status: MembershipActive}
		allocated := false
		allocationErr := pool.ForEachCandidate(func(candidate net.IP) error {
			membership.VirtualIPv4 = candidate
			if err := transaction.AddMembership(transactionContext, membership); err == nil {
				allocated = true
				return stopHTTPAllocation
			} else if errors.Is(err, ErrConflict) {
				return nil
			} else {
				return err
			}
		})
		if !errors.Is(allocationErr, stopHTTPAllocation) && allocationErr != nil {
			return allocationErr
		}
		if !allocated {
			return &address.PoolExhaustedError{CIDR: pool.CIDR()}
		}
		currentNetwork, err := transaction.GetNetwork(transactionContext, network.ID)
		if err != nil {
			return err
		}
		result = enrollmentResult{Node: node, Membership: membership, Network: currentNetwork, Subject: node.ID, NetworkID: network.ID}
		// Session issuance is the final callback operation, after every
		// persistence write has completed.
		token, session, err := handler.sessions.IssueNodeSession(result.Subject, result.NetworkID)
		if err != nil {
			return err
		}
		issuedToken = token
		result.Session = session
		result.SessionToken = token
		return nil
	})
	if err != nil {
		if issuedToken == "" {
			return enrollmentResult{}, err
		}
		result.SessionToken = issuedToken
		return handler.recoverEnrollmentCommit(result, err)
	}
	return result, nil
}

// recoverEnrollmentCommit handles the ambiguous boundary where a repository
// may have committed successfully and still returned an error. Fresh bounded
// reads prevent cancellation of the original request from orphaning a live
// enrollment session.
func (handler *Handler) recoverEnrollmentCommit(result enrollmentResult, transactionErr error) (enrollmentResult, error) {
	recoveryContext, cancel := context.WithTimeout(context.Background(), handler.enrollmentRecoveryTimeout)
	defer cancel()
	uncertain := false
	for attempt := 0; attempt < handler.enrollmentRecoveryAttempts; attempt++ {
		if attempt > 0 && handler.enrollmentRecoveryDelay > 0 {
			timer := time.NewTimer(handler.enrollmentRecoveryDelay)
			select {
			case <-timer.C:
			case <-recoveryContext.Done():
				return result, errors.Join(ErrEnrollmentRecoveryPending, transactionErr)
			}
		}
		node, nodeErr := handler.repository.GetNode(recoveryContext, result.Node.ID)
		networkIDs, membershipErr := handler.repository.GetNodeNetworkIDs(recoveryContext, result.Node.ID)
		if nodeErr == nil && membershipErr == nil && len(networkIDs) == 1 && networkIDs[0] == result.NetworkID {
			result.Node = node
			return result, nil
		}
		if !(errors.Is(nodeErr, ErrNotFound) && errors.Is(membershipErr, ErrNotFound)) {
			uncertain = true
		}
	}
	if uncertain {
		return result, errors.Join(ErrEnrollmentRecoveryPending, transactionErr)
	}
	handler.sessions.RevokeSession(result.SessionToken)
	return enrollmentResult{}, transactionErr
}

var stopHTTPAllocation = errors.New("allocation completed")

func (handler *Handler) snapshot(writer http.ResponseWriter, request *http.Request) {
	principal, err := handler.authenticate(request)
	if err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	if principal.session.Role != auth.RoleNode {
		writeAPIError(writer, http.StatusForbidden, auth.ErrInsufficientPermission)
		return
	}
	networkID, ok := resourceID(request.URL.Path, "networks", "snapshot")
	if !ok {
		writeAPIError(writer, http.StatusNotFound, ErrNotFound)
		return
	}
	if err := handler.authorizeNetwork(principal, networkID); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	snapshot, err := handler.repository.BuildSnapshot(request.Context(), networkID, principal.session.Subject)
	if err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshotResponse(snapshot))
}

type heartbeatRequest struct {
	Endpoints     []heartbeatEndpoint `json:"endpoints"`
	ClientVersion *string             `json:"client_version"`
}

type heartbeatEndpoint struct {
	Address    string `json:"address"`
	Port       uint16 `json:"port"`
	Family     string `json:"family"`
	Interface  string `json:"interface"`
	Priority   int    `json:"priority"`
	ObservedAt string `json:"observed_at"`
}

func (handler *Handler) heartbeat(writer http.ResponseWriter, request *http.Request) {
	principal, err := handler.authenticate(request)
	if err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	nodeID, ok := resourceID(request.URL.Path, "nodes", "heartbeat")
	if !ok {
		writeAPIError(writer, http.StatusNotFound, ErrNotFound)
		return
	}
	if principal.session.Role == auth.RoleNode {
		if nodeID != principal.session.Subject {
			writeAPIError(writer, http.StatusForbidden, auth.ErrInsufficientPermission)
			return
		}
		if err := handler.authorizeNetwork(principal, principal.session.NetworkID); err != nil {
			writeAPIError(writer, statusForError(err), err)
			return
		}
	} else if principal.session.Role != auth.RoleAdmin {
		writeAPIError(writer, http.StatusForbidden, auth.ErrInsufficientPermission)
		return
	} else if err := handler.authorizeAdminNodeScope(request.Context(), principal, nodeID); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	if _, err := handler.repository.GetNode(request.Context(), nodeID); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	var body heartbeatRequest
	if err := decodeJSON(writer, request, handler.maxBodyBytes, &body); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	endpoints := make([]EndpointCandidate, len(body.Endpoints))
	for index, endpoint := range body.Endpoints {
		parsed, err := parseHeartbeatEndpoint(nodeID, endpoint)
		if err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		endpoints[index] = parsed
	}
	if err := handler.repository.ReplaceEndpoints(request.Context(), nodeID, endpoints); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	if body.ClientVersion != nil {
		if updater, ok := handler.repository.(interface {
			UpdateNodeClientVersion(context.Context, string, string) error
		}); ok {
			if err := updater.UpdateNodeClientVersion(request.Context(), nodeID, *body.ClientVersion); err != nil {
				writeAPIError(writer, statusForError(err), err)
				return
			}
		}
	}
	if err := handler.repository.TouchNode(request.Context(), nodeID, handler.clock().UTC()); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func parseHeartbeatEndpoint(nodeID string, endpoint heartbeatEndpoint) (EndpointCandidate, error) {
	address := net.ParseIP(endpoint.Address)
	if address == nil {
		return EndpointCandidate{}, ErrValidation
	}
	observedAt, err := time.Parse(time.RFC3339, endpoint.ObservedAt)
	if err != nil {
		return EndpointCandidate{}, ErrValidation
	}
	candidate := EndpointCandidate{
		NodeID:     nodeID,
		Address:    address,
		Port:       endpoint.Port,
		Family:     endpoint.Family,
		Interface:  endpoint.Interface,
		Priority:   endpoint.Priority,
		ObservedAt: observedAt,
	}
	if err := ValidateEndpointCandidate(candidate); err != nil {
		return EndpointCandidate{}, err
	}
	return candidate, nil
}

func (handler *Handler) deleteNode(writer http.ResponseWriter, request *http.Request) {
	principal, err := handler.requireAdmin(request)
	if err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "v1" || parts[1] != "nodes" || parts[2] == "" {
		writeAPIError(writer, http.StatusNotFound, ErrNotFound)
		return
	}
	nodeID, err := url.PathUnescape(parts[2])
	if err != nil || nodeID == "" {
		writeAPIError(writer, http.StatusNotFound, ErrNotFound)
		return
	}
	if err := handler.authorizeAdminNodeScope(request.Context(), principal, nodeID); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	if err := handler.repository.RemoveNode(request.Context(), nodeID); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) leaveNode(writer http.ResponseWriter, request *http.Request) {
	principal, err := handler.authenticate(request)
	if err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	nodeID, ok := resourceID(request.URL.Path, "nodes", "leave")
	if !ok {
		writeAPIError(writer, http.StatusNotFound, ErrNotFound)
		return
	}
	switch principal.session.Role {
	case auth.RoleNode:
		if nodeID != principal.session.Subject {
			writeAPIError(writer, http.StatusForbidden, auth.ErrInsufficientPermission)
			return
		}
		if err := handler.authorizeNetwork(principal, principal.session.NetworkID); err != nil {
			writeAPIError(writer, statusForError(err), err)
			return
		}
	case auth.RoleAdmin:
		if err := handler.authorizeAdminNodeScope(request.Context(), principal, nodeID); err != nil {
			writeAPIError(writer, statusForError(err), err)
			return
		}
	default:
		writeAPIError(writer, http.StatusForbidden, auth.ErrInsufficientPermission)
		return
	}
	if _, err := handler.repository.GetNode(request.Context(), nodeID); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	if err := handler.repository.RemoveNode(request.Context(), nodeID); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) events(writer http.ResponseWriter, request *http.Request) {
	principal, err := handler.authenticate(request)
	if err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	if principal.session.Role != auth.RoleNode {
		writeAPIError(writer, http.StatusForbidden, auth.ErrInsufficientPermission)
		return
	}
	if err := handler.authorizeNetwork(principal, principal.session.NetworkID); err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	snapshot, err := handler.repository.BuildSnapshot(request.Context(), principal.session.NetworkID, principal.session.Subject)
	if err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin:     handler.checkOrigin,
	}
	connection, err := upgrader.Upgrade(writer, request, http.Header{"X-Request-ID": []string{writer.Header().Get("X-Request-ID")}})
	if err != nil {
		return
	}
	defer connection.Close()
	connection.SetReadLimit(handler.maxBodyBytes)
	if err := connection.WriteJSON(map[string]any{"type": "snapshot", "snapshot": snapshotResponse(snapshot)}); err != nil {
		return
	}
	for {
		if _, _, err := connection.ReadMessage(); err != nil {
			return
		}
	}
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, limit int64, destination any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errInvalidJSON
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errInvalidJSON
	}
	return nil
}

func requestID(request *http.Request) string {
	value := request.Header.Get("X-Request-ID")
	if validRequestID(value) {
		return value
	}
	return randomIdentifier()
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func randomIdentifier() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "request-id"
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host != "" && strings.EqualFold(parsed.Host, request.Host)
}

type inviteResponse struct {
	InviteID  string    `json:"invite_id"`
	NetworkID string    `json:"network_id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type enrollmentResponse struct {
	Node         apiNodeResponse       `json:"node"`
	Membership   apiMembershipResponse `json:"membership"`
	Network      apiNetworkResponse    `json:"network"`
	Session      sessionResponse       `json:"session"`
	SessionToken string                `json:"session_token"`
}

// enrollmentRecoveryResponse keeps the issued credential available to a
// client while the server reports that commit status remains uncertain. The
// client can retry its control-plane synchronization with this credential.
type enrollmentRecoveryResponse struct {
	Error        string                `json:"error"`
	Retryable    bool                  `json:"retryable"`
	Node         apiNodeResponse       `json:"node"`
	Membership   apiMembershipResponse `json:"membership"`
	Network      apiNetworkResponse    `json:"network"`
	Session      sessionResponse       `json:"session"`
	SessionToken string                `json:"session_token"`
}

type sessionResponse struct {
	Token     string    `json:"token"`
	Subject   string    `json:"subject"`
	NetworkID string    `json:"network_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type apiNetworkResponse struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Pool          string    `json:"pool"`
	IPv4Pool      string    `json:"ipv4_pool"`
	OwnerID       string    `json:"owner_id"`
	ConfigVersion int64     `json:"config_version"`
	CreatedAt     time.Time `json:"created_at"`
}

func makeNetworkResponse(network Network) apiNetworkResponse {
	return apiNetworkResponse{ID: network.ID, Name: network.Name, Pool: network.IPv4Pool, IPv4Pool: network.IPv4Pool, OwnerID: network.OwnerID, ConfigVersion: network.ConfigVersion, CreatedAt: network.CreatedAt}
}

type apiNodeResponse struct {
	ID            string    `json:"id"`
	DisplayName   string    `json:"display_name"`
	PublicKey     string    `json:"public_key"`
	Platform      string    `json:"platform"`
	ClientVersion string    `json:"client_version"`
	LastSeen      time.Time `json:"last_seen"`
}

func makeNodeResponse(node Node) apiNodeResponse {
	return apiNodeResponse{ID: node.ID, DisplayName: node.DisplayName, PublicKey: node.PublicKey, Platform: node.Platform, ClientVersion: node.ClientVersion, LastSeen: node.LastSeen}
}

type apiMembershipResponse struct {
	NetworkID   string `json:"network_id"`
	NodeID      string `json:"node_id"`
	VirtualIPv4 net.IP `json:"virtual_ipv4"`
	Role        string `json:"role"`
	Status      string `json:"status"`
}

func makeMembershipResponse(membership Membership) apiMembershipResponse {
	return apiMembershipResponse{NetworkID: membership.NetworkID, NodeID: membership.NodeID, VirtualIPv4: membership.VirtualIPv4, Role: membership.Role, Status: membership.Status}
}

type apiEndpointResponse struct {
	NodeID     string    `json:"node_id"`
	Address    net.IP    `json:"address"`
	Port       uint16    `json:"port"`
	Family     string    `json:"family"`
	Interface  string    `json:"interface"`
	Priority   int       `json:"priority"`
	ObservedAt time.Time `json:"observed_at"`
}

func makeEndpointResponse(endpoint EndpointCandidate) apiEndpointResponse {
	return apiEndpointResponse{NodeID: endpoint.NodeID, Address: endpoint.Address, Port: endpoint.Port, Family: endpoint.Family, Interface: endpoint.Interface, Priority: endpoint.Priority, ObservedAt: endpoint.ObservedAt}
}

type apiRelayAssignmentResponse struct {
	ID          string     `json:"id"`
	NetworkID   string     `json:"network_id"`
	NodeID      string     `json:"node_id"`
	RelayNodeID string     `json:"relay_node_id"`
	Address     net.IP     `json:"address"`
	Port        uint16     `json:"port"`
	Family      string     `json:"family"`
	Status      string     `json:"status"`
	AssignedAt  time.Time  `json:"assigned_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

func makeRelayAssignmentResponse(assignment RelayAssignment) apiRelayAssignmentResponse {
	return apiRelayAssignmentResponse{ID: assignment.ID, NetworkID: assignment.NetworkID, NodeID: assignment.NodeID, RelayNodeID: assignment.RelayNodeID, Address: assignment.Address, Port: assignment.Port, Family: assignment.Family, Status: assignment.Status, AssignedAt: assignment.AssignedAt, ExpiresAt: assignment.ExpiresAt}
}

type peerResponse struct {
	NodeID      string                `json:"node_id"`
	DisplayName string                `json:"display_name"`
	PublicKey   string                `json:"public_key"`
	VirtualIPv4 net.IP                `json:"virtual_ipv4"`
	Node        apiNodeResponse       `json:"node"`
	Membership  apiMembershipResponse `json:"membership"`
	Endpoints   []apiEndpointResponse `json:"endpoints"`
}

type snapshotResponseBody struct {
	NetworkID        string                      `json:"network_id"`
	Generation       int64                       `json:"generation"`
	ConfigVersion    int64                       `json:"config_version"`
	LocalNodeID      string                      `json:"local_node_id"`
	LocalVirtualIPv4 net.IP                      `json:"local_virtual_ipv4"`
	Peers            []peerResponse              `json:"peers"`
	RelayAssignment  *apiRelayAssignmentResponse `json:"relay_assignment,omitempty"`
	GeneratedAt      time.Time                   `json:"generated_at"`
}

func snapshotResponse(snapshot NetworkSnapshot) snapshotResponseBody {
	peers := make([]peerResponse, len(snapshot.Peers))
	for index, peer := range snapshot.Peers {
		endpoints := make([]apiEndpointResponse, len(peer.Endpoints))
		for endpointIndex, endpoint := range peer.Endpoints {
			endpoints[endpointIndex] = makeEndpointResponse(endpoint)
		}
		peers[index] = peerResponse{NodeID: peer.NodeID, DisplayName: peer.DisplayName, PublicKey: peer.PublicKey, VirtualIPv4: peer.VirtualIPv4, Node: makeNodeResponse(peer.Node), Membership: makeMembershipResponse(peer.Membership), Endpoints: endpoints}
	}
	var relay *apiRelayAssignmentResponse
	if snapshot.RelayAssignment != nil {
		converted := makeRelayAssignmentResponse(*snapshot.RelayAssignment)
		relay = &converted
	} else if snapshot.Relay != nil {
		converted := makeRelayAssignmentResponse(*snapshot.Relay)
		relay = &converted
	}
	return snapshotResponseBody{NetworkID: snapshot.NetworkID, Generation: snapshot.Generation, ConfigVersion: snapshot.ConfigVersion, LocalNodeID: snapshot.LocalNodeID, LocalVirtualIPv4: snapshot.LocalVirtualIPv4, Peers: peers, RelayAssignment: relay, GeneratedAt: snapshot.GeneratedAt}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeAPIError(writer http.ResponseWriter, status int, err error) {
	if status < 400 {
		status = http.StatusInternalServerError
	}
	code := "internal_error"
	switch status {
	case http.StatusUnauthorized:
		code = "unauthorized"
	case http.StatusForbidden:
		code = "forbidden"
	case http.StatusNotFound:
		code = "not_found"
	case http.StatusConflict:
		code = "conflict"
	case http.StatusUnprocessableEntity:
		code = "invalid_request"
	case http.StatusServiceUnavailable:
		code = "enrollment_recovery_pending"
	}
	writeJSON(writer, status, map[string]string{"error": code})
}

func statusForError(err error) int {
	switch {
	case errors.Is(err, auth.ErrMissingCredential), errors.Is(err, auth.ErrMalformedCredential),
		errors.Is(err, auth.ErrInvalidCredential), errors.Is(err, auth.ErrSessionExpired),
		errors.Is(err, auth.ErrInvalidBootstrapToken), errors.Is(err, auth.ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, auth.ErrInsufficientPermission), errors.Is(err, auth.ErrWrongNetwork), errors.Is(err, auth.ErrRevokedNetwork):
		return http.StatusForbidden
	case errors.Is(err, ErrEnrollmentRecoveryPending):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrConflict), errors.Is(err, ErrInviteConsumed), errors.Is(err, ErrInviteExpired), errors.Is(err, ErrInviteRevoked):
		return http.StatusConflict
	case errors.Is(err, ErrValidation), errors.Is(err, address.ErrInvalidPool), errors.Is(err, address.ErrPoolExhausted),
		errors.Is(err, ErrInvalidInviteToken), errors.Is(err, ErrInvalidRequest), errors.Is(err, errInvalidJSON):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}
