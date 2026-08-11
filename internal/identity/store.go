// Package identity owns the device's WireGuard identity. Private key bytes
// stay inside this package and are never part of the service/IPC protocol.
package identity

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var (
	ErrUnsupportedPlatform = errors.New("identity protection is unsupported on this platform")
	ErrCorruptIdentity     = errors.New("identity record is corrupt")
	ErrInvalidIdentity     = errors.New("identity is invalid")
)

// Protector abstracts the platform key-protection primitive. Windows uses
// DPAPI; tests can inject a deterministic authenticated protector.
type Protector interface {
	Protect([]byte) ([]byte, error)
	Unprotect([]byte) ([]byte, error)
}

// Identity exposes only the public WireGuard key. The private key is kept in
// an unexported field so it cannot be returned by the IPC layer accidentally.
type Identity struct {
	PublicKey  string
	privateKey []byte
}

// PublicKeyValue returns the base64-encoded WireGuard public key.
func (identity Identity) PublicKeyValue() string { return identity.PublicKey }

type record struct {
	Version             int    `json:"version"`
	PublicKey           string `json:"public_key"`
	ProtectedPrivateKey string `json:"protected_private_key"`
}

// Store persists one identity record at path.
type Store struct {
	path      string
	protector Protector
	random    io.Reader
	secure    func(string) error
}

func NewStore(path string) *Store {
	return &Store{path: path, random: rand.Reader, secure: secureIdentityFile}
}

func NewStoreWithProtector(path string, protector Protector) *Store {
	// This constructor is the test-injection boundary. Production callers use
	// NewStore so platform ACL enforcement cannot be bypassed.
	return &Store{path: path, protector: protector, random: rand.Reader, secure: func(string) error { return nil }}
}

// LoadOrCreate loads an existing identity or creates it atomically on first
// use. A corrupt record is never silently replaced.
func (store *Store) LoadOrCreate() (Identity, error) {
	if store == nil || store.path == "" {
		return Identity{}, ErrInvalidIdentity
	}
	protector := store.protector
	if protector == nil {
		var err error
		protector, err = newDefaultProtector()
		if err != nil {
			return Identity{}, err
		}
	}
	if data, err := os.ReadFile(store.path); err == nil {
		return store.load(data, protector)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, err
	}
	return store.create(protector)
}

func (store *Store) load(data []byte, protector Protector) (Identity, error) {
	var saved record
	if err := json.Unmarshal(data, &saved); err != nil {
		return Identity{}, fmt.Errorf("%w: decode record: %v", ErrCorruptIdentity, err)
	}
	if saved.Version != 1 || saved.PublicKey == "" || saved.ProtectedPrivateKey == "" {
		return Identity{}, ErrCorruptIdentity
	}
	protected, err := base64.StdEncoding.DecodeString(saved.ProtectedPrivateKey)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: protected key encoding: %v", ErrCorruptIdentity, err)
	}
	privateKey, err := protector.Unprotect(protected)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: unprotect key: %v", ErrCorruptIdentity, err)
	}
	publicKey, err := publicKeyForPrivate(privateKey)
	if err != nil || encodeKey(publicKey) != saved.PublicKey {
		return Identity{}, ErrCorruptIdentity
	}
	return Identity{PublicKey: saved.PublicKey, privateKey: append([]byte(nil), privateKey...)}, nil
}

func (store *Store) create(protector Protector) (Identity, error) {
	curve := ecdh.X25519()
	key, err := curve.GenerateKey(store.random)
	if err != nil {
		return Identity{}, err
	}
	privateKey := key.Bytes()
	publicKey := key.PublicKey().Bytes()
	protected, err := protector.Protect(privateKey)
	if err != nil {
		return Identity{}, err
	}
	saved := record{Version: 1, PublicKey: encodeKey(publicKey), ProtectedPrivateKey: base64.StdEncoding.EncodeToString(protected)}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return Identity{}, err
	}
	if err := store.write(data); err != nil {
		return Identity{}, err
	}
	return Identity{PublicKey: saved.PublicKey, privateKey: append([]byte(nil), privateKey...)}, nil
}

func publicKeyForPrivate(privateKey []byte) ([]byte, error) {
	if len(privateKey) != 32 {
		return nil, ErrCorruptIdentity
	}
	key, err := ecdh.X25519().NewPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	return key.PublicKey().Bytes(), nil
}

func encodeKey(key []byte) string { return base64.StdEncoding.EncodeToString(key) }

func (store *Store) write(data []byte) error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(store.path), ".identity-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, store.path); err != nil {
		return err
	}
	secure := store.secure
	if secure == nil {
		secure = secureIdentityFile
	}
	return secure(store.path)
}
