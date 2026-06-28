package main

import (
	"strings"

	"github.com/pkg/sftp"
)

// Permission names used in user configuration.
const (
	PermRead   = "read"
	PermWrite  = "write"
	PermDelete = "delete"
)

// UserPermissions is the parsed, boolean view of a user's capabilities.
type UserPermissions struct {
	Read   bool
	Write  bool
	Delete bool
}

// allPermissions returns a UserPermissions with every flag enabled.
func allPermissions() UserPermissions {
	return UserPermissions{Read: true, Write: true, Delete: true}
}

// parsePermissions converts a list of permission strings into UserPermissions.
// Unknown values are ignored. If no recognised permission is supplied, the
// user gets full access (backwards compatible default).
func parsePermissions(list []string) UserPermissions {
	var p UserPermissions
	for _, s := range list {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case PermRead:
			p.Read = true
		case PermWrite:
			p.Write = true
		case PermDelete:
			p.Delete = true
		}
	}
	if !p.Read && !p.Write && !p.Delete {
		return allPermissions()
	}
	return p
}

// userPermissions returns the parsed permissions for username from cfg.
func userPermissions(cfg *Config, username string) UserPermissions {
	for _, u := range cfg.Users {
		if u.Username == username {
			return parsePermissions(u.Permissions)
		}
	}
	return allPermissions()
}

func (h *S3Handlers) requireRead() error {
	if !h.perms.Read {
		return sftp.ErrSSHFxPermissionDenied
	}
	return nil
}

func (h *S3Handlers) requireWrite() error {
	if !h.perms.Write {
		return sftp.ErrSSHFxPermissionDenied
	}
	return nil
}

func (h *S3Handlers) requireDelete() error {
	if !h.perms.Delete {
		return sftp.ErrSSHFxPermissionDenied
	}
	return nil
}
