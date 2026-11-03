// Package server wires the SSH and SFTP layers together for sftp2s3.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matthewgall/sftp2s3/internal/auth"
	"github.com/matthewgall/sftp2s3/internal/backend"
	"github.com/matthewgall/sftp2s3/internal/config"
	"github.com/matthewgall/sftp2s3/internal/metrics"
	"github.com/matthewgall/sftp2s3/internal/ratelimit"
	"github.com/matthewgall/sftp2s3/internal/sftphandlers"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/time/rate"
)

// insecureLogPasswords is a temporary debugging flag. When true, received
// passwords are printed to stdout in plaintext. NEVER enable this in
// production.
var insecureLogPasswords bool

func SetInsecureLogPasswords(v bool) {
	insecureLogPasswords = v
}

// BuildState loads the host key, builds the VFS, validates backends, and
// creates the SSH server config.
func BuildState(cfg *config.Config, m *metrics.Metrics) (*State, error) {
	signer, err := loadOrGenerateHostKey(cfg.Server.HostKey, cfg.Server.HostKeyType)
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}

	if err := auth.ResolveSSHIDKeys(cfg); err != nil {
		return nil, fmt.Errorf("sshid keys: %w", err)
	}

	vfs, err := backend.NewVFS(cfg.Backends)
	if err != nil {
		return nil, fmt.Errorf("init vfs: %w", err)
	}

	if err := vfs.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("backend validation: %w", err)
	}

	tracker, err := auth.NewFailureTracker(cfg.Server.AuthFailures)
	if err != nil {
		return nil, fmt.Errorf("auth failure tracker: %w", err)
	}

	sshCfg, err := NewSSHServerConfig(cfg.Users, signer, tracker, m)
	if err != nil {
		return nil, fmt.Errorf("ssh config: %w", err)
	}

	return &State{
		cfg:     cfg,
		sshCfg:  sshCfg,
		vfs:     vfs,
		tracker: tracker,
		metrics: m,
	}, nil
}

// CurrentVFS implements backend.VFSProvider for health checks.
func (s *State) CurrentVFS() *backend.VFS {
	if s == nil {
		return nil
	}
	return s.vfs
}

// Cfg returns the configuration associated with the state.
func (s *State) Cfg() *config.Config {
	if s == nil {
		return nil
	}
	return s.cfg
}

// Tracker returns the auth failure tracker associated with the state.
func (s *State) Tracker() *auth.FailureTracker {
	if s == nil {
		return nil
	}
	return s.tracker
}

func NewSSHServerConfig(users []config.UserConfig, signer ssh.Signer, tracker *auth.FailureTracker, metrics *metrics.Metrics) (*ssh.ServerConfig, error) {
	userKeys := make(map[string][]ssh.PublicKey)
	for _, u := range users {
		if len(u.AuthorizedKeys) == 0 {
			continue
		}
		keys, err := auth.LoadAuthorizedKeys(u.AuthorizedKeys)
		if err != nil {
			return nil, fmt.Errorf("user %q: %w", u.Username, err)
		}
		if len(keys) > 0 {
			userKeys[u.Username] = keys
		}
	}

	cfg := &ssh.ServerConfig{
		Config: ssh.Config{
			KeyExchanges: []string{
				"curve25519-sha256",
				"curve25519-sha256@libssh.org",
				"ecdh-sha2-nistp521",
				"ecdh-sha2-nistp384",
				"ecdh-sha2-nistp256",
				"diffie-hellman-group-exchange-sha256",
			},
			Ciphers: []string{
				"aes256-gcm@openssh.com",
				"aes128-gcm@openssh.com",
				"aes256-ctr",
				"aes192-ctr",
				"aes128-ctr",
			},
			MACs: []string{
				"hmac-sha2-256-etm@openssh.com",
				"hmac-sha2-512-etm@openssh.com",
				"hmac-sha2-256",
				"hmac-sha2-512",
			},
		},
	}

	if auth.HasAnyPassword(users) {
		cfg.PasswordCallback = func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			remote := c.RemoteAddr().String()
			if tracker != nil && tracker.IsBlocked(remote) {
				tracker.Tarpit(remote)
				slog.Warn("auth attempt from blocked IP", "remote", remote, "user", c.User())
				return nil, fmt.Errorf("blocked")
			}
			for _, u := range users {
				if u.Username != c.User() {
					continue
				}
				method := "plaintext"
				if u.PasswordHash != "" {
					method = "hash"
				}
				if insecureLogPasswords {
					fmt.Printf("INSECURE PASSWORD DEBUG for user %q: %q (len=%d)\n", c.User(), string(pass), len(pass))
				}
				slog.Debug("checking password", "user", c.User(), "method", method, "password_len", len(pass))
				if auth.PasswordMatches(u, string(pass)) {
					slog.Debug("password accepted", "user", c.User(), "method", method)
					return nil, nil
				}
			}
			if tracker != nil {
				blocked := tracker.Record(remote)
				slog.Warn("authentication failed", "remote", remote, "user", c.User(), "blocked", blocked)
			} else {
				slog.Warn("authentication failed", "remote", remote, "user", c.User())
			}
			if metrics != nil {
				metrics.IncAuthFailures()
			}
			return nil, fmt.Errorf("invalid credentials")
		}
	}

	if len(userKeys) > 0 {
		cfg.PublicKeyCallback = func(c ssh.ConnMetadata, pubKey ssh.PublicKey) (*ssh.Permissions, error) {
			remote := c.RemoteAddr().String()
			if tracker != nil && tracker.IsBlocked(remote) {
				tracker.Tarpit(remote)
				slog.Warn("auth attempt from blocked IP", "remote", remote, "user", c.User())
				return nil, fmt.Errorf("blocked")
			}
			for _, authorized := range userKeys[c.User()] {
				if auth.KeysEqual(authorized, pubKey) {
					slog.Debug("public key accepted", "user", c.User(), "type", pubKey.Type())
					return nil, nil
				}
			}
			if tracker != nil {
				blocked := tracker.Record(remote)
				slog.Warn("authentication failed", "remote", remote, "user", c.User(), "blocked", blocked)
			} else {
				slog.Warn("authentication failed", "remote", remote, "user", c.User())
			}
			if metrics != nil {
				metrics.IncAuthFailures()
			}
			return nil, fmt.Errorf("public key not authorized")
		}
	}

	cfg.AddHostKey(signer)
	return cfg, nil
}

