package storage

import (
	"context"
	"testing"
	"time"

	"github.com/matthewgall/sftp2s3/internal/config"
	"github.com/matthewgall/sftp2s3/internal/storagetest"
)

func TestNewBackendValid(t *testing.T) {
	b, err := NewBackend(context.Background(), config.BackendConfig{
		Name:            "primary",
		Bucket:          "bucket",
		Region:          "us-east-1",
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
		Prefix:          "router/",
		Timeout:         "30s",
		PartSize:        5 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	if b.Name != "primary" {
		t.Fatalf("name=%q", b.Name)
	}
	if b.Bucket != "bucket" {
		t.Fatalf("bucket=%q", b.Bucket)
	}
	if b.Prefix != "router/" {
		t.Fatalf("prefix=%q", b.Prefix)
	}
	if b.Timeout != 30*time.Second {
		t.Fatalf("timeout=%v", b.Timeout)
	}
	if b.Client == nil || b.Uploader == nil {
		t.Fatal("expected client and uploader")
	}
}

func TestNewBackendInvalidTimeout(t *testing.T) {
	_, err := NewBackend(context.Background(), config.BackendConfig{
		Name:    "b",
		Bucket:  "bucket",
		Region:  "us-east-1",
		Timeout: "not-a-duration",
	})
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
}

func TestBackendObjectKey(t *testing.T) {
	b := &Backend{Prefix: "router/"}
	if got := b.ObjectKey("file.bin"); got != "router/file.bin" {
		t.Fatalf("ObjectKey=%q", got)
	}
	empty := &Backend{Prefix: ""}
	if got := empty.ObjectKey("file.bin"); got != "file.bin" {
		t.Fatalf("ObjectKey no prefix=%q", got)
	}
}

func TestBackendDirPrefix(t *testing.T) {
	b := &Backend{Prefix: "router/"}
	if got := b.DirPrefix("dir"); got != "router/dir/" {
		t.Fatalf("DirPrefix=%q", got)
	}
	empty := &Backend{Prefix: ""}
	if got := empty.DirPrefix("dir"); got != "dir/" {
		t.Fatalf("DirPrefix empty=%q", got)
	}
	if got := empty.DirPrefix(""); got != "" {
		t.Fatalf("DirPrefix empty root=%q", got)
	}
}

func TestBackendString(t *testing.T) {
	b := &Backend{Name: "mock", Bucket: "bucket"}
	if got := b.String(); got != "mock/bucket" {
		t.Fatalf("String=%q", got)
	}
}

func TestBackendOperationsWithMock(t *testing.T) {
	client := storagetest.NewS3Client(t, map[string][]byte{"file.bin": []byte("hello")}, false)
	b := &Backend{
		Name:     "mock",
		Bucket:   "bucket",
		Prefix:   "",
		Client:   client,
		Timeout:  5 * time.Second,
		PartSize: 8 * 1024 * 1024,
	}

	exists, err := b.ObjectExists(context.Background(), "file.bin")
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	exists, err = b.ObjectExists(context.Background(), "missing.bin")
	if err != nil || exists {
		t.Fatalf("missing exists=%v err=%v", exists, err)
	}
}
