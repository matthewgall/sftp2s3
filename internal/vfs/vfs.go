// Package vfs maps SFTP paths to one or more S3 backends.
package vfs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/matthewgall/sftp2s3/internal/config"
	"github.com/matthewgall/sftp2s3/internal/storage"
)

// VFS maps backend names to S3 backends and resolves SFTP paths.
type VFS struct {
	Backends map[string]*storage.Backend
}

// NewVFS builds a VFS from configuration, creating an S3 backend for each
// entry.
func NewVFS(configs []config.BackendConfig) (*VFS, error) {
	ctx := context.Background()
	vfs := &VFS{Backends: make(map[string]*storage.Backend, len(configs))}

	for _, bc := range configs {
		b, err := storage.NewBackend(ctx, bc)
		if err != nil {
			return nil, err
		}
		vfs.Backends[bc.Name] = b
		slog.Debug("configured backend", "name", bc.Name, "bucket", bc.Bucket, "prefix", b.Prefix, "path_style", bc.UsePathStyle || bc.PathStyleLegacy)
	}
	return vfs, nil
}

// Filter returns a new VFS containing only the named backends. An empty names
// slice returns the original VFS unchanged.
func (vfs *VFS) Filter(names []string) *VFS {
	if len(names) == 0 {
		return vfs
	}
	filtered := make(map[string]*storage.Backend, len(names))
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
	filtered := make(map[string]*storage.Backend, len(vfs.Backends))
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
func (vfs *VFS) Resolve(p string) (*storage.Backend, string, error) {
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
