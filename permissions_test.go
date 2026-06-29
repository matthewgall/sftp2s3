package main

import (
	"context"
	"testing"

	"github.com/pkg/sftp"
)

func TestParsePermissionsDefaults(t *testing.T) {
	p := parsePermissions(nil)
	if !p.Read || !p.Write || !p.Delete {
		t.Fatalf("expected all permissions by default, got %+v", p)
	}
}

func TestParsePermissionsExplicit(t *testing.T) {
	p := parsePermissions([]string{"read", "delete"})
	if !p.Read || p.Write || !p.Delete {
		t.Fatalf("expected read+delete, got %+v", p)
	}
}

func TestParsePermissionsUnknownValues(t *testing.T) {
	p := parsePermissions([]string{"read", "admin", "superuser"})
	if !p.Read || p.Write || p.Delete {
		t.Fatalf("expected full defaults when only unknown values given, got %+v", p)
	}
}

func TestUserPermissionsLookup(t *testing.T) {
	cfg := &Config{
		Users: []UserConfig{
			{Username: "alice", Permissions: []string{"read"}},
			{Username: "bob"},
		},
	}
	alice := userPermissions(cfg, "alice")
	if !alice.Read || alice.Write || alice.Delete {
		t.Fatalf("alice perms=%+v", alice)
	}
	bob := userPermissions(cfg, "bob")
	if !bob.Read || !bob.Write || !bob.Delete {
		t.Fatalf("bob perms=%+v", bob)
	}
	unknown := userPermissions(cfg, "charlie")
	if !unknown.Read || !unknown.Write || !unknown.Delete {
		t.Fatalf("unknown user should get full perms, got %+v", unknown)
	}
}

func TestHandlersReadOnlyDenied(t *testing.T) {
	b := newMockBackend(t, "bucket", "", map[string][]byte{"file.bin": make([]byte, 10)})
	vfs := &VFS{Backends: map[string]*Backend{b.Name: b}}
	readOnly := parsePermissions([]string{"read"})

	hCmd := NewS3Handlers(context.Background(), vfs, "u", "127.0.0.1", readOnly, nil, 0, 0, "", nil).FileCmd.(*S3Handlers)
	if err := hCmd.Filecmd(&sftp.Request{Filepath: "/mock/file.bin", Method: "Remove"}); err != sftp.ErrSSHFxPermissionDenied {
		t.Fatalf("remove without delete: got %v", err)
	}
	if err := hCmd.Filecmd(&sftp.Request{Filepath: "/mock/dir", Method: "Mkdir"}); err != sftp.ErrSSHFxPermissionDenied {
		t.Fatalf("mkdir without write: got %v", err)
	}
	if err := hCmd.Filecmd(&sftp.Request{Filepath: "/mock/file.bin", Target: "/mock/file2.bin", Method: "Rename"}); err != sftp.ErrSSHFxPermissionDenied {
		t.Fatalf("rename without write/delete: got %v", err)
	}

	hPut := NewS3Handlers(context.Background(), vfs, "u", "127.0.0.1", readOnly, nil, 0, 0, "", nil).FilePut.(*S3Handlers)
	if _, err := hPut.Filewrite(&sftp.Request{Filepath: "/mock/file.bin", Method: "Put"}); err != sftp.ErrSSHFxPermissionDenied {
		t.Fatalf("write without write perm: got %v", err)
	}

	hList := NewS3Handlers(context.Background(), vfs, "u", "127.0.0.1", readOnly, nil, 0, 0, "", nil).FileList.(*S3Handlers)
	if _, err := hList.Filelist(&sftp.Request{Filepath: "/mock", Method: "List"}); err != nil {
		t.Fatalf("list with read perm should succeed: got %v", err)
	}
}

func TestHandlersWriteOnlyDenied(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	vfs := &VFS{Backends: map[string]*Backend{b.Name: b}}
	writeOnly := parsePermissions([]string{"write"})

	hList := NewS3Handlers(context.Background(), vfs, "u", "127.0.0.1", writeOnly, nil, 0, 0, "", nil).FileList.(*S3Handlers)
	if _, err := hList.Filelist(&sftp.Request{Filepath: "/mock", Method: "List"}); err != sftp.ErrSSHFxPermissionDenied {
		t.Fatalf("list without read: got %v", err)
	}
}
