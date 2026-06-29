package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsConnections(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	m.ConnectionOpened()
	m.ConnectionOpened()
	m.ConnectionClosed()

	if got := testutil.ToFloat64(m.activeConns); got != 1 {
		t.Fatalf("activeConns=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.totalConns); got != 2 {
		t.Fatalf("totalConns=%v, want 2", got)
	}
}

func TestMetricsBytes(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	m.AddUploadBytes(100)
	m.AddDownloadBytes(200)

	if got := testutil.ToFloat64(m.uploadBytes); got != 100 {
		t.Fatalf("uploadBytes=%v, want 100", got)
	}
	if got := testutil.ToFloat64(m.downloadBytes); got != 200 {
		t.Fatalf("downloadBytes=%v, want 200", got)
	}
}

func TestMetricsS3Op(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	m.ObserveS3Op("ListObjectsV2", "primary", time.Now(), nil)
	m.ObserveS3Op("ListObjectsV2", "primary", time.Now(), errors.New("boom"))

	if got := testutil.ToFloat64(m.s3OpsTotal.WithLabelValues("ListObjectsV2", "primary", "success")); got != 1 {
		t.Fatalf("success count=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.s3OpsTotal.WithLabelValues("ListObjectsV2", "primary", "error")); got != 1 {
		t.Fatalf("error count=%v, want 1", got)
	}
}

func TestMetricsAuthFailures(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	m.IncAuthFailures()
	m.IncAuthFailures()

	if got := testutil.ToFloat64(m.authFailures); got != 2 {
		t.Fatalf("authFailures=%v, want 2", got)
	}
}

type fakeHealthChecker struct{ healthy bool }

func (f *fakeHealthChecker) AllHealthy() bool { return f.healthy }

func TestMetricsBackendHealth(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	m.SetBackendHealthy("primary", true)
	m.SetBackendHealthy("replica", false)

	if got := testutil.ToFloat64(m.backendHealthy.WithLabelValues("primary")); got != 1 {
		t.Fatalf("primary healthy=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.backendHealthy.WithLabelValues("replica")); got != 0 {
		t.Fatalf("replica healthy=%v, want 0", got)
	}
}

func TestStartMetricsServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	checker := &fakeHealthChecker{healthy: true}
	startMetricsServer(ctx, listener, checker, "", "", "")

	// Wait for the server to be ready.
	baseURL := "http://" + listener.Addr().String()
	var healthy bool
	for i := 0; i < 50; i++ {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			healthy = true
			break
		}
		if err == nil {
			resp.Body.Close()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !healthy {
		t.Fatal("metrics server did not become healthy")
	}

	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("get /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status=%d", resp.StatusCode)
	}

	checker.healthy = false
	resp, err = http.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("get /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/healthz status=%d, want 503", resp.StatusCode)
	}

	cancel()
}

func TestMetricsServerAuth(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startMetricsServer(ctx, listener, &fakeHealthChecker{healthy: true}, "secrettoken", "", "")

	baseURL := "http://" + listener.Addr().String()
	var ready bool
	for i := 0; i < 50; i++ {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			ready = true
			break
		}
		if err == nil {
			resp.Body.Close()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("metrics server did not become ready")
	}

	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, baseURL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer secrettoken")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with token, got %d", resp.StatusCode)
	}
}
