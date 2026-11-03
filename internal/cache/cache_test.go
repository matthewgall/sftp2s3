package cache

import (
	"path/filepath"
	"testing"
)

func TestCacheSubDir(t *testing.T) {
	if got := SubDir("", "sshid"); got != "" {
		t.Fatalf("empty cacheDir should return empty, got %q", got)
	}
	want := filepath.Join("/var/cache", "sshid")
	if got := SubDir("/var/cache", "sshid"); got != want {
		t.Fatalf("SubDir=%q, want %q", got, want)
	}
}
