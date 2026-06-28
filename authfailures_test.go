package main

import (
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
