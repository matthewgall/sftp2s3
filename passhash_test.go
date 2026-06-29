package main

import (
	"bytes"
	"io"
	"os"
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

	if err := runHashPassword(); err != nil {
		t.Fatalf("runHashPassword: %v", err)
	}
	_ = outW.Close()

	buf, _ := io.ReadAll(outR)
	hash := strings.TrimSpace(string(buf))
	if hash == "" {
		t.Fatal("expected hash output")
	}
	if !bytes.HasPrefix([]byte(hash), []byte("$2a$")) {
		t.Fatalf("expected bcrypt hash, got %q", hash)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("testpass")); err != nil {
		t.Fatalf("generated hash does not verify: %v", err)
	}
}
