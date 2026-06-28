package main

import "path/filepath"

// cacheSubDir returns a feature-specific subdirectory of cacheDir.
// If cacheDir is empty it returns an empty string, which callers should treat
// as "do not cache on disk".
func cacheSubDir(cacheDir, feature string) string {
	if cacheDir == "" {
		return ""
	}
	return filepath.Join(cacheDir, feature)
}
