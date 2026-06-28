package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"testing"

	"golang.org/x/crypto/ssh"
)

func generateTestKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("new public key: %v", err)
	}
	return pub, string(ssh.MarshalAuthorizedKey(pub))
}

func TestLoadAuthorizedKeysLiteral(t *testing.T) {
	pub, line := generateTestKey(t)

	keys, err := loadAuthorizedKeys([]string{line})
	if err != nil {
		t.Fatalf("load keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if !keysEqual(keys[0], pub) {
		t.Fatal("loaded key does not match")
	}
}

func TestLoadAuthorizedKeysFromFile(t *testing.T) {
	pub1, line1 := generateTestKey(t)
	pub2, line2 := generateTestKey(t)

	tmp, err := os.CreateTemp("", "authorized_keys-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(line1 + "\n# comment\n" + line2 + "\n"); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	keys, err := loadAuthorizedKeys([]string{tmp.Name()})
	if err != nil {
		t.Fatalf("load keys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if !keysEqual(keys[0], pub1) || !keysEqual(keys[1], pub2) {
		t.Fatal("loaded keys do not match")
	}
}

func TestLoadAuthorizedKeysInvalidLiteral(t *testing.T) {
	_, err := loadAuthorizedKeys([]string{"not-a-key"})
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
}

func TestLoadAuthorizedKeysMissingFile(t *testing.T) {
	_, err := loadAuthorizedKeys([]string{"/nonexistent/path/authorized_keys"})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestKeysEqual(t *testing.T) {
	pub1, _ := generateTestKey(t)
	pub2, _ := generateTestKey(t)

	if !keysEqual(pub1, pub1) {
		t.Fatal("same key should be equal")
	}
	if keysEqual(pub1, pub2) {
		t.Fatal("different keys should not be equal")
	}
}
