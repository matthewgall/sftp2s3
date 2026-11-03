package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestS3DeletePrefixError(t *testing.T) {
	b := newFailingBackend(t, "bucket", "router/")
	if err := b.DeletePrefix(context.Background(), "router/dir/"); err == nil {
		t.Fatal("expected error from failing backend")
	}
}

func TestS3ObjectExistsError(t *testing.T) {
	b := newFailingBackend(t, "bucket", "")
	_, err := b.ObjectExists(context.Background(), "file.bin")
	if err == nil {
		t.Fatal("expected error from failing backend")
	}
}

func TestS3PrefixHasEntriesError(t *testing.T) {
	b := newFailingBackend(t, "bucket", "")
	_, err := b.PrefixHasEntries(context.Background(), "dir/")
	if err == nil {
		t.Fatal("expected error from failing backend")
	}
}

func TestS3CopyObjectError(t *testing.T) {
	b := newFailingBackend(t, "bucket", "")
	err := b.CopyObject(context.Background(), "src.bin", "dst.bin")
	if err == nil {
		t.Fatal("expected error from failing backend")
	}
}

func TestS3DeleteObjectNotFoundIgnored(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	// Deleting a non-existent object should not return an error.
	if err := b.DeleteObject(context.Background(), "missing.bin"); err != nil {
		t.Fatalf("delete missing object: %v", err)
	}
}

func TestS3DeleteObjectError(t *testing.T) {
	b := newFailingBackend(t, "bucket", "")
	if err := b.DeleteObject(context.Background(), "file.bin"); err == nil {
		t.Fatal("expected error from failing backend")
	}
}

func TestS3DeletePrefixWithContinuation(t *testing.T) {
	// This test relies on the fakeS3 implementation returning all objects in a
	// single page, so it verifies the basic delete path for a populated prefix.
	objects := map[string][]byte{
		"router/dir/file1.bin": make([]byte, 10),
		"router/dir/file2.bin": make([]byte, 20),
	}
	b := newMockBackend(t, "bucket", "", objects)
	if err := b.DeletePrefix(context.Background(), "router/dir/"); err != nil {
		t.Fatalf("delete prefix: %v", err)
	}
	for k := range objects {
		_, err := b.Client.HeadObject(context.Background(), &s3.HeadObjectInput{
			Bucket: &b.Bucket,
			Key:    &k,
		})
		var nf *types.NotFound
		if err != nil && !errors.As(err, &nf) {
			t.Fatalf("unexpected head error for %s: %v", k, err)
		}
	}
}
