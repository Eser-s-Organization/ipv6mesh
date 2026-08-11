package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type testProtector struct {
	key [32]byte
}

func (protector testProtector) Protect(plain []byte) ([]byte, error) {
	return protector.seal(plain)
}

func (protector testProtector) Unprotect(protected []byte) ([]byte, error) {
	return protector.open(protected)
}

func (protector testProtector) cipher() (cipher.AEAD, error) {
	block, err := aes.NewCipher(protector.key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (protector testProtector) seal(plain []byte) ([]byte, error) {
	aead, err := protector.cipher()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, nil), nil
}

func (protector testProtector) open(protected []byte) ([]byte, error) {
	aead, err := protector.cipher()
	if err != nil {
		return nil, err
	}
	if len(protected) < aead.NonceSize() {
		return nil, io.ErrUnexpectedEOF
	}
	nonce, ciphertext := protected[:aead.NonceSize()], protected[aead.NonceSize():]
	return aead.Open(nil, nonce, ciphertext, nil)
}

func TestStoreGeneratesAndRestoresStablePublicKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	protector := testProtector{key: [32]byte{1, 2, 3, 4}}

	firstStore := NewStoreWithProtector(path, protector)
	first, err := firstStore.LoadOrCreate()
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	if first.PublicKey == "" {
		t.Fatal("first identity has empty public key")
	}

	secondStore := NewStoreWithProtector(path, protector)
	second, err := secondStore.LoadOrCreate()
	if err != nil {
		t.Fatalf("restart LoadOrCreate: %v", err)
	}
	if second.PublicKey != first.PublicKey {
		t.Fatalf("public key changed across restart: first %q, second %q", first.PublicKey, second.PublicKey)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read identity record: %v", err)
	}
	if string(contents) == "" {
		t.Fatal("identity record is empty")
	}
	var record map[string]any
	if err := json.Unmarshal(contents, &record); err != nil {
		t.Fatalf("identity record is not JSON: %v", err)
	}
	if _, exists := record["private_key"]; exists {
		t.Fatal("identity record contains an unprotected private_key field")
	}
	if _, exists := record["protected_private_key"]; !exists {
		t.Fatal("identity record does not contain protected private key material")
	}
}

func TestIdentityDoesNotExposePrivateKey(t *testing.T) {
	type publicOnly interface {
		PublicKeyValue() string
	}

	var _ publicOnly = (*Identity)(nil)
	if _, ok := any(Identity{}).(interface{ PrivateKey() []byte }); ok {
		t.Fatal("identity exposes a private-key accessor")
	}
}
