package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/ssh"
)

// loadOrGenerateHostKey loads an existing host key or generates a new one of
// the requested type and persists it to path.
func loadOrGenerateHostKey(path, keyType string) (ssh.Signer, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return generateHostKey(path, keyType)
	}
	return loadHostKey(path)
}

// generateHostKey creates a host key of the requested type and persists it.
func generateHostKey(path, keyType string) (ssh.Signer, error) {
	switch keyType {
	case "rsa":
		return generateRSAHostKey(path)
	case "ed25519":
		return generateEd25519HostKey(path)
	default:
		return nil, fmt.Errorf("unsupported host key type %q", keyType)
	}
}

// generateRSAHostKey creates a 3072-bit RSA host key.
func generateRSAHostKey(path string) (ssh.Signer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, fmt.Errorf("generate rsa key: %w", err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, pemData, 0600); err != nil {
		return nil, fmt.Errorf("write host key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(pemData)
	if err != nil {
		return nil, fmt.Errorf("parse rsa key: %w", err)
	}
	return wrapRSASigner(signer), nil
}

// generateEd25519HostKey creates an Ed25519 host key in OpenSSH format.
func generateEd25519HostKey(path string) (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	marshalled, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, fmt.Errorf("marshal ed25519 key: %w", err)
	}
	pemData := pem.EncodeToMemory(marshalled)
	if err := os.WriteFile(path, pemData, 0600); err != nil {
		return nil, fmt.Errorf("write host key: %w", err)
	}
	return ssh.ParsePrivateKey(pemData)
}

// loadHostKey parses an existing PEM-encoded host key from path. RSA keys are
// wrapped so that the SHA-1 ssh-rsa algorithm cannot be negotiated.
func loadHostKey(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read host key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}
	return wrapRSASigner(signer), nil
}

// rsaAlgorithmSigner wraps an RSA signer and refuses to use the SHA-1 ssh-rsa
// algorithm, leaving only rsa-sha2-256 and rsa-sha2-512 available.
type rsaAlgorithmSigner struct {
	ssh.AlgorithmSigner
}

func (s *rsaAlgorithmSigner) SignWithAlgorithm(rand io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	if algorithm == ssh.SigAlgoRSA {
		return nil, fmt.Errorf("ssh-rsa (SHA-1) is disabled")
	}
	return s.AlgorithmSigner.SignWithAlgorithm(rand, data, algorithm)
}

// wrapRSASigner returns a signer that filters out the SHA-1 RSA algorithm. If
// the provided signer is not an RSA AlgorithmSigner it is returned unchanged.
func wrapRSASigner(signer ssh.Signer) ssh.Signer {
	if signer.PublicKey().Type() != ssh.KeyAlgoRSA {
		return signer
	}
	algSigner, ok := signer.(ssh.AlgorithmSigner)
	if !ok {
		return signer
	}
	return &rsaAlgorithmSigner{AlgorithmSigner: algSigner}
}
