package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"
)

// loadOrGenerateHostKey loads an existing host key or generates a new RSA key
// and writes it to path.
func loadOrGenerateHostKey(path string) (ssh.Signer, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return generateHostKey(path)
	}
	return loadHostKey(path)
}

// generateHostKey creates a 3072-bit RSA host key and persists it to path.
func generateHostKey(path string) (ssh.Signer, error) {
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
	return ssh.ParsePrivateKey(pemData)
}

// loadHostKey parses an existing PEM-encoded host key from path.
func loadHostKey(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read host key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}
	return signer, nil
}
