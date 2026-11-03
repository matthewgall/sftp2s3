package backend

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/matthewgall/sftp2s3/internal/metrics"
)

// VFSProvider returns the current VFS. It is typically implemented by the
// server layer so that health checks see the latest state after a SIGHUP reload.
type VFSProvider interface {
	CurrentVFS() *VFS
}

// HealthMonitor periodically checks that every configured backend is
// reachable, without requiring any SFTP user interaction.
type HealthMonitor struct {
	vfsProvider VFSProvider
	metrics     *metrics.Metrics
	interval    time.Duration

	mu     sync.RWMutex
	status map[string]bool
}

// NewHealthMonitor creates a monitor that reads the current VFS every
// interval. A zero or negative interval disables checks.
func NewHealthMonitor(vfsProvider VFSProvider, m *metrics.Metrics, interval time.Duration) *HealthMonitor {
	return &HealthMonitor{
		vfsProvider: vfsProvider,
		metrics:     m,
		interval:    interval,
		status:      make(map[string]bool),
	}
}

// Start runs health checks until ctx is done.
func (m *HealthMonitor) Start(ctx context.Context) {
	if m.interval <= 0 {
		return
	}

	// Run an immediate check on startup.
	m.checkAll()

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAll()
		}
	}
}

func (m *HealthMonitor) checkAll() {
	state := m.vfsProvider.CurrentVFS()
	if state == nil {
		return
	}

	newStatus := make(map[string]bool, len(state.Backends))
	for name, b := range state.Backends {
		healthy := m.checkBackend(b)
		newStatus[name] = healthy
		if m.metrics != nil {
			m.metrics.SetBackendHealthy(name, healthy)
		}
	}

	m.mu.Lock()
	m.status = newStatus
	m.mu.Unlock()
}

func (m *HealthMonitor) checkBackend(b *Backend) bool {
	ctx, cancel := context.WithTimeout(context.Background(), b.Timeout)
	defer cancel()

	start := time.Now()
	_, err := b.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(b.Bucket),
		Prefix:  aws.String(b.Prefix),
		MaxKeys: aws.Int32(1),
	})
	if m.metrics != nil {
		m.metrics.ObserveS3Op("HealthCheck", b.Name, start, err)
	}
	if err != nil {
		slog.Warn("backend health check failed", "backend", b.Name, "bucket", b.Bucket, "error", err)
		return false
	}
	slog.Debug("backend health check ok", "backend", b.Name, "bucket", b.Bucket)
	return true
}

// Healthy reports the last known health of a single backend.
func (m *HealthMonitor) Healthy(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status[name]
}

// AllHealthy reports whether every known backend was healthy at the last check.
// If no backends are configured, it returns true.
func (m *HealthMonitor) AllHealthy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.status) == 0 {
		return true
	}
	for _, healthy := range m.status {
		if !healthy {
			return false
		}
	}
	return true
}
