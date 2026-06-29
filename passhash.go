package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// passwordMatches reports whether pass satisfies the configured credential for
// u. If a bcrypt password_hash is set it is used exclusively; otherwise a
// plaintext password comparison is used.
func passwordMatches(u UserConfig, pass string) bool {
	if u.PasswordHash != "" {
		err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pass))
		slog.Debug("bcrypt compare", "user", u.Username, "hash_len", len(u.PasswordHash), "password_len", len(pass), "match", err == nil)
		return err == nil
	}
	return u.Password != "" && u.Password == pass
}

// runHashPassword reads a password from stdin and prints a bcrypt hash.
func runHashPassword() error {
	fmt.Fprint(os.Stderr, "Enter password: ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	password := strings.TrimSpace(line)
	if password == "" {
		return fmt.Errorf("password cannot be empty")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	fmt.Println(string(hash))
	return nil
}

// runVerifyPassword reads a password from stdin and checks it against the
// supplied bcrypt hash, printing "match" or "mismatch".
func runVerifyPassword(hash string) error {
	fmt.Fprint(os.Stderr, "Enter password: ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	password := strings.TrimSpace(line)
	if password == "" {
		return fmt.Errorf("password cannot be empty")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		fmt.Println("mismatch")
		return nil
	}
	fmt.Println("match")
	return nil
}
