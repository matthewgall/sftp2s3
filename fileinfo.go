package main

import (
	"io"
	"os"
	"time"

	"github.com/pkg/sftp"
)

// fileInfo implements os.FileInfo for entries returned to SFTP clients.
type fileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) Mode() os.FileMode  { return fi.mode }
func (fi *fileInfo) ModTime() time.Time { return fi.modTime }
func (fi *fileInfo) IsDir() bool        { return fi.mode.IsDir() }
func (fi *fileInfo) Sys() any           { return nil }

// newDirInfo creates an os.FileInfo for a directory entry.
func newDirInfo(name string) *fileInfo {
	return &fileInfo{
		name:    name,
		size:    4096,
		mode:    os.ModeDir | 0o755,
		modTime: time.Now(),
	}
}

// newFileInfo creates an os.FileInfo for a regular file entry.
func newFileInfo(name string, size int64, modTime time.Time) *fileInfo {
	if modTime.IsZero() {
		modTime = time.Now()
	}
	return &fileInfo{
		name:    name,
		size:    size,
		mode:    0o644,
		modTime: modTime,
	}
}

// listerAt wraps a slice of os.FileInfo to implement sftp.ListerAt.
type listerAt []os.FileInfo

// ListAt copies entries from the internal slice into list starting at offset.
func (l listerAt) ListAt(list []os.FileInfo, offset int64) (int, error) {
	n := 0
	for offset < int64(len(l)) && n < len(list) {
		list[n] = l[offset]
		n++
		offset++
	}
	if n < len(list) {
		return n, io.EOF
	}
	return n, nil
}

var _ sftp.ListerAt = listerAt(nil)
