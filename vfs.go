package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

// objectKey returns the full S3 object key for a path relative to the backend.
func (b *Backend) objectKey(rel string) string {
	if b.Prefix == "" {
		return rel
	}
	return b.Prefix + rel
}

// dirPrefix returns the S3 prefix representing a directory for the given
// relative path, including a trailing slash.
func (b *Backend) dirPrefix(rel string) string {
	if b.Prefix == "" && rel == "" {
		return ""
	}
	return strings.TrimSuffix(b.objectKey(rel), "/") + "/"
}

// VFS maps backend names to S3 backends and resolves SFTP paths.
type VFS struct {
	Backends map[string]*Backend
}

// newS3HTTPClient returns an http.Client tuned for the given request timeout.
func newS3HTTPClient(timeout time.Duration) *http.Client {
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

// NewVFS builds a VFS from configuration, creating an S3 client and uploader
// for each backend.
func NewVFS(configs []BackendConfig) (*VFS, error) {
	ctx := context.Background()
	vfs := &VFS{Backends: make(map[string]*Backend, len(configs))}

	for _, bc := range configs {
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
			awsconfig.WithHTTPClient(newS3HTTPClient(timeout)),
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

		vfs.Backends[bc.Name] = &Backend{
			Name:     bc.Name,
			Bucket:   bc.Bucket,
			Prefix:   prefix,
			Client:   client,
			Uploader: uploader,
			PartSize: bc.PartSize,
			Timeout:  timeout,
		}
		slog.Debug("configured backend", "name", bc.Name, "bucket", bc.Bucket, "prefix", prefix, "path_style", bc.UsePathStyle || bc.PathStyleLegacy)
	}
	return vfs, nil
}

// Filter returns a new VFS containing only the named backends. An empty names
// slice returns the original VFS unchanged.
func (vfs *VFS) Filter(names []string) *VFS {
	if len(names) == 0 {
		return vfs
	}
	filtered := make(map[string]*Backend, len(names))
	for _, name := range names {
		if b, ok := vfs.Backends[name]; ok {
			filtered[name] = b
		}
	}
	return &VFS{Backends: filtered}
}

// WithUserPrefix returns a new VFS where each backend's Prefix is extended by
// userPrefix. This implements per-user chroot without exposing the prefix to
// the user.
func (vfs *VFS) WithUserPrefix(userPrefix string) *VFS {
	if userPrefix == "" {
		return vfs
	}
	userPrefix = strings.Trim(userPrefix, "/")
	if userPrefix == "" {
		return vfs
	}
	return vfs.WithBackendPrefixes(map[string]string{"*": userPrefix})
}

// WithBackendPrefixes returns a new VFS where each backend's Prefix is
// extended by the per-backend prefix from prefixes. A "*" entry is used as a
// default for backends not explicitly listed. This lets a user be chrooted to
// different prefixes on different backends.
func (vfs *VFS) WithBackendPrefixes(prefixes map[string]string) *VFS {
	if len(prefixes) == 0 {
		return vfs
	}
	filtered := make(map[string]*Backend, len(vfs.Backends))
	for name, b := range vfs.Backends {
		p, ok := prefixes[name]
		if !ok {
			p = prefixes["*"]
		}
		if p == "" {
			filtered[name] = b
			continue
		}
		p = strings.Trim(p, "/")
		if p == "" {
			filtered[name] = b
			continue
		}
		nb := *b
		nb.Prefix = b.Prefix + p + "/"
		filtered[name] = &nb
	}
	return &VFS{Backends: filtered}
}

// Validate checks that every backend is reachable by issuing a single
// ListObjectsV2 request under its prefix.
func (vfs *VFS) Validate(ctx context.Context) error {
	for name, b := range vfs.Backends {
		ctx, cancel := context.WithTimeout(ctx, b.Timeout)
		_, err := b.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:  aws.String(b.Bucket),
			Prefix:  aws.String(b.Prefix),
			MaxKeys: aws.Int32(1),
		})
		cancel()
		if err != nil {
			return fmt.Errorf("backend %q (bucket=%q): %w", name, b.Bucket, err)
		}
		slog.Info("backend validated", "name", name, "bucket", b.Bucket, "prefix", b.Prefix)
	}
	return nil
}

// Resolve maps an SFTP path to a backend and a key relative to that backend.
// An empty path resolves to the virtual root.
func (vfs *VFS) Resolve(p string) (*Backend, string, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil, "", nil
	}
	parts := strings.SplitN(p, "/", 2)
	b, ok := vfs.Backends[parts[0]]
	if !ok {
		return nil, "", os.ErrNotExist
	}
	if len(parts) == 1 {
		return b, "", nil
	}
	return b, parts[1], nil
}
