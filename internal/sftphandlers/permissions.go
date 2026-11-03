package sftphandlers

import (
	"strings"

	"github.com/matthewgall/sftp2s3/internal/config"
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

// AllPermissions returns a UserPermissions with every flag enabled.
func AllPermissions() UserPermissions {
	return UserPermissions{Read: true, Write: true, Delete: true}
}

// ParsePermissions converts a list of permission strings into UserPermissions.
// Unknown values are ignored. If no recognised permission is supplied, the
// user gets full access (backwards compatible default).
func ParsePermissions(list []string) UserPermissions {
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
		return AllPermissions()
	}
	return p
}

// GetUserPermissions returns the parsed permissions for username from cfg.
func GetUserPermissions(cfg *config.Config, username string) UserPermissions {
	for _, u := range cfg.Users {
		if u.Username == username {
			return ParsePermissions(u.Permissions)
		}
	}
	return AllPermissions()
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