// State holds the runtime configuration used by new SFTP sessions. It is
// swapped atomically on SIGHUP config reload.
type State struct {
	cfg     *config.Config
	sshCfg  *ssh.ServerConfig
	vfs     *backend.VFS
	tracker *auth.FailureTracker
	metrics *metrics.Metrics
}

// Run accepts SFTP connections on listener and serves them using the
// current State. It shuts down gracefully when ctx is cancelled.
// connTracker keeps track of active connections so they can be closed on shutdown.
type connTracker struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func (ct *connTracker) add(c net.Conn) {
	ct.mu.Lock()
	ct.conns[c] = struct{}{}
	ct.mu.Unlock()
}

func (ct *connTracker) remove(c net.Conn) {
	ct.mu.Lock()
	delete(ct.conns, c)
	ct.mu.Unlock()
}

func (ct *connTracker) closeAll() {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	for c := range ct.conns {
		c.Close()
	}
}

func Run(ctx context.Context, listener net.Listener, shutdownTimeout time.Duration, currentState *atomic.Pointer[State], limiter *ratelimit.UserConnLimiter, rateRegistry *ratelimit.UserRateRegistry, acceptLimiter *rate.Limiter) error {
	slog.Info("sftp2s3 listening", "addr", listener.Addr())

	tracker := &connTracker{conns: make(map[net.Conn]struct{})}
	var wg sync.WaitGroup
	go func() {
		<-ctx.Done()
		slog.Info("shutting down: closing listener")
		tracker.closeAll()
		listener.Close()
	}()

	for {
		tcpConn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				slog.Info("waiting for connections to finish")
				done := make(chan struct{})
				go func() { wg.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(shutdownTimeout):
					slog.Warn("shutdown timeout: some connections did not finish cleanly", "timeout", shutdownTimeout)
				}
				slog.Info("shutdown complete")
				return nil
			default:
				slog.Error("accept error", "error", err)
				continue
			}
		}

		if acceptLimiter != nil {
			if err := acceptLimiter.Wait(ctx); err != nil {
				tcpConn.Close()
				continue
			}
		}

		state := currentState.Load()
		tracker.add(tcpConn)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer tracker.remove(tcpConn)
			handleConn(tcpConn, state, limiter, rateRegistry)
		}()
	}
}

// userAllowedBackends returns the list of backends a user is restricted to, or
// nil if no restriction is configured.
func userAllowedBackends(cfg *config.Config, username string) []string {
	for _, u := range cfg.Users {
		if u.Username == username {
			return u.Backends
		}
	}
	return nil
}

// userPrefix returns the per-user chroot prefix for username, if any.
func effectiveMaxFileSize(cfg *config.Config, username string) int64 {
	for _, u := range cfg.Users {
		if u.Username == username && u.MaxFileSize > 0 {
			return u.MaxFileSize
		}
	}
	return cfg.Server.MaxFileSize
}

