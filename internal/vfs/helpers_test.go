package vfs

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/matthewgall/sftp2s3/internal/storage"
	"github.com/matthewgall/sftp2s3/internal/storagetest"
)

func newMockBackend(t *testing.T, bucket, prefix string, content map[string][]byte) *storage.Backend {
	t.Helper()
	client := storagetest.NewS3Client(t, content, false)
	return &storage.Backend{
		Name:     "mock",
		Bucket:   bucket,
		Prefix:   prefix,
		Client:   client,
		Uploader: manager.NewUploader(client),
		PartSize: 8 * 1024 * 1024,
		Timeout:  5 * time.Second,
	}
}

func newFailingBackend(t *testing.T, bucket, prefix string) *storage.Backend {
	t.Helper()
	client := storagetest.NewS3Client(t, nil, true)
	return &storage.Backend{
		Name:     "mock",
		Bucket:   bucket,
		Prefix:   prefix,
		Client:   client,
		Uploader: manager.NewUploader(client),
		PartSize: 8 * 1024 * 1024,
		Timeout:  5 * time.Second,
	}
}
