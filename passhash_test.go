package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestPasswordMatchesHash(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	u := UserConfig{Username: "u", PasswordHash: string(hash)}
	if !passwordMatches(u, "secret") {
		t.Fatal("expected password to match hash")
	}
	if passwordMatches(u, "wrong") {
		t.Fatal("expected wrong password to fail")
	}
}

func TestPasswordMatchesPlaintext(t *testing.T) {
	u := UserConfig{Username: "u", Password: "secret"}
	if !passwordMatches(u, "secret") {
		t.Fatal("expected plaintext password to match")
	}
	if passwordMatches(u, "wrong") {
		t.Fatal("expected wrong password to fail")
	}
}

func TestPasswordHashPreferred(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hashed"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	u := UserConfig{Username: "u", PasswordHash: string(hash), Password: "plain"}
	if !passwordMatches(u, "hashed") {
		t.Fatal("expected hash to take precedence")
	}
	if passwordMatches(u, "plain") {
		t.Fatal("expected plaintext fallback to be ignored when hash is set")
	}
}

func TestRunVerifyPasswordEmpty(t *testing.T) {
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()
	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() { _ = w.Close() }()
	if err := runVerifyPassword("$2a$10$OHesdVg9R.GQVTaBzy.qS.hBkDE4P2li81yE.yk.F8Aj0XAvvbg5u"); err == nil {
		t.Fatal("expected error for empty password")
	}
}

func TestRunVerifyPasswordMismatch(t *testing.T) {
	oldStdin := os.Stdin
	oldStdout := os.Stdout
	defer func() { os.Stdin = oldStdin; os.Stdout = oldStdout }()

	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		_, _ = w.WriteString("wrongpass\n")
		_ = w.Close()
	}()

	outR, outW, _ := os.Pipe()
	os.Stdout = outW
	hash, _ := bcrypt.GenerateFromPassword([]byte("rightpass"), bcrypt.MinCost)
	if err := runVerifyPassword(string(hash)); err != nil {
		t.Fatalf("runVerifyPassword: %v", err)
	}
	_ = outW.Close()
	buf, _ := io.ReadAll(outR)
	if strings.TrimSpace(string(buf)) != "mismatch" {
		t.Fatalf("expected mismatch, got %q", buf)
	}
}

func TestRunVerifyPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("testpass"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}

	oldStdin := os.Stdin
	oldStdout := os.Stdout
	defer func() { os.Stdin = oldStdin; os.Stdout = oldStdout }()

	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		_, _ = w.WriteString("testpass\n")
		_ = w.Close()
	}()

	outR, outW, _ := os.Pipe()
	os.Stdout = outW

	if err := runVerifyPassword(string(hash)); err != nil {
		t.Fatalf("runVerifyPassword: %v", err)
	}
	_ = outW.Close()

	buf, _ := io.ReadAll(outR)
	if strings.TrimSpace(string(buf)) != "match" {
		t.Fatalf("expected match, got %q", buf)
	}
}

func TestRunHashPassword(t *testing.T) {
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()

	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		_, _ = w.WriteString("testpass\n")
		_ = w.Close()
	}()

	outFile := filepath.Join(t.TempDir(), "hash.txt")
	if err := runHashPassword(outFile); err != nil {
		t.Fatalf("runHashPassword: %v", err)
	}

	hash, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	hashStr := strings.TrimSpace(string(hash))
	if hashStr == "" {
		t.Fatal("expected hash output")
	}
	if !bytes.HasPrefix([]byte(hashStr), []byte("$2a$")) {
		t.Fatalf("expected bcrypt hash, got %q", hashStr)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashStr), []byte("testpass")); err != nil {
		t.Fatalf("generated hash does not verify: %v", err)
	}
}

func TestRunHashPasswordErrors(t *testing.T) {
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()

	// Empty password.
	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() { _ = w.Close() }()
	if err := runHashPassword(filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected error for empty password")
	}

	// Unwritable output path.
	r2, w2, _ := os.Pipe()
	os.Stdin = r2
	go func() {
		_, _ = w2.WriteString("testpass\n")
		_ = w2.Close()
	}()
	if err := runHashPassword(filepath.Join("/dev/null", "cannot-write-here")); err == nil {
		t.Fatal("expected error for unwritable output path")
	}
}

func TestRunHashPasswordStdout(t *testing.T) {
	oldStdin := os.Stdin
	oldStdout := os.Stdout
	defer func() { os.Stdin = oldStdin; os.Stdout = oldStdout }()

	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		_, _ = w.WriteString("stdoutpass\n")
		_ = w.Close()
	}()

	outR, outW, _ := os.Pipe()
	os.Stdout = outW
	if err := runHashPassword("-"); err != nil {
		t.Fatalf("runHashPassword stdout: %v", err)
	}
	_ = outW.Close()
	buf, _ := io.ReadAll(outR)
	if !bytes.HasPrefix(bytes.TrimSpace(buf), []byte("$2a$")) {
		t.Fatalf("expected bcrypt hash on stdout, got %q", buf)
	}
}
