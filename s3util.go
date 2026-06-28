package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// s3DeleteObject deletes a single object from b.
func s3DeleteObject(ctx context.Context, b *Backend, key string) error {
	_, err := b.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.Bucket),
		Key:    aws.String(key),
	})
	return err
}

// s3DeletePrefix deletes all objects under prefix from b.
func s3DeletePrefix(ctx context.Context, b *Backend, prefix string) error {
	paginator := s3.NewListObjectsV2Paginator(b.Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(b.Bucket),
		Prefix: aws.String(prefix),
	})

	var keys []types.ObjectIdentifier
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			keys = append(keys, types.ObjectIdentifier{Key: obj.Key})
		}
	}

	for i := 0; i < len(keys); i += 1000 {
		end := i + 1000
		if end > len(keys) {
			end = len(keys)
		}
		_, err := b.Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(b.Bucket),
			Delete: &types.Delete{
				Objects: keys[i:end],
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// s3ObjectExists reports whether key exists in b.
func s3ObjectExists(ctx context.Context, b *Backend, key string) (bool, error) {
	_, err := b.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.Bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return false, nil
	}
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return false, nil
	}
	return false, err
}

// s3CopyObject copies srcKey to dstKey within b.
func s3CopyObject(ctx context.Context, b *Backend, srcKey, dstKey string) error {
	copySource := fmt.Sprintf("/%s/%s", b.Bucket, url.PathEscape(srcKey))
	_, err := b.Client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(b.Bucket),
		Key:        aws.String(dstKey),
		CopySource: aws.String(copySource),
	})
	return err
}

// s3PrefixHasEntries reports whether any object or common prefix exists under
// prefix in b.
func s3PrefixHasEntries(ctx context.Context, b *Backend, prefix string) (bool, error) {
	out, err := b.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:    aws.String(b.Bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
		MaxKeys:   aws.Int32(1),
	})
	if err != nil {
		return false, err
	}
	return len(out.CommonPrefixes) > 0 || len(out.Contents) > 0, nil
}
