// Package s3 provides a thin wrapper around an S3-compatible backend.
package storage

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/matthewgall/sftp2s3/internal/config"
)

const defaultTimeout = 60 * time.Second

// Backend holds an S3 client and configuration for a single backend.
type Backend struct {
	Name     string
	Bucket   string
	Prefix   string
	Client   *s3.Client
	Uploader *manager.Uploader
	PartSize int64
	Timeout  time.Duration
}

// ObjectKey returns the full S3 object key for a path relative to the backend.
func (b *Backend) ObjectKey(rel string) string {
	if b.Prefix == "" {
		return rel
	}
	return b.Prefix + rel
}

// DirPrefix returns the S3 prefix representing a directory for the given
// relative path, including a trailing slash.
func (b *Backend) DirPrefix(rel string) string {
	if b.Prefix == "" && rel == "" {
		return ""
	}
	return strings.TrimSuffix(b.ObjectKey(rel), "/") + "/"
}

// newHTTPClient returns an http.Client tuned for the given request timeout.
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
		},
	}
}

// NewBackend creates and validates an S3 Backend from a single backend
// configuration.
func NewBackend(ctx context.Context, bc config.BackendConfig) (*Backend, error) {
	timeout := defaultTimeout
	if bc.Timeout != "" {
		d, err := time.ParseDuration(bc.Timeout)
		if err != nil {
			return nil, fmt.Errorf("backend %q: invalid timeout %q: %w", bc.Name, bc.Timeout, err)
		}
		timeout = d
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(bc.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(bc.AccessKeyID, bc.SecretAccessKey, ""),
		),
		awsconfig.WithHTTPClient(newHTTPClient(timeout)),
	)
	if err != nil {
		return nil, fmt.Errorf("backend %q: aws config: %w", bc.Name, err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if bc.EndpointURL != "" {
			o.BaseEndpoint = &bc.EndpointURL
		}
		o.UsePathStyle = bc.UsePathStyle || bc.PathStyleLegacy
	})

	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = bc.PartSize
	})

	prefix := strings.Trim(bc.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}

	return &Backend{
		Name:     bc.Name,
		Bucket:   bc.Bucket,
		Prefix:   prefix,
		Client:   client,
		Uploader: uploader,
		PartSize: bc.PartSize,
		Timeout:  timeout,
	}, nil
}

// String returns a short description of the backend for logging.
func (b *Backend) String() string {
	return fmt.Sprintf("%s/%s", b.Name, b.Bucket)
}
