package health

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matthewgall/sftp2s3/internal/metrics"
	"github.com/matthewgall/sftp2s3/internal/storage"
	"github.com/matthewgall/sftp2s3/internal/vfs"
	"github.com/prometheus/client_golang/prometheus"
)

type staticVFSProvider struct {
	v atomic.Pointer[vfs.VFS]
}

func (p *staticVFSProvider) CurrentVFS() *vfs.VFS {
	return p.v.Load()
}

func newTestVFSProvider(vfs *vfs.VFS) *staticVFSProvider {
	p := &staticVFSProvider{}
	p.v.Store(vfs)
	return p
}

func TestBackendHealthMonitor(t *testing.T) {
	b1 := newMockBackend(t, "bucket1", "", map[string][]byte{"file.bin": make([]byte, 10)})
	b1.Name = "primary"
	b2 := newFailingBackend(t, "bucket2", "")
	b2.Name = "failing"

	provider := newTestVFSProvider(&vfs.VFS{Backends: map[string]*storage.Backend{b1.Name: b1, b2.Name: b2}})
	m := metrics.NewMetrics(prometheus.NewRegistry())
	monitor := NewHealthMonitor(provider, m, 50*time.Millisecond)

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

	provider := newTestVFSProvider(&vfs.VFS{Backends: map[string]*storage.Backend{b1.Name: b1}})
	monitor := NewHealthMonitor(provider, nil, 50*time.Millisecond)
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
	provider.v.Store(&vfs.VFS{Backends: map[string]*storage.Backend{b2.Name: b2}})

	time.Sleep(100 * time.Millisecond)
	if monitor.AllHealthy() {
		t.Fatal("expected all healthy=false after state swap to failing backend")
	}
}

func TestBackendHealthMonitorDisabled(t *testing.T) {
	b := newMockBackend(t, "bucket", "", nil)
	provider := newTestVFSProvider(&vfs.VFS{Backends: map[string]*storage.Backend{b.Name: b}})
	monitor := NewHealthMonitor(provider, nil, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Should return immediately and not panic.
	monitor.Start(ctx)

	if !monitor.AllHealthy() {
		t.Fatal("disabled monitor should report all healthy")
	}
}
