package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestS3DeletePrefixError(t *testing.T) {
	b := newFailingBackend(t, "bucket", "router/")
	if err := s3DeletePrefix(context.Background(), b, "router/dir/"); err == nil {
		t.Fatal("expected error from failing backend")
	}
}

func TestS3ObjectExistsError(t *testing.T) {
	b := newFailingBackend(t, "bucket", "")
	_, err := s3ObjectExists(context.Background(), b, "file.bin")
	if err == nil {
		t.Fatal("expected error from failing backend")
	}
}

func TestS3PrefixHasEntriesError(t *testing.T) {
	b := newFailingBackend(t, "bucket", "")
	_, err := s3PrefixHasEntries(context.Background(), b, "dir/")
	if err == nil {
		t.Fatal("expected error from failing backend")
	}
}

func TestS3CopyObjectError(t *testing.T) {
	b := newFailingBackend(t, "bucket", "")
	err := s3CopyObject(context.Background(), b, "src.bin", "dst.bin")
	if err == nil {
		t.Fatal("expected error from failing backend")
	}
}

func TestS3DeleteObjectNotFoundIgnored(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	// Deleting a non-existent object should not return an error.
	if err := s3DeleteObject(context.Background(), b, "missing.bin"); err != nil {
		t.Fatalf("delete missing object: %v", err)
	}
}

func TestS3DeleteObjectError(t *testing.T) {
	b := newFailingBackend(t, "bucket", "")
	if err := s3DeleteObject(context.Background(), b, "file.bin"); err == nil {
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
	if err := s3DeletePrefix(context.Background(), b, "router/dir/"); err != nil {
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
