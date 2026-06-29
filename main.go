// Package main implements sftp2s3, an SFTP server that proxies uploads,
// downloads, listings, and deletions to one or more S3-compatible backends.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// requireRecklessEnv returns an error if the named acknowledgement env var is
// not set. It is used to make enabling --insecure-log-passwords a deliberate
// two-step action.
func requireRecklessEnv(name string) error {
	if os.Getenv(name) == "" {
		return fmt.Errorf("set %s to acknowledge you are intentionally leaking passwords", name)
	}
	return nil
}

// initLogger creates a structured logger using the requested level and format.
func initLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, opts)
	default:
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(handler)
}

// buildServerState loads the host key, builds the VFS, validates backends, and
// creates the SSH server config.
func buildServerState(cfg *Config, metrics *Metrics) (*serverState, error) {
	signer, err := loadOrGenerateHostKey(cfg.Server.HostKey, cfg.Server.HostKeyType)
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}

	if err := resolveSSHIDKeys(cfg); err != nil {
		return nil, fmt.Errorf("sshid keys: %w", err)
	}

	vfs, err := NewVFS(cfg.Backends)
	if err != nil {
		return nil, fmt.Errorf("init vfs: %w", err)
	}

	if err := vfs.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("backend validation: %w", err)
	}

	tracker, err := newAuthFailureTracker(cfg.Server.AuthFailures)
	if err != nil {
		return nil, fmt.Errorf("auth failure tracker: %w", err)
	}

	sshCfg, err := newSSHServerConfig(cfg.Users, signer, tracker, metrics)
	if err != nil {
		return nil, fmt.Errorf("ssh config: %w", err)
	}

	return &serverState{
		cfg:     cfg,
		sshCfg:  sshCfg,
		vfs:     vfs,
		tracker: tracker,
		metrics: metrics,
	}, nil
}

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func main() {
	var configPath string
	var showVersion bool
	var hashPassword bool
	var hashPasswordOutput string
	var verifyPassword string
	var insecureLogPasswords bool
	flag.StringVar(&configPath, "c", "config.yaml", "path to config file")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.BoolVar(&hashPassword, "hash-password", false, "read a password from stdin and write a bcrypt hash to a file")
	flag.StringVar(&hashPasswordOutput, "hash-password-output", "", "output file for -hash-password (default sftp2s3.hash, use - for stdout)")
	flag.StringVar(&verifyPassword, "verify-password", "", "read a password from stdin and verify it against the provided bcrypt hash")
	flag.BoolVar(&insecureLogPasswords, "insecure-log-passwords", false, "INSECURE: log received plaintext passwords to stdout for debugging")
	flag.Parse()

	if insecureLogPasswords {
		const recklessEnv = "SFTP2S3_I_AM_RECKLESSLY_LEAKING_PASSWORDS"
		if err := requireRecklessEnv(recklessEnv); err != nil {
			slog.Error("refusing to start with --insecure-log-passwords", "error", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "WARNING: --insecure-log-passwords is enabled. Passwords will be printed in plaintext. Remove this flag immediately after debugging.\n")
		setInsecureLogPasswords(true)
	}

	if showVersion {
		fmt.Printf("sftp2s3 %s (commit %s, built %s)\n", Version, Commit, BuildDate)
		return
	}

	if hashPassword {
		if hashPasswordOutput == "" {
			hashPasswordOutput = "sftp2s3.hash"
		}
		if err := runHashPassword(hashPasswordOutput); err != nil {
			slog.Error("hash password failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if verifyPassword != "" {
		if err := runVerifyPassword(verifyPassword); err != nil {
			slog.Error("verify password failed", "error", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		slog.Error("load config failed", "error", err)
		os.Exit(1)
	}
	slog.Info("loaded config", "path", configPath, "users", len(cfg.Users), "backends", len(cfg.Backends))

	logger := initLogger(cfg.Server.LogLevel, cfg.Server.LogFormat)
	slog.SetDefault(logger)

	metrics := NewMetrics(prometheus.DefaultRegisterer)

	state, err := buildServerState(cfg, metrics)
	if err != nil {
		slog.Error("build server state failed", "error", err)
		os.Exit(1)
	}

	currentState := &atomic.Pointer[serverState]{}
	currentState.Store(state)

	connLimiter := newUserConnLimiter(cfg.Users)
	rateRegistry := newUserRateRegistry(cfg.Users)
	acceptLimiter := newAcceptRateLimiter(cfg.Server.MaxConnectionsPerSecond)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	healthInterval := 30 * time.Second
	if cfg.Server.BackendHealthInterval != "" {
		d, err := time.ParseDuration(cfg.Server.BackendHealthInterval)
		if err != nil {
			slog.Error("invalid backend_health_interval", "value", cfg.Server.BackendHealthInterval, "error", err)
			os.Exit(1)
		}
		healthInterval = d
	}
	monitor := newBackendHealthMonitor(currentState, metrics, healthInterval)

	if cfg.Server.MetricsAddr != "" {
		metricsListener, err := net.Listen("tcp", cfg.Server.MetricsAddr)
		if err != nil {
			slog.Error("metrics listen failed", "error", err)
			os.Exit(1)
		}
		startMetricsServer(ctx, metricsListener, monitor, cfg.Server.MetricsToken, cfg.Server.MetricsCertFile, cfg.Server.MetricsKeyFile)
	}

	trackerCtx, trackerCancel := context.WithCancel(ctx)
	defer trackerCancel()
	state.tracker.Start(trackerCtx)

	go monitor.Start(ctx)

	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sighupCh:
				slog.Info("received SIGHUP, reloading config")

				newCfg, err := loadConfig(configPath)
				if err != nil {
					slog.Error("reload failed: load config", "error", err)
					continue
				}

				newLogger := initLogger(newCfg.Server.LogLevel, newCfg.Server.LogFormat)
				slog.SetDefault(newLogger)

				oldState := currentState.Load()
				if oldState != nil && (newCfg.Server.Host != oldState.cfg.Server.Host || newCfg.Server.Port != oldState.cfg.Server.Port) {
					slog.Warn("listener address changed; restart required for new address to take effect")
				}

				newState, err := buildServerState(newCfg, metrics)
				if err != nil {
					slog.Error("reload failed: build state", "error", err)
					continue
				}

				trackerCancel()
				trackerCtx, trackerCancel = context.WithCancel(ctx)
				newState.tracker.Start(trackerCtx)

				connLimiter.Update(newCfg.Users)
				rateRegistry.Update(newCfg.Users)
				currentState.Store(newState)
				slog.Info("config reloaded", "users", len(newCfg.Users), "backends", len(newCfg.Backends))
			}
		}
	}()

	shutdownTimeout := 30 * time.Second
	if cfg.Server.ShutdownTimeout != "" {
		d, err := time.ParseDuration(cfg.Server.ShutdownTimeout)
		if err != nil {
			slog.Error("invalid shutdown_timeout", "value", cfg.Server.ShutdownTimeout, "error", err)
			os.Exit(1)
		}
		shutdownTimeout = d
	}

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("listen failed", "error", err)
		os.Exit(1)
	}
	if err := runServer(ctx, listener, shutdownTimeout, currentState, connLimiter, rateRegistry, acceptLimiter); err != nil {
		slog.Error("run server failed", "error", err)
		os.Exit(1)
	}
}
