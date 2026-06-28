package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
	_, err := generateHostKey(dir)
	if err == nil {
		t.Fatal("expected error when host key path is unwritable")
	}
}
