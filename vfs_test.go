package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func makeTestVFS() *VFS {
	return &VFS{
		Backends: map[string]*Backend{
			"alpha": {Name: "alpha", Bucket: "a"},
			"beta":  {Name: "beta", Bucket: "b"},
			"gamma": {Name: "gamma", Bucket: "c"},
		},
	}
}

func TestNewVFS(t *testing.T) {
	configs := []BackendConfig{
		{
			Name:            "b",
			Bucket:          "bucket",
			Region:          "us-east-1",
			AccessKeyID:     "ak",
			SecretAccessKey: "sk",
			Prefix:          "router/",
			Timeout:         "30s",
		},
	}
	vfs, err := NewVFS(configs)
	if err != nil {
		t.Fatalf("NewVFS: %v", err)
	}
	if len(vfs.Backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(vfs.Backends))
	}
	b := vfs.Backends["b"]
	if b == nil {
		t.Fatal("backend missing")
	}
	if b.Prefix != "router/" {
		t.Fatalf("prefix=%q, want router/", b.Prefix)
	}
	if b.Timeout != 30*time.Second {
		t.Fatalf("timeout=%v, want 30s", b.Timeout)
	}
}

func TestNewVFSInvalidTimeout(t *testing.T) {
	configs := []BackendConfig{
		{Name: "b", Bucket: "bucket", Timeout: "not-a-duration"},
	}
	_, err := NewVFS(configs)
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
}

func TestBackendPrefixNormalization(t *testing.T) {
	b := &Backend{Prefix: "router/"}
	if got := b.objectKey("file.bin"); got != "router/file.bin" {
		t.Fatalf("objectKey=%q, want router/file.bin", got)
	}
	if got := b.dirPrefix("dir"); got != "router/dir/" {
		t.Fatalf("dirPrefix=%q, want router/dir/", got)
	}
}

func TestVFSResolve(t *testing.T) {
	vfs := makeTestVFS()

	tests := []struct {
		path     string
		wantName string
		wantKey  string
		wantErr  error
	}{
		{"/", "", "", nil},
		{"", "", "", nil},
		{"/alpha", "alpha", "", nil},
		{"/alpha/file.bin", "alpha", "file.bin", nil},
		{"/alpha/dir/file.bin", "alpha", "dir/file.bin", nil},
		{"/unknown", "", "", os.ErrNotExist},
	}

	for _, tt := range tests {
		b, key, err := vfs.Resolve(tt.path)
		if !errors.Is(err, tt.wantErr) {
			t.Fatalf("Resolve(%q) err=%v, want %v", tt.path, err, tt.wantErr)
		}
		if b != nil && b.Name != tt.wantName {
			t.Fatalf("Resolve(%q) backend=%q, want %q", tt.path, b.Name, tt.wantName)
		}
		if b == nil && tt.wantName != "" {
			t.Fatalf("Resolve(%q) got nil backend, want %q", tt.path, tt.wantName)
		}
		if key != tt.wantKey {
			t.Fatalf("Resolve(%q) key=%q, want %q", tt.path, key, tt.wantKey)
		}
	}
}

func TestVFSFilter(t *testing.T) {
	vfs := makeTestVFS()

	t.Run("nil allows all", func(t *testing.T) {
		f := vfs.Filter(nil)
		if len(f.Backends) != 3 {
			t.Fatalf("expected 3 backends, got %d", len(f.Backends))
		}
	})

	t.Run("empty allows all", func(t *testing.T) {
		f := vfs.Filter([]string{})
		if len(f.Backends) != 3 {
			t.Fatalf("expected 3 backends, got %d", len(f.Backends))
		}
	})

	t.Run("subset", func(t *testing.T) {
		f := vfs.Filter([]string{"alpha", "gamma"})
		if len(f.Backends) != 2 {
			t.Fatalf("expected 2 backends, got %d", len(f.Backends))
		}
		if _, ok := f.Backends["alpha"]; !ok {
			t.Fatalf("expected alpha in filtered backends")
		}
		if _, ok := f.Backends["beta"]; ok {
			t.Fatalf("did not expect beta in filtered backends")
		}
	})

	t.Run("unknown names ignored", func(t *testing.T) {
		f := vfs.Filter([]string{"delta"})
		if len(f.Backends) != 0 {
			t.Fatalf("expected 0 backends, got %d", len(f.Backends))
		}
	})
}

