package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the Prometheus collectors exposed by sftp2s3.
type Metrics struct {
	activeConns    prometheus.Gauge
	totalConns     prometheus.Counter
	uploadBytes    prometheus.Counter
	downloadBytes  prometheus.Counter
	s3OpsTotal     *prometheus.CounterVec
	s3OpDuration   *prometheus.HistogramVec
	authFailures   prometheus.Counter
	backendHealthy *prometheus.GaugeVec
}

// NewMetrics creates and registers a Metrics instance with reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		activeConns: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "sftp2s3_connections_active",
			Help: "Number of currently connected SFTP clients.",
		}),
		totalConns: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "sftp2s3_connections_total",
			Help: "Total number of SFTP client connections accepted.",
		}),
		uploadBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "sftp2s3_upload_bytes_total",
			Help: "Total bytes uploaded through SFTP.",
		}),
		downloadBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "sftp2s3_download_bytes_total",
			Help: "Total bytes downloaded through SFTP.",
		}),
		s3OpsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sftp2s3_s3_operations_total",
			Help: "Total S3 operations by name, backend, and status.",
		}, []string{"operation", "backend", "status"}),
		s3OpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "sftp2s3_s3_operation_duration_seconds",
			Help:    "S3 operation duration distribution by operation and backend.",
			Buckets: prometheus.DefBuckets,
		}, []string{"operation", "backend"}),
		authFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "sftp2s3_auth_failures_total",
			Help: "Total failed authentication attempts.",
		}),
		backendHealthy: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "sftp2s3_backend_healthy",
			Help: "Whether each backend is currently healthy (1) or not (0).",
		}, []string{"backend"}),
	}

	if reg != nil {
		reg.MustRegister(
			m.activeConns,
			m.totalConns,
			m.uploadBytes,
			m.downloadBytes,
			m.s3OpsTotal,
			m.s3OpDuration,
			m.authFailures,
			m.backendHealthy,
		)
	}

	return m
}

// ConnectionOpened records a new SFTP connection.
func (m *Metrics) ConnectionOpened() {
	m.activeConns.Inc()
	m.totalConns.Inc()
}

// ConnectionClosed records that an SFTP connection has closed.
func (m *Metrics) ConnectionClosed() {
	m.activeConns.Dec()
}

// AddUploadBytes adds n bytes to the total upload counter.
func (m *Metrics) AddUploadBytes(n int64) {
	if n > 0 {
		m.uploadBytes.Add(float64(n))
	}
}

// AddDownloadBytes adds n bytes to the total download counter.
func (m *Metrics) AddDownloadBytes(n int64) {
	if n > 0 {
		m.downloadBytes.Add(float64(n))
	}
}

// IncAuthFailures increments the failed authentication counter.
func (m *Metrics) IncAuthFailures() {
	m.authFailures.Inc()
}

// SetBackendHealthy sets the backend health gauge for name.
func (m *Metrics) SetBackendHealthy(backend string, healthy bool) {
	v := float64(0)
	if healthy {
		v = 1
	}
	m.backendHealthy.WithLabelValues(backend).Set(v)
}

// ObserveS3Op records the result and duration of an S3 operation.
func (m *Metrics) ObserveS3Op(op, backend string, start time.Time, err error) {
	status := "success"
	if err != nil {
		status = "error"
	}
	m.s3OpsTotal.WithLabelValues(op, backend, status).Inc()
	m.s3OpDuration.WithLabelValues(op, backend).Observe(time.Since(start).Seconds())
}

// healthChecker is the subset of the backend health monitor used by the
// metrics HTTP server.
type healthChecker interface {
	AllHealthy() bool
}

// startMetricsServer starts an HTTP server on listener that exposes /metrics.
// It runs until ctx is done. If checker is non-nil, /healthz reflects backend
// health.
//
// If metricsToken is non-empty, requests to /metrics must include an
// Authorization: Bearer <token> header. If certFile and keyFile are provided,
// the server uses TLS.
func startMetricsServer(ctx context.Context, listener net.Listener, checker healthChecker, metricsToken, certFile, keyFile string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", metricsAuthHandler(metricsToken, promhttp.Handler()))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if checker != nil && !checker.AllHealthy() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unhealthy"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	server := &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("metrics server shutdown error", "error", err)
		}
	}()

	go func() {
		slog.Info("metrics server listening", "addr", listener.Addr(), "tls", certFile != "" && keyFile != "")
		var err error
		if certFile != "" && keyFile != "" {
			err = server.ServeTLS(listener, certFile, keyFile)
		} else {
			err = server.Serve(listener)
		}
		if err != nil && err != http.ErrServerClosed {
			slog.Error("metrics server failed", "error", err)
		}
	}()
}

// metricsAuthHandler returns an http.Handler that enforces an optional bearer
// token on the /metrics endpoint.
func metricsAuthHandler(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	expected := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expected {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
