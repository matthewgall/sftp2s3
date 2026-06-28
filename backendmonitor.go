package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// backendHealthMonitor periodically checks that every configured backend is
// reachable, without requiring any SFTP user interaction.
type backendHealthMonitor struct {
	currentState *atomic.Pointer[serverState]
	metrics      *Metrics
	interval     time.Duration

	mu     sync.RWMutex
	status map[string]bool
}

// newBackendHealthMonitor creates a monitor that reads the current serverState
// every interval. A zero or negative interval disables checks.
func newBackendHealthMonitor(currentState *atomic.Pointer[serverState], metrics *Metrics, interval time.Duration) *backendHealthMonitor {
	return &backendHealthMonitor{
		currentState: currentState,
		metrics:      metrics,
		interval:     interval,
		status:       make(map[string]bool),
	}
}

// Start runs health checks until ctx is done.
func (m *backendHealthMonitor) Start(ctx context.Context) {
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

func (m *backendHealthMonitor) checkAll() {
	state := m.currentState.Load()
	if state == nil || state.vfs == nil {
		return
	}

	newStatus := make(map[string]bool, len(state.vfs.Backends))
	for name, b := range state.vfs.Backends {
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

func (m *backendHealthMonitor) checkBackend(b *Backend) bool {
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
func (m *backendHealthMonitor) Healthy(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status[name]
}

// AllHealthy reports whether every known backend was healthy at the last check.
// If no backends are configured, it returns true.
func (m *backendHealthMonitor) AllHealthy() bool {
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
