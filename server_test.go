package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/ssh"
)

func TestUserAllowedBackends(t *testing.T) {
	cfg := &Config{
		Users: []UserConfig{
			{Username: "alice", Backends: []string{"alpha", "beta"}},
			{Username: "bob"},
		},
	}

	if got := userAllowedBackends(cfg, "alice"); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("alice backends=%v", got)
	}
	if got := userAllowedBackends(cfg, "bob"); got != nil {
		t.Fatalf("bob should allow all, got %v", got)
	}
	if got := userAllowedBackends(cfg, "charlie"); got != nil {
		t.Fatalf("unknown user should allow all, got %v", got)
	}
}

func TestUserBackendPrefixes(t *testing.T) {
	cfg := &Config{
		Users: []UserConfig{
			{Username: "alice", Prefix: "site1"},
			{Username: "bob", BackendPrefixes: map[string]string{"r2": "site2"}},
			{Username: "carol"},
		},
	}
	alice := userBackendPrefixes(cfg, "alice")
	if got := alice["*"]; got != "site1" {
		t.Fatalf("alice wildcard prefix=%q, want site1", got)
	}
	bob := userBackendPrefixes(cfg, "bob")
	if got := bob["r2"]; got != "site2" {
		t.Fatalf("bob r2 prefix=%q, want site2", got)
	}
	if userBackendPrefixes(cfg, "carol") != nil {
		t.Fatal("expected nil for carol")
	}
	if userBackendPrefixes(cfg, "unknown") != nil {
		t.Fatal("expected nil for unknown")
	}
}

func TestIsBenignHandshakeErr(t *testing.T) {
	if isBenignHandshakeErr(nil) {
		t.Fatal("nil error should not be benign")
	}
	if !isBenignHandshakeErr(io.EOF) {
		t.Fatal("EOF should be benign")
	}
	if !isBenignHandshakeErr(net.ErrClosed) {
		t.Fatal("net.ErrClosed should be benign")
	}
	if !isBenignHandshakeErr(errors.New("connection reset by peer")) {
		t.Fatal("connection reset should be benign")
	}
	if !isBenignHandshakeErr(errors.New("use of closed network connection")) {
		t.Fatal("closed network connection should be benign")
	}
	if isBenignHandshakeErr(errors.New("some other error")) {
		t.Fatal("other errors should not be benign")
	}
}

func generateTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func generateTestAuthorizedKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return pub, string(ssh.MarshalAuthorizedKey(pub))
}

func TestNewSSHServerConfigPasswordAuth(t *testing.T) {
	signer := generateTestSigner(t)
	users := []UserConfig{
		{Username: "alice", Password: "secret"},
	}

	cfg, err := newSSHServerConfig(users, signer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	connMeta := &fakeConnMetadata{user: "alice"}

	perms, err := cfg.PasswordCallback(connMeta, []byte("secret"))
	if err != nil {
		t.Fatalf("valid password failed: %v", err)
	}
	if perms != nil {
		t.Fatal("expected nil permissions")
	}

	_, err = cfg.PasswordCallback(connMeta, []byte("wrong"))
	if err == nil {
		t.Fatal("invalid password should fail")
	}
}

func TestNewSSHServerConfigPublicKeyAuth(t *testing.T) {
	signer := generateTestSigner(t)
	pub, line := generateTestAuthorizedKey(t)
	wrongPub, _ := generateTestAuthorizedKey(t)

	users := []UserConfig{
		{Username: "alice", AuthorizedKeys: []string{line}},
	}

	cfg, err := newSSHServerConfig(users, signer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	connMeta := &fakeConnMetadata{user: "alice"}

	perms, err := cfg.PublicKeyCallback(connMeta, pub)
	if err != nil {
		t.Fatalf("valid key failed: %v", err)
	}
	if perms != nil {
		t.Fatal("expected nil permissions")
	}

	_, err = cfg.PublicKeyCallback(connMeta, wrongPub)
	if err == nil {
		t.Fatal("invalid key should fail")
	}
}

func TestNewSSHServerConfigInvalidAuthorizedKeys(t *testing.T) {
	signer := generateTestSigner(t)
	users := []UserConfig{
		{Username: "alice", AuthorizedKeys: []string{"not-a-key"}},
	}

	_, err := newSSHServerConfig(users, signer, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid authorized key")
	}
}

func TestNewSSHServerConfigNoAuthMethods(t *testing.T) {
	signer := generateTestSigner(t)
	users := []UserConfig{
		{Username: "alice"},
	}

	cfg, err := newSSHServerConfig(users, signer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PasswordCallback != nil || cfg.PublicKeyCallback != nil {
		t.Fatal("expected no auth callbacks")
	}
}

func TestNewSSHServerConfigPasswordAuthTarpit(t *testing.T) {
	signer := generateTestSigner(t)
	users := []UserConfig{
		{Username: "alice", Password: "secret"},
	}
	tracker, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts: 2,
		Window:      time.Minute.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	metrics := NewMetrics(prometheus.NewRegistry())

	cfg, err := newSSHServerConfig(users, signer, tracker, metrics)
	if err != nil {
		t.Fatal(err)
	}

	connMeta := &fakeConnMetadata{user: "alice"}
	// Two failed attempts should trip the tarpit.
	cfg.PasswordCallback(connMeta, []byte("wrong"))
	cfg.PasswordCallback(connMeta, []byte("wrong"))

	_, err = cfg.PasswordCallback(connMeta, []byte("secret"))
	if err == nil {
		t.Fatal("expected authentication to be blocked after max failures")
	}
}

type fakeConnMetadata struct {
	user string
}

func (f *fakeConnMetadata) User() string          { return f.user }
func (f *fakeConnMetadata) SessionID() []byte     { return nil }
func (f *fakeConnMetadata) ClientVersion() []byte { return nil }
func (f *fakeConnMetadata) ServerVersion() []byte { return nil }
func (f *fakeConnMetadata) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}
}
func (f *fakeConnMetadata) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}
}

func TestLoadOrGenerateHostKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host_key")

	signer, err := loadOrGenerateHostKey(path, "ed25519")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if signer == nil {
		t.Fatal("expected signer")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected 0600, got %o", info.Mode().Perm())
	}

	signer2, err := loadOrGenerateHostKey(path, "ed25519")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if signer2 == nil {
		t.Fatal("expected signer on load")
	}
}
