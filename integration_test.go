package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/crypto/ssh"
)

func TestSFTPIntegration(t *testing.T) {
	// Generate a host key for the server.
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}

	// Build a VFS with a single mock backend.
	objects := map[string][]byte{}
	b := newMockBackend(t, "bucket", "", objects)
	vfs := &VFS{Backends: map[string]*Backend{b.Name: b}}

	cfg := &Config{}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 22
	cfg.Users = []UserConfig{{Username: "test", Password: "secret"}}

	tracker, err := newAuthFailureTracker(AuthFailuresConfig{})
	if err != nil {
		t.Fatal(err)
	}
	metrics := NewMetrics(nil)

	sshCfg, err := newSSHServerConfig(cfg.Users, hostSigner, tracker, metrics)
	if err != nil {
		t.Fatal(err)
	}

	state := &serverState{
		cfg:     cfg,
		sshCfg:  sshCfg,
		vfs:     vfs,
		tracker: tracker,
		metrics: metrics,
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	currentState := &atomic.Pointer[serverState]{}
	currentState.Store(state)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runServer(ctx, listener, 5*time.Second, currentState, newUserConnLimiter(nil), newUserRateRegistry(nil), nil)
	}()

	clientConn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	sshClient, chans, reqs, err := ssh.NewClientConn(clientConn, listener.Addr().String(), &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("secret")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh handshake: %v", err)
	}
	client := ssh.NewClient(sshClient, chans, reqs)
	defer client.Close()

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	defer sftpClient.Close()

	// List root.
	entries, err := sftpClient.ReadDir("/")
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "mock" || !entries[0].IsDir() {
		t.Fatalf("unexpected root entries: %+v", entries)
	}

	// Upload a file.
	f, err := sftpClient.Create("/mock/hello.txt")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	body := []byte("hello from sftp")
	if _, err := f.Write(body); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// List backend.
	entries, err = sftpClient.ReadDir("/mock")
	if err != nil {
		t.Fatalf("list backend: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "hello.txt" {
		t.Fatalf("unexpected backend entries: %+v", entries)
	}

	// Download the file.
	rf, err := sftpClient.Open("/mock/hello.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := io.ReadAll(rf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	rf.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("downloaded %q, want %q", got, body)
	}

	// Remove the file.
	if err := sftpClient.Remove("/mock/hello.txt"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// Verify metrics recorded the connection.
	if testutil.ToFloat64(metrics.totalConns) != 1 {
		t.Fatalf("expected 1 connection, got %v", testutil.ToFloat64(metrics.totalConns))
	}

	// Close connections and shut down the server.
	sftpClient.Close()
	client.Close()
	cancel()

	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("server goroutine did not finish")
	}
}
