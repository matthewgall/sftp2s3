package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestBackendHealthMonitor(t *testing.T) {
	b1 := newMockBackend(t, "bucket1", "", map[string][]byte{"file.bin": make([]byte, 10)})
	b1.Name = "primary"
	b2 := newFailingBackend(t, "bucket2", "")
	b2.Name = "failing"

	state := &serverState{
		vfs: &VFS{Backends: map[string]*Backend{b1.Name: b1, b2.Name: b2}},
	}
	currentState := &atomic.Pointer[serverState]{}
	currentState.Store(state)

	metrics := NewMetrics(prometheus.NewRegistry())
	monitor := newBackendHealthMonitor(currentState, metrics, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go monitor.Start(ctx)

	// Wait for at least one check cycle.
	time.Sleep(150 * time.Millisecond)

	if !monitor.Healthy("primary") {
		t.Fatal("expected primary backend to be healthy")
	}
	if monitor.Healthy("failing") {
		t.Fatal("expected failing backend to be unhealthy")
	}
	if monitor.AllHealthy() {
		t.Fatal("expected AllHealthy=false when one backend is down")
	}
}

func TestBackendHealthMonitorStateSwap(t *testing.T) {
	b1 := newMockBackend(t, "bucket", "", map[string][]byte{"file.bin": make([]byte, 10)})
	b1.Name = "primary"

	state := &serverState{vfs: &VFS{Backends: map[string]*Backend{b1.Name: b1}}}
	currentState := &atomic.Pointer[serverState]{}
	currentState.Store(state)

	monitor := newBackendHealthMonitor(currentState, nil, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go monitor.Start(ctx)

	time.Sleep(100 * time.Millisecond)
	if !monitor.AllHealthy() {
		t.Fatal("expected all healthy with one good backend")
	}

	// Swap in a failing backend.
	b2 := newFailingBackend(t, "bucket", "")
	b2.Name = "primary"
	currentState.Store(&serverState{vfs: &VFS{Backends: map[string]*Backend{b2.Name: b2}}})

	time.Sleep(100 * time.Millisecond)
	if monitor.AllHealthy() {
		t.Fatal("expected all healthy=false after state swap to failing backend")
	}
}

func TestBackendHealthMonitorDisabled(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	state := &serverState{vfs: &VFS{Backends: map[string]*Backend{b.Name: b}}}
	currentState := &atomic.Pointer[serverState]{}
	currentState.Store(state)

	monitor := newBackendHealthMonitor(currentState, nil, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Should return immediately and not panic.
	monitor.Start(ctx)

	if !monitor.AllHealthy() {
		t.Fatal("disabled monitor should report all healthy")
	}
}
