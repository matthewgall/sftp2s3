package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestGenerateEd25519HostKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host_ed25519_key")
	signer, err := generateHostKey(path, "ed25519")
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("expected ed25519 key, got %s", signer.PublicKey().Type())
	}

	loaded, err := loadHostKey(path)
	if err != nil {
		t.Fatalf("load ed25519 key: %v", err)
	}
	if !bytes.Equal(loaded.PublicKey().Marshal(), signer.PublicKey().Marshal()) {
		t.Fatal("loaded key does not match generated key")
	}
}

func TestGenerateRSAHostKeyDisablesSHA1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host_rsa_key")
	signer, err := generateHostKey(path, "rsa")
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoRSA {
		t.Fatalf("expected rsa key, got %s", signer.PublicKey().Type())
	}

	algSigner, ok := signer.(ssh.AlgorithmSigner)
	if !ok {
		t.Fatal("expected RSA signer to implement AlgorithmSigner")
	}
	if _, err := algSigner.SignWithAlgorithm(rand.Reader, []byte("data"), ssh.SigAlgoRSA); err == nil {
		t.Fatal("expected ssh-rsa (SHA-1) signature to be rejected")
	}
	if _, err := algSigner.SignWithAlgorithm(rand.Reader, []byte("data"), ssh.SigAlgoRSASHA2256); err != nil {
		t.Fatalf("expected rsa-sha2-256 signature to succeed: %v", err)
	}
}

func TestLoadHostKeyRSA(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host_rsa_key")
	if _, err := generateHostKey(path, "rsa"); err != nil {
		t.Fatalf("generate: %v", err)
	}

	signer, err := loadHostKey(path)
	if err != nil {
		t.Fatalf("load rsa key: %v", err)
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoRSA {
		t.Fatalf("expected rsa, got %s", signer.PublicKey().Type())
	}

	algSigner, ok := signer.(ssh.AlgorithmSigner)
	if !ok {
		t.Fatal("expected AlgorithmSigner")
	}
	if _, err := algSigner.SignWithAlgorithm(rand.Reader, []byte("data"), ssh.SigAlgoRSASHA2256); err != nil {
		t.Fatalf("sign: %v", err)
	}
}

func TestLoadHostKeyInvalidPEM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad_key")
	if err := os.WriteFile(path, []byte("not a pem"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := loadHostKey(path)
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

func TestGenerateHostKeyUnwritablePath(t *testing.T) {
	// Use a directory path as the destination so WriteFile fails.
	dir := t.TempDir()
	_, err := generateHostKey(dir, "ed25519")
	if err == nil {
		t.Fatal("expected error when host key path is unwritable")
	}
}

func TestGenerateRSAHostKeyUnwritablePath(t *testing.T) {
	dir := t.TempDir()
	_, err := generateHostKey(dir, "rsa")
	if err == nil {
		t.Fatal("expected error when rsa host key path is unwritable")
	}
}
