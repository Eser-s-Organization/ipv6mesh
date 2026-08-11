package auth_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/auth"
)

func TestBootstrapTokenValidationAcceptsOnlyTheConfiguredSecret(t *testing.T) {
	if err := auth.ValidateBootstrapToken("bootstrap-secret", "bootstrap-secret"); err != nil {
		t.Fatalf("valid bootstrap token rejected: %v", err)
	}
	if err := auth.ValidateBootstrapToken("bootstrap-secret", "bootstrap-secrex"); !errors.Is(err, auth.ErrInvalidBootstrapToken) {
		t.Fatalf("wrong bootstrap token error = %v, want ErrInvalidBootstrapToken", err)
	}
	if err := auth.ValidateBootstrapToken("", "bootstrap-secret"); !errors.Is(err, auth.ErrInvalidBootstrapToken) {
		t.Fatalf("missing configured bootstrap token error = %v, want ErrInvalidBootstrapToken", err)
	}
}

func TestHashTokenHashesTheCompleteRawToken(t *testing.T) {
	if got := auth.HashToken("invite-1.secret"); got != "7a822ac8595e90f76a766456c581d12d2a4adc97cba607d6b2dc2e3954ed54ad" {
		t.Fatalf("HashToken = %q", got)
	}
}

func TestParseBearerHeaderRejectsMissingWrongAndExtraFields(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   error
	}{
		{name: "missing", header: "", want: auth.ErrMissingCredential},
		{name: "basic", header: "Basic abc", want: auth.ErrMalformedCredential},
		{name: "empty token", header: "Bearer ", want: auth.ErrMalformedCredential},
		{name: "extra field", header: "Bearer abc extra", want: auth.ErrMalformedCredential},
		{name: "leading whitespace", header: " Bearer abc", want: auth.ErrMalformedCredential},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := auth.ParseBearerHeader(test.header)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	token, err := auth.ParseBearerHeader("Bearer opaque-token")
	if err != nil || token != "opaque-token" {
		t.Fatalf("valid bearer header = %q, %v", token, err)
	}
}

func TestNodeSessionExpiresUsingInjectedClock(t *testing.T) {
	now := time.Date(2026, time.August, 11, 1, 0, 0, 0, time.UTC)
	store := auth.NewSessionStoreWithOptions(auth.SessionStoreOptions{
		TTL:    time.Minute,
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x4a}, 64)),
	})

	token, session, err := store.IssueNodeSession("node-1", "network-1")
	if err != nil {
		t.Fatalf("issue node session: %v", err)
	}
	if session.Subject != "node-1" || session.NetworkID != "network-1" || session.Role != auth.RoleNode {
		t.Fatalf("unexpected session claims: %+v", session)
	}
	if len(token) < 32 || strings.Contains(token, "node-1") {
		t.Fatalf("session token is not opaque/high entropy enough: %q", token)
	}

	if _, err := store.Authenticate(token); err != nil {
		t.Fatalf("fresh session rejected: %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := store.Authenticate(token); !errors.Is(err, auth.ErrSessionExpired) {
		t.Fatalf("expired session error = %v, want ErrSessionExpired", err)
	}
}

func TestSessionNetworkAuthorizationRejectsWrongAndRevokedNetworks(t *testing.T) {
	store := auth.NewSessionStoreWithOptions(auth.SessionStoreOptions{
		TTL:    time.Hour,
		Now:    time.Now,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x5b}, 128)),
	})
	token, _, err := store.IssueNodeSession("node-1", "network-1")
	if err != nil {
		t.Fatalf("issue node session: %v", err)
	}
	session, err := store.Authenticate(token)
	if err != nil {
		t.Fatalf("authenticate node session: %v", err)
	}
	if err := store.AuthorizeNetwork(session, "network-2"); !errors.Is(err, auth.ErrWrongNetwork) {
		t.Fatalf("wrong network error = %v, want ErrWrongNetwork", err)
	}

	store.RevokeNetwork("network-1")
	if _, err := store.Authenticate(token); !errors.Is(err, auth.ErrRevokedNetwork) {
		t.Fatalf("revoked network error = %v, want ErrRevokedNetwork", err)
	}
	if _, err := store.Authenticate(token); !errors.Is(err, auth.ErrRevokedNetwork) {
		t.Fatalf("repeated revoked network error = %v, want ErrRevokedNetwork", err)
	}
}

func TestSessionNetworkAuthorizationRejectsExpiredSessionClaims(t *testing.T) {
	now := time.Date(2026, time.August, 11, 1, 0, 0, 0, time.UTC)
	store := auth.NewSessionStoreWithOptions(auth.SessionStoreOptions{
		TTL:    time.Minute,
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x6c}, 64)),
	})
	_, session, err := store.IssueNodeSession("node-1", "network-1")
	if err != nil {
		t.Fatalf("issue node session: %v", err)
	}
	now = now.Add(time.Minute)
	if err := store.AuthorizeNetwork(session, "network-1"); !errors.Is(err, auth.ErrSessionExpired) {
		t.Fatalf("expired authorization error = %v, want ErrSessionExpired", err)
	}
}
