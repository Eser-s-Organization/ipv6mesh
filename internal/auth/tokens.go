// Package auth contains the control-plane authentication boundary. Bootstrap
// credentials are compared without a regular string comparison; enrolled
// sessions are opaque, short-lived values held only by the server.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

var (
	ErrUnauthorized           = errors.New("unauthorized")
	ErrMissingCredential      = errors.New("missing credential")
	ErrMalformedCredential    = errors.New("malformed bearer credential")
	ErrInvalidCredential      = errors.New("invalid credential")
	ErrInvalidBootstrapToken  = errors.New("invalid bootstrap token")
	ErrSessionExpired         = errors.New("session expired")
	ErrInvalidSessionTTL      = errors.New("invalid session ttl")
	ErrWrongNetwork           = errors.New("wrong network")
	ErrRevokedNetwork         = errors.New("revoked network")
	ErrInsufficientPermission = errors.New("insufficient permission")
)

// Role identifies the kind of principal represented by a session.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleNode  Role = "node"
)

// Session is the server-side claim set for an opaque bearer session. Token is
// deliberately not a field: callers should only receive the raw token at the
// point it is issued.
type Session struct {
	Subject   string
	Role      Role
	NetworkID string
	ExpiresAt time.Time
}

// SessionStoreOptions makes session tests deterministic without changing the
// production defaults.
type SessionStoreOptions struct {
	TTL    time.Duration
	Now    func() time.Time
	Random io.Reader
}

// SessionStore stores only opaque token verifiers and their short-lived
// claims. Network revocation is kept separately so a global administrator
// session can also be refused for a revoked network.
type SessionStore struct {
	mu              sync.Mutex
	ttl             time.Duration
	now             func() time.Time
	random          io.Reader
	sessions        map[string]Session
	revokedNetworks map[string]struct{}
}

// NewSessionStore creates a store with crypto/rand and the wall clock.
func NewSessionStore(ttl time.Duration) *SessionStore {
	return NewSessionStoreWithOptions(SessionStoreOptions{TTL: ttl})
}

// NewSessionStoreWithOptions creates a store with injectable time and entropy
// sources. A nil source uses the production default.
func NewSessionStoreWithOptions(options SessionStoreOptions) *SessionStore {
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	randomSource := options.Random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	return &SessionStore{
		ttl:             options.TTL,
		now:             now,
		random:          randomSource,
		sessions:        make(map[string]Session),
		revokedNetworks: make(map[string]struct{}),
	}
}

// ValidateBootstrapToken compares the configured bootstrap secret and the
// presented secret through fixed-size SHA-256 digests and
// subtle.ConstantTimeCompare. Empty values are never valid.
func ValidateBootstrapToken(configured, presented string) error {
	want := sha256.Sum256([]byte(configured))
	got := sha256.Sum256([]byte(presented))
	match := subtle.ConstantTimeCompare(want[:], got[:])
	if configured == "" || presented == "" || match != 1 {
		return ErrInvalidBootstrapToken
	}
	return nil
}

// BootstrapTokenValid is a boolean convenience wrapper for callers that do
// not need to classify the failure.
func BootstrapTokenValid(configured, presented string) bool {
	return ValidateBootstrapToken(configured, presented) == nil
}

// HashToken returns the lowercase SHA-256 digest of the complete raw token.
// Raw invite material must not be persisted or included in logs.
func HashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// ParseBearerHeader accepts exactly "Bearer <token>" with one ASCII space.
// It intentionally does not trim or accept additional fields.
func ParseBearerHeader(header string) (string, error) {
	if header == "" {
		return "", ErrMissingCredential
	}
	parts := strings.Split(header, " ")
	if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" || strings.ContainsAny(parts[1], " \t\r\n") {
		return "", ErrMalformedCredential
	}
	return parts[1], nil
}

// ParseBearerToken is an alias with a concise name for HTTP adapters.
func ParseBearerToken(header string) (string, error) {
	return ParseBearerHeader(header)
}

// AuthenticateHeader parses and authenticates an Authorization header.
func (store *SessionStore) AuthenticateHeader(header string) (Session, error) {
	token, err := ParseBearerHeader(header)
	if err != nil {
		return Session{}, err
	}
	return store.Authenticate(token)
}

