package main

import (
	"path/filepath"
	"testing"
)

func TestCacheSubDir(t *testing.T) {
	if got := cacheSubDir("", "sshid"); got != "" {
		t.Fatalf("empty cacheDir should return empty, got %q", got)
	}
	want := filepath.Join("/var/cache", "sshid")
	if got := cacheSubDir("/var/cache", "sshid"); got != want {
		t.Fatalf("cacheSubDir=%q, want %q", got, want)
	}
}
