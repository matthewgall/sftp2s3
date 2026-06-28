package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

// keysEqual reports whether two ssh.PublicKeys are the same.
func keysEqual(a, b ssh.PublicKey) bool {
	return bytes.Equal(a.Marshal(), b.Marshal())
}

// loadAuthorizedKeys loads public keys from a list of sources. Each source is
// either a literal authorized_keys line or a path to a file containing one or
// more keys.
func loadAuthorizedKeys(sources []string) ([]ssh.PublicKey, error) {
	var keys []ssh.PublicKey
	for _, src := range sources {
		var data []byte
		var err error
		if isLiteralKey(src) {
			data = []byte(src)
		} else {
			data, err = os.ReadFile(src)
			if err != nil {
				return nil, fmt.Errorf("read authorized keys %q: %w", src, err)
			}
		}
		parsed, err := parseAuthorizedKeys(data)
		if err != nil {
			return nil, fmt.Errorf("parse authorized keys %q: %w", src, err)
		}
		keys = append(keys, parsed...)
	}
	return keys, nil
}

func isLiteralKey(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "ssh-") ||
		strings.HasPrefix(s, "ecdsa-sha2-") ||
		strings.HasPrefix(s, "sk-") ||
		strings.HasPrefix(s, "ssh-ed25519")
}

func parseAuthorizedKeys(data []byte) ([]ssh.PublicKey, error) {
	var keys []ssh.PublicKey
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, err
		}
		keys = append(keys, pub)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

// hasAnyPassword reports whether any user has a non-empty password configured.
func hasAnyPassword(users []UserConfig) bool {
	for _, u := range users {
		if u.Password != "" {
			return true
		}
	}
	return false
}
