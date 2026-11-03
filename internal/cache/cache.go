// Package cache provides helpers for on-disk feature caches used by sftp2s3.
package cache

import "path/filepath"

// SubDir returns a feature-specific subdirectory of cacheDir.
// If cacheDir is empty it returns an empty string, which callers should treat
// as "do not cache on disk".
func SubDir(cacheDir, feature string) string {
	if cacheDir == "" {
		return ""
	}
	return filepath.Join(cacheDir, feature)
}