// IssueAdminSession creates a global administrator session.
func (store *SessionStore) IssueAdminSession(subject string) (string, Session, error) {
	return store.IssueSession(subject, RoleAdmin, "")
}

// IssueAdminNetworkSession creates an administrator session scoped to one
// network. It is useful for operators that should not have global access.
func (store *SessionStore) IssueAdminNetworkSession(subject, networkID string) (string, Session, error) {
	return store.IssueSession(subject, RoleAdmin, networkID)
}

// IssueNodeSession creates a node session scoped to one network.
func (store *SessionStore) IssueNodeSession(subject, networkID string) (string, Session, error) {
	return store.IssueSession(subject, RoleNode, networkID)
}

// IssueSession creates a session with the requested role and network scope.
func (store *SessionStore) IssueSession(subject string, role Role, networkID string) (string, Session, error) {
	if store == nil || store.ttl <= 0 || strings.TrimSpace(subject) == "" {
		return "", Session{}, ErrInvalidSessionTTL
	}
	if role != RoleAdmin && role != RoleNode {
		return "", Session{}, ErrInsufficientPermission
	}
	if role == RoleNode && strings.TrimSpace(networkID) == "" {
		return "", Session{}, ErrWrongNetwork
	}
	now := store.now().UTC()
	session := Session{Subject: subject, Role: role, NetworkID: networkID, ExpiresAt: now.Add(store.ttl)}

	store.mu.Lock()
	defer store.mu.Unlock()
	if networkID != "" {
		if _, revoked := store.revokedNetworks[networkID]; revoked {
			return "", Session{}, ErrRevokedNetwork
		}
	}
	for attempt := 0; attempt < 4; attempt++ {
		value := make([]byte, 32)
		if _, err := io.ReadFull(store.random, value); err != nil {
			return "", Session{}, err
		}
		token := base64.RawURLEncoding.EncodeToString(value)
		if _, exists := store.sessions[token]; exists {
			continue
		}
		store.sessions[token] = session
		return token, session, nil
	}
	return "", Session{}, errors.New("unable to generate unique session token")
}

// Authenticate resolves an opaque token and checks expiration and network
// revocation at the time of use.
func (store *SessionStore) Authenticate(token string) (Session, error) {
	if store == nil || token == "" {
		return Session{}, ErrInvalidCredential
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	session, exists := store.sessions[token]
	if !exists {
		return Session{}, ErrInvalidCredential
	}
	if !store.now().UTC().Before(session.ExpiresAt) {
		delete(store.sessions, token)
		return Session{}, ErrSessionExpired
	}
	if session.NetworkID != "" {
		if _, revoked := store.revokedNetworks[session.NetworkID]; revoked {
			return Session{}, ErrRevokedNetwork
		}
	}
	return session, nil
}

// AuthorizeNetwork checks role scope and network revocation for a previously
// authenticated session.
func (store *SessionStore) AuthorizeNetwork(session Session, networkID string) error {
	if store == nil || strings.TrimSpace(networkID) == "" {
		return ErrWrongNetwork
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if !session.ExpiresAt.IsZero() && !store.now().UTC().Before(session.ExpiresAt) {
		return ErrSessionExpired
	}
	if _, revoked := store.revokedNetworks[networkID]; revoked {
		return ErrRevokedNetwork
	}
	switch session.Role {
	case RoleAdmin:
		if session.NetworkID != "" && session.NetworkID != networkID {
			return ErrWrongNetwork
		}
		return nil
	case RoleNode:
		if session.NetworkID != networkID {
			return ErrWrongNetwork
		}
		return nil
	default:
		return ErrInsufficientPermission
	}
}

// RevokeSession removes one issued session. It is used to compensate for a
// persistence commit failure after a transaction has reached its final
// session-signing step.
func (store *SessionStore) RevokeSession(token string) {
	if store == nil || token == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.sessions, token)
}

// RevokeNetwork records a network-level denial. Existing sessions are kept so
// callers can distinguish a revoked network (403) from an unknown credential
// (401).
func (store *SessionStore) RevokeNetwork(networkID string) {
	if store == nil || networkID == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.revokedNetworks[networkID] = struct{}{}
}

// IsNetworkRevoked reports whether network-scoped authorization is disabled.
func (store *SessionStore) IsNetworkRevoked(networkID string) bool {
	if store == nil {
		return false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	_, revoked := store.revokedNetworks[networkID]
	return revoked
}
