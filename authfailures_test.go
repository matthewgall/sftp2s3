package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthFailureTrackerBlock(t *testing.T) {
	tracker, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   3,
		Window:        "1m",
		BlockDuration: "1m",
	})
	if err != nil {
		t.Fatal(err)
	}

	remote := "1.2.3.4"
	if tracker.isBlocked(remote) {
		t.Fatal("should not be blocked initially")
	}

	tracker.record(remote)
	tracker.record(remote)
	if tracker.isBlocked(remote) {
		t.Fatal("should not be blocked before threshold")
	}

	tracker.record(remote)
	if !tracker.isBlocked(remote) {
		t.Fatal("should be blocked after threshold")
	}
}

func TestAuthFailureTrackerWindowExpiry(t *testing.T) {
	tracker, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   2,
		Window:        "50ms",
		BlockDuration: "1m",
	})
	if err != nil {
		t.Fatal(err)
	}

	remote := "1.2.3.4"
	tracker.record(remote)
	time.Sleep(60 * time.Millisecond)
	tracker.record(remote)

	if tracker.isBlocked(remote) {
		t.Fatal("old attempt should have expired")
	}
}

func TestAuthFailureTrackerSaveLoad(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")

	tracker, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   2,
		Window:        "1m",
		BlockDuration: "1h",
		StateFile:     stateFile,
	})
	if err != nil {
		t.Fatal(err)
	}

	remote := "1.2.3.4"
	tracker.record(remote)
	tracker.record(remote)
	if !tracker.isBlocked(remote) {
		t.Fatal("expected blocked before save")
	}

	if err := tracker.save(); err != nil {
		t.Fatal(err)
	}

	tracker2, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   2,
		Window:        "1m",
		BlockDuration: "1h",
		StateFile:     stateFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tracker2.isBlocked(remote) {
		t.Fatal("expected blocked after load")
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("state file is empty")
	}
}

func TestAuthFailureTrackerStartSavesOnShutdown(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")

	tracker, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   2,
		Window:        "1m",
		BlockDuration: "1h",
		StateFile:     stateFile,
		SaveInterval:  "1h", // long interval so shutdown triggers the save
	})
	if err != nil {
		t.Fatal(err)
	}

	remote := "1.2.3.4"
	tracker.record(remote)
	tracker.record(remote)

	ctx, cancel := context.WithCancel(context.Background())
	tracker.Start(ctx)
	cancel()
	time.Sleep(50 * time.Millisecond) // let goroutine finish

	tracker2, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   2,
		Window:        "1m",
		BlockDuration: "1h",
		StateFile:     stateFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tracker2.isBlocked(remote) {
		t.Fatal("expected state to be saved on shutdown")
	}
}

func TestAuthFailureTrackerTarpit(t *testing.T) {
	tracker, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   2,
		Window:        "1m",
		BlockDuration: "1h",
		TarpitDelay:   "100ms",
	})
	if err != nil {
		t.Fatal(err)
	}

	remote := "1.2.3.4"
	tracker.record(remote)
	tracker.record(remote)

	start := time.Now()
	tracker.tarpit(remote)
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("expected tarpit delay")
	}
}

func TestAuthFailureTrackerLoadPrunesExpiredAttempts(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")
	old := time.Now().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	data := `{"attempts":{"1.2.3.4":["` + old + `"]},"blocked":{}}`
	if err := os.WriteFile(stateFile, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	tracker, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   2,
		Window:        "1m",
		BlockDuration: "1h",
		StateFile:     stateFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.isBlocked("1.2.3.4") {
		t.Fatal("expected expired attempt to be pruned")
	}
}

func TestAuthFailureTrackerLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")
	if err := os.WriteFile(stateFile, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   2,
		Window:        "1m",
		BlockDuration: "1h",
		StateFile:     stateFile,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthFailureTrackerSavePrunesExpired(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")

	tracker, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   2,
		Window:        "1m",
		BlockDuration: "1ms",
		StateFile:     stateFile,
	})
	if err != nil {
		t.Fatal(err)
	}

	remote := "1.2.3.4"
	tracker.record(remote)
	tracker.record(remote)
	if !tracker.isBlocked(remote) {
		t.Fatal("expected blocked")
	}

	time.Sleep(5 * time.Millisecond)
	if err := tracker.save(); err != nil {
		t.Fatal(err)
	}

	tracker2, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   2,
		Window:        "1m",
		BlockDuration: "1ms",
		StateFile:     stateFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tracker2.isBlocked(remote) {
		t.Fatal("expected expired block to be pruned on save")
	}
}

func TestAuthFailureTrackerStartNoOp(t *testing.T) {
	tracker, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:  2,
		Window:       "1m",
		StateFile:    t.TempDir(), // directory path, but interval is 0 so Start returns early
		SaveInterval: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tracker.Start(ctx) // should return immediately without starting a goroutine
}

func TestAuthFailureTrackerSaveError(t *testing.T) {
	dir := t.TempDir()
	tracker, err := newAuthFailureTracker(AuthFailuresConfig{
		MaxAttempts:   2,
		Window:        "1m",
		BlockDuration: "1h",
		StateFile:     dir, // directory path causes write failure
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tracker.save(); err == nil {
		t.Fatal("expected save error for directory path")
	}
}