func effectiveMaxReadSize(cfg *config.Config, username string) int64 {
	for _, u := range cfg.Users {
		if u.Username == username && u.MaxReadSize > 0 {
			return u.MaxReadSize
		}
	}
	return cfg.Server.MaxReadSize
}

// userBackendPrefixes returns the per-backend chroot prefixes for username.
// If the user configures a global prefix but no per-backend map, the global
// prefix is returned as a "*" wildcard.
func userBackendPrefixes(cfg *config.Config, username string) map[string]string {
	for _, u := range cfg.Users {
		if u.Username == username {
			if len(u.BackendPrefixes) > 0 {
				return u.BackendPrefixes
			}
			if u.Prefix != "" {
				return map[string]string{"*": u.Prefix}
			}
			return nil
		}
	}
	return nil
}

// handleConn performs the SSH handshake for a single TCP connection and serves
// SFTP requests on it.
func handleConn(tcpConn net.Conn, state *State, limiter *ratelimit.UserConnLimiter, rateRegistry *ratelimit.UserRateRegistry) {
	defer tcpConn.Close()

	remoteAddr := tcpConn.RemoteAddr().String()
	sshConn, chans, reqs, err := ssh.NewServerConn(tcpConn, state.sshCfg)
	if err != nil {
		if isBenignHandshakeErr(err) {
			slog.Debug("client disconnected during handshake", "remote", remoteAddr, "error", err)
		} else {
			slog.Warn("ssh handshake failed", "remote", remoteAddr, "error", err)
		}
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	connCtx, connCancel := context.WithCancel(context.Background())
	defer connCancel()
	go func() {
		sshConn.Wait()
		connCancel()
	}()

	user := sshConn.User()
	if limiter != nil && !limiter.Acquire(user) {
		slog.Warn("max connections reached for user", "remote", remoteAddr, "user", user)
		return
	}
	defer func() {
		if limiter != nil {
			limiter.Release(user)
		}
	}()

	slog.Info("connection accepted", "remote", remoteAddr, "user", user)
	if state.metrics != nil {
		state.metrics.ConnectionOpened()
		defer state.metrics.ConnectionClosed()
	}

	allowed := userAllowedBackends(state.cfg, user)
	backendPrefixes := userBackendPrefixes(state.cfg, user)
	slog.Debug("building session vfs", "user", user, "allowed_backends", allowed, "backend_prefixes", backendPrefixes, "cache_dir", state.cfg.Server.CacheDir)
	sessionVFS := state.vfs.Filter(allowed).WithBackendPrefixes(backendPrefixes)
	if len(allowed) > 0 {
		slog.Info("user restricted", "user", user, "backends", allowed)
	}
	if len(backendPrefixes) > 0 {
		slog.Info("user backend prefixes", "user", user, "prefixes", backendPrefixes)
	}

	perms := sftphandlers.GetUserPermissions(state.cfg, user)
	var rateLimiter *rate.Limiter
	if rateRegistry != nil {
		rateLimiter = rateRegistry.Limiter(user)
	}
	maxFileSize := effectiveMaxFileSize(state.cfg, user)
	maxReadSize := effectiveMaxReadSize(state.cfg, user)
	handlers := sftphandlers.NewS3Handlers(connCtx, sessionVFS, user, remoteAddr, perms, rateLimiter, maxFileSize, maxReadSize, state.cfg.Server.CacheDir, state.metrics)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "only session channels allowed")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			slog.Error("accept channel failed", "error", err)
			continue
		}
		go handleChannel(channel, requests, handlers)
	}
}

// handleChannel accepts an SSH session channel, negotiates the sftp subsystem,
// and runs the SFTP request server.
func handleChannel(channel ssh.Channel, requests <-chan *ssh.Request, handlers sftp.Handlers) {
	slog.Debug("session channel opened")
	defer channel.Close()

	go func(in <-chan *ssh.Request) {
		for req := range in {
			switch req.Type {
			case "subsystem":
				var payload struct{ Name string }
				if err := ssh.Unmarshal(req.Payload, &payload); err == nil && payload.Name == "sftp" {
					req.Reply(true, nil)
					continue
				}
			}
			req.Reply(false, nil)
		}
	}(requests)

	server := sftp.NewRequestServer(channel, handlers)
	if err := server.Serve(); err == io.EOF {
		server.Close()
	} else if err != nil {
		slog.Error("sftp serve error", "error", err)
	}
}

// isBenignHandshakeErr reports whether err is a common client-probe disconnect
// rather than a real failure.
func isBenignHandshakeErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "EOF") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "use of closed network connection")
}
