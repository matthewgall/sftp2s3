package sftphandlers

import (
	"testing"
	"time"

	"github.com/matthewgall/sftp2s3/internal/backend"
	"github.com/matthewgall/sftp2s3/internal/backendtest"
)

func newMockBackend(t *testing.T, bucket, prefix string, content map[string][]byte) *backend.Backend {
	t.Helper()
	client := backendtest.NewS3Client(t, content, false)
	return &backend.Backend{
		Name:     "mock",
		Bucket:   bucket,
		Prefix:   prefix,
		Client:   client,
		Uploader: backendtest.NewUploader(client),
		PartSize: 8 * 1024 * 1024,
		Timeout:  5 * time.Second,
	}
}
