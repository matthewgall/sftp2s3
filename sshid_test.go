package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSHIDResolveFetchesAndCaches(t *testing.T) {
	oldBase := sshidBaseURL
	defer func() { sshidBaseURL = oldBase }()

	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		gotAccept = r.Header.Get("Accept")
		w.Write([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFetc alice@example\nssh-rsa AAAAB3NzaC1yc2etc bob@example\n"))
	}))
	defer srv.Close()
	sshidBaseURL = srv.URL

	cacheDir := t.TempDir()
	resolver := newSSHIDResolver(cacheDir)
	resolver.ttl = time.Hour

	keys, err := resolver.Resolve(&SSHIDConfig{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}
	if gotUA != sshidUserAgent {
		t.Fatalf("User-Agent=%q, want %q", gotUA, sshidUserAgent)
	}
	if gotAccept != "*/*" {
		t.Fatalf("Accept=%q, want */*", gotAccept)
	}

	// A second resolve should hit the cache, not the server.
	keys2, err := resolver.Resolve(&SSHIDConfig{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if keys2[0] != keys[0] {
		t.Fatal("cache returned different key")
	}

	// Cache file should exist under the sshid subdirectory.
	cacheFile := filepath.Join(cacheDir, "sshid", "sshid-alice.keys")
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
}

func TestSSHIDResolveAlgorithmFilter(t *testing.T) {
	oldBase := sshidBaseURL
	defer func() { sshidBaseURL = oldBase }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ssh-ed25519 AAAADYNAMIC dynamic@example\nssh-rsa AAAARSA rsa@example\n"))
	}))
	defer srv.Close()
	sshidBaseURL = srv.URL

	resolver := newSSHIDResolver("")
	keys, err := resolver.Resolve(&SSHIDConfig{Username: "x", Algorithms: []string{"ed25519"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !strings.Contains(keys[0], "AAAADYNAMIC") {
		t.Fatalf("expected 1 ed25519 key, got %v", keys)
	}
}

func TestSSHIDResolveStaleFallback(t *testing.T) {
	oldBase := sshidBaseURL
	defer func() { sshidBaseURL = oldBase }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	sshidBaseURL = srv.URL

	cacheDir := t.TempDir()
	cacheFile := filepath.Join(cacheDir, "sshid", "sshid-bob.keys")
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheFile, []byte("ssh-ed25519 AAAACACHED bob@example\n"), 0600); err != nil {
		t.Fatal(err)
	}

	resolver := newSSHIDResolver(cacheDir)
	resolver.ttl = 0 // force treat cache as stale but present
	keys, err := resolver.Resolve(&SSHIDConfig{Username: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !strings.Contains(keys[0], "AAAACACHED") {
		t.Fatalf("expected stale cached key, got %v", keys)
	}
}

func TestSSHIDResolveNotFound(t *testing.T) {
	oldBase := sshidBaseURL
	defer func() { sshidBaseURL = oldBase }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	sshidBaseURL = srv.URL

	resolver := newSSHIDResolver("")
	keys, err := resolver.Resolve(&SSHIDConfig{Username: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected no keys for 404, got %v", keys)
	}
}

func TestResolveSSHIDKeysAppends(t *testing.T) {
	oldBase := sshidBaseURL
	defer func() { sshidBaseURL = oldBase }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ssh-ed25519 AAAADYNAMIC dynamic@example\n"))
	}))
	defer srv.Close()
	sshidBaseURL = srv.URL

	cfg := &Config{}
	cfg.Users = []UserConfig{{
		Username:       "backup",
		AuthorizedKeys: []string{"ssh-ed25519 AAAASTATIC static@example"},
		SSHID: &SSHIDConfig{
			Username: "backupuser",
		},
	}}

	if err := resolveSSHIDKeys(cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Users[0].AuthorizedKeys) != 2 {
		t.Fatalf("expected 2 keys, got %v", cfg.Users[0].AuthorizedKeys)
	}
	if !strings.Contains(cfg.Users[0].AuthorizedKeys[1], "AAAADYNAMIC") {
		t.Fatalf("sshid key not appended: %v", cfg.Users[0].AuthorizedKeys)
	}
}
