package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// AuthFailuresConfig configures the per-IP auth failure tarpit. Empty fields
// use sensible defaults.
type AuthFailuresConfig struct {
	MaxAttempts   int    `yaml:"max_attempts"`
	Window        string `yaml:"window"`
	BlockDuration string `yaml:"block_duration"`
	TarpitDelay   string `yaml:"tarpit_delay"`
	StateFile     string `yaml:"state_file"`
	SaveInterval  string `yaml:"save_interval"`
}

type authState struct {
	Attempts map[string][]time.Time `json:"attempts"`
	Blocked  map[string]time.Time   `json:"blocked"`
}

type authFailureTracker struct {
	mu            sync.Mutex
	attempts      map[string][]time.Time
	blocked       map[string]time.Time
	maxAttempts   int
	window        time.Duration
	blockDuration time.Duration
	tarpitDelay   time.Duration
	stateFile     string
	saveInterval  time.Duration
}

// newAuthFailureTracker creates a tracker from cfg, loading any persisted state
// from StateFile when configured.
func newAuthFailureTracker(cfg AuthFailuresConfig) (*authFailureTracker, error) {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}

	window, err := parseDurationDefault(cfg.Window, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("invalid auth failure window: %w", err)
	}
	blockDuration, err := parseDurationDefault(cfg.BlockDuration, 15*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("invalid auth failure block_duration: %w", err)
	}
	tarpitDelay, err := parseDurationDefault(cfg.TarpitDelay, 0)
	if err != nil {
		return nil, fmt.Errorf("invalid auth failure tarpit_delay: %w", err)
	}
	saveInterval, err := parseDurationDefault(cfg.SaveInterval, time.Minute)
	if err != nil {
		return nil, fmt.Errorf("invalid auth failure save_interval: %w", err)
	}

	t := &authFailureTracker{
		attempts:      make(map[string][]time.Time),
		blocked:       make(map[string]time.Time),
		maxAttempts:   cfg.MaxAttempts,
		window:        window,
		blockDuration: blockDuration,
		tarpitDelay:   tarpitDelay,
		stateFile:     cfg.StateFile,
		saveInterval:  saveInterval,
	}

	if t.stateFile != "" {
		if err := t.load(); err != nil {
			slog.Warn("failed to load auth failure state", "file", t.stateFile, "error", err)
		}
	}

	return t, nil
}

func parseDurationDefault(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("duration must be non-negative")
	}
	return d, nil
}

// Start begins periodic state saves. It performs a final save when ctx is done.
func (t *authFailureTracker) Start(ctx context.Context) {
	if t.stateFile == "" || t.saveInterval <= 0 {
		return
	}
	ticker := time.NewTicker(t.saveInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := t.save(); err != nil {
					slog.Warn("failed to save auth failure state", "error", err)
				}
			case <-ctx.Done():
				if err := t.save(); err != nil {
					slog.Warn("failed to save auth failure state on shutdown", "error", err)
				}
				return
			}
		}
	}()
}

// isBlocked reports whether remote is currently tarpitted.
func (t *authFailureTracker) isBlocked(remote string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if until, ok := t.blocked[remote]; ok && time.Now().Before(until) {
		return true
	}
	delete(t.blocked, remote)
	return false
}

// record increments the failure count for remote and returns true if the IP
// has just crossed the threshold and is now blocked.
func (t *authFailureTracker) record(remote string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.pruneAttemptsLocked(remote, now)
	t.attempts[remote] = append(t.attempts[remote], now)

	if len(t.attempts[remote]) >= t.maxAttempts {
		t.blocked[remote] = now.Add(t.blockDuration)
		delete(t.attempts, remote)
		return true
	}
	return false
}

// tarpit sleeps for the configured delay when remote is currently blocked.
func (t *authFailureTracker) tarpit(remote string) {
	if t.tarpitDelay <= 0 {
		return
	}
	if t.isBlocked(remote) {
		time.Sleep(t.tarpitDelay)
	}
}

func (t *authFailureTracker) pruneAttemptsLocked(remote string, now time.Time) {
	cutoff := now.Add(-t.window)
	kept := t.attempts[remote][:0]
	for _, when := range t.attempts[remote] {
		if when.After(cutoff) {
			kept = append(kept, when)
		}
	}
	if len(kept) == 0 {
		delete(t.attempts, remote)
	} else {
		t.attempts[remote] = kept
	}
}

func (t *authFailureTracker) load() error {
	f, err := os.Open(t.stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	var s authState
	if err := json.NewDecoder(f).Decode(&s); err != nil {
		return err
	}

	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()

	for remote, attempts := range s.Attempts {
		var valid []time.Time
		for _, when := range attempts {
			if now.Sub(when) <= t.window {
				valid = append(valid, when)
			}
		}
		if len(valid) > 0 {
			t.attempts[remote] = valid
		}
	}
	for remote, until := range s.Blocked {
		if now.Before(until) {
			t.blocked[remote] = until
		}
	}
	return nil
}

func (t *authFailureTracker) save() error {
	if t.stateFile == "" {
		return nil
	}

	t.mu.Lock()
	now := time.Now()
	for remote := range t.attempts {
		t.pruneAttemptsLocked(remote, now)
	}
	for remote, until := range t.blocked {
		if !now.Before(until) {
			delete(t.blocked, remote)
		}
	}
	s := authState{
		Attempts: t.attempts,
		Blocked:  t.blocked,
	}
	t.mu.Unlock()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.stateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, t.stateFile)
}
