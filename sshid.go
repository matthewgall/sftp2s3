package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	sshidUserAgent = "curl/8.14.1"
	sshidCacheTTL  = 1 * time.Hour
)

var sshidBaseURL = "https://sshid.io"

// sshidAlgorithmPrefixes maps friendly algorithm names to the corresponding
// authorized_keys key-type prefixes. It is only used when a user explicitly
// limits which key types to accept.
var sshidAlgorithmPrefixes = map[string][]string{
	"ed25519": {"ssh-ed25519"},
	"rsa":     {"ssh-rsa"},
	"ecdsa":   {"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521"},
}

// sshidResolver fetches and caches sshid.io public keys for a username.
type sshidResolver struct {
	client   *http.Client
	cacheDir string
	ttl      time.Duration
}

// newSSHIDResolver creates a resolver that stores cached keys under
// cacheDir/sshid. If cacheDir is empty, keys are fetched but not cached on
// disk. The sshid subdirectory avoids collisions with other features that may
// use the shared cache_dir in the future.
func newSSHIDResolver(cacheDir string) *sshidResolver {
	return &sshidResolver{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		cacheDir: cacheSubDir(cacheDir, "sshid"),
		ttl:      sshidCacheTTL,
	}
}

// Resolve returns the authorized-key lines for the configured sshid user.
// It fetches the single https://sshid.io/<username> endpoint (which returns
// all key types), caches the response, and falls back to a cached copy when
// the remote endpoint is unreachable.
func (r *sshidResolver) Resolve(cfg *SSHIDConfig) ([]string, error) {
	if cfg == nil || cfg.Username == "" {
		return nil, nil
	}

	cachePath := ""
	if r.cacheDir != "" {
		cachePath = filepath.Join(r.cacheDir, fmt.Sprintf("sshid-%s.keys", safeFileName(cfg.Username)))
		if data, ok := r.readCache(cachePath); ok {
			return r.filterKeys(data, cfg.Algorithms), nil
		}
	}

	url := fmt.Sprintf("%s/%s", sshidBaseURL, cfg.Username)
	data, err := r.fetch(url)
	if err != nil {
		if cachePath != "" {
			if cached, ok := r.readCacheStale(cachePath); ok {
				slog.Warn("sshid fetch failed, using stale cache", "url", url, "error", err)
				return r.filterKeys(cached, cfg.Algorithms), nil
			}
		}
		return nil, err
	}

	if cachePath != "" {
		if err := r.writeCache(cachePath, data); err != nil {
			slog.Warn("failed to write sshid cache", "path", cachePath, "error", err)
		}
	}
	return r.filterKeys(data, cfg.Algorithms), nil
}

func (r *sshidResolver) fetch(url string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", sshidUserAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (r *sshidResolver) filterKeys(data string, algorithms []string) []string {
	var keys []string
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !isLiteralKey(line) {
			continue
		}
		if len(algorithms) > 0 && !sshidKeyMatchesAlgorithms(line, algorithms) {
			continue
		}
		keys = append(keys, line)
	}
	return keys
}

func sshidKeyMatchesAlgorithms(line string, algorithms []string) bool {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false
	}
	keyType := parts[0]
	for _, alg := range algorithms {
		for _, prefix := range sshidAlgorithmPrefixes[strings.ToLower(alg)] {
			if keyType == prefix {
				return true
			}
		}
	}
	return false
}

func (r *sshidResolver) readCache(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > r.ttl {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func (r *sshidResolver) readCacheStale(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func (r *sshidResolver) writeCache(path, data string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func safeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// resolveSSHIDKeys fetches sshid.io keys for every user that configures them
// and appends the resulting authorized-key lines to that user's
// AuthorizedKeys list.
func resolveSSHIDKeys(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	resolver := newSSHIDResolver(cfg.Server.CacheDir)
	for i := range cfg.Users {
		if cfg.Users[i].SSHID == nil || cfg.Users[i].SSHID.Username == "" {
			continue
		}
		keys, err := resolver.Resolve(cfg.Users[i].SSHID)
		if err != nil {
			return fmt.Errorf("user %q sshid: %w", cfg.Users[i].Username, err)
		}
		cfg.Users[i].AuthorizedKeys = append(cfg.Users[i].AuthorizedKeys, keys...)
	}
	return nil
}