func TestBackendObjectKey(t *testing.T) {
	b := &Backend{Prefix: "router/"}
	if got := b.objectKey("file.bin"); got != "router/file.bin" {
		t.Fatalf("objectKey(file.bin)=%q, want router/file.bin", got)
	}
	if got := b.objectKey(""); got != "router/" {
		t.Fatalf("objectKey()=%q, want router/", got)
	}

	b2 := &Backend{Prefix: ""}
	if got := b2.objectKey("file.bin"); got != "file.bin" {
		t.Fatalf("objectKey(file.bin)=%q, want file.bin", got)
	}
}

func TestVFSWithUserPrefix(t *testing.T) {
	vfs := makeTestVFS()
	chroot := vfs.WithUserPrefix("site1")
	b := chroot.Backends["alpha"]
	if b.Prefix != "site1/" {
		t.Fatalf("expected prefix site1/, got %q", b.Prefix)
	}
	// original should be unchanged
	if vfs.Backends["alpha"].Prefix != "" {
		t.Fatal("original vfs was mutated")
	}
}

func TestVFSWithBackendPrefixes(t *testing.T) {
	vfs := makeTestVFS()
	vfs.Backends["alpha"].Prefix = "router/"

	chroot := vfs.WithBackendPrefixes(map[string]string{
		"alpha": "site1",
		"*":     "default",
	})

	if got := chroot.Backends["alpha"].Prefix; got != "router/site1/" {
		t.Fatalf("alpha prefix=%q, want router/site1/", got)
	}
	if got := chroot.Backends["beta"].Prefix; got != "default/" {
		t.Fatalf("beta prefix=%q, want default/", got)
	}
	if got := chroot.Backends["gamma"].Prefix; got != "default/" {
		t.Fatalf("gamma prefix=%q, want default/", got)
	}

	// original should be unchanged
	if vfs.Backends["alpha"].Prefix != "router/" {
		t.Fatal("original vfs was mutated")
	}
}

func TestVFSWithBackendPrefixesEmpty(t *testing.T) {
	vfs := makeTestVFS()
	if vfs.WithBackendPrefixes(nil) != vfs {
		t.Fatal("nil prefixes should return original vfs")
	}
	if vfs.WithBackendPrefixes(map[string]string{}) != vfs {
		t.Fatal("empty prefixes should return original vfs")
	}
}

func TestVFSWithUserPrefixEmpty(t *testing.T) {
	vfs := makeTestVFS()
	if vfs.WithUserPrefix("") != vfs {
		t.Fatal("empty prefix should return original vfs")
	}
	if vfs.WithUserPrefix("/") != vfs {
		t.Fatal("slash-only prefix should return original vfs")
	}
}

func TestVFSValidate(t *testing.T) {
	b := newMockBackend(t, "bucket", "router/", map[string][]byte{
		"router/file.bin": make([]byte, 100),
	})
	vfs := &VFS{Backends: map[string]*Backend{b.Name: b}}
	if err := vfs.Validate(context.Background()); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestVFSValidateFailure(t *testing.T) {
	b := newFailingBackend(t, "bucket", "")
	vfs := &VFS{Backends: map[string]*Backend{b.Name: b}}
	if err := vfs.Validate(context.Background()); err == nil {
		t.Fatal("expected validation error from failing backend")
	}
}

func TestBackendDirPrefix(t *testing.T) {
	b := &Backend{Prefix: "router/"}
	if got := b.dirPrefix("dir"); got != "router/dir/" {
		t.Fatalf("dirPrefix(dir)=%q, want router/dir/", got)
	}
	if got := b.dirPrefix(""); got != "router/" {
		t.Fatalf("dirPrefix()=%q, want router/", got)
	}

	empty := &Backend{Prefix: ""}
	if got := empty.dirPrefix("dir"); got != "dir/" {
		t.Fatalf("dirPrefix(dir)=%q, want dir/", got)
	}
	if got := empty.dirPrefix(""); got != "" {
		t.Fatalf("dirPrefix()=%q, want empty", got)
	}
}
