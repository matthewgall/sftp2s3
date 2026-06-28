package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestBuildServerState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>testbucket</Name>
  <Prefix></Prefix>
  <MaxKeys>1</MaxKeys>
  <IsTruncated>false</IsTruncated>
  <KeyCount>0</KeyCount>
</ListBucketResult>`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	hostKeyPath := filepath.Join(dir, "host_key")
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgData := fmt.Sprintf(`server:
  host: 127.0.0.1
  port: 2222
  host_key: %s
backends:
  - name: primary
    bucket: testbucket
    region: us-east-1
    endpoint_url: %s
    use_path_style: true
    access_key_id: ${TEST_AK}
    secret_access_key: ${TEST_SK}
users:
  - username: alice
    password: secret
`, hostKeyPath, srv.URL)

	if err := os.WriteFile(cfgPath, []byte(cfgData), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_AK", "ak")
	t.Setenv("TEST_SK", "sk")

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	state, err := buildServerState(cfg, NewMetrics(prometheus.NewRegistry()))
	if err != nil {
		t.Fatalf("build server state: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}
}

func TestBuildServerStateHostKeyError(t *testing.T) {
	cfg := &Config{}
	cfg.Server.HostKey = t.TempDir() // directory is not a valid host key path
	cfg.Server.LogLevel = "info"
	cfg.Server.LogFormat = "text"

	_, err := buildServerState(cfg, NewMetrics(prometheus.NewRegistry()))
	if err == nil {
		t.Fatal("expected host key error")
	}
}

func TestBuildServerStateVFSInitError(t *testing.T) {
	cfg := &Config{}
	cfg.Server.HostKey = filepath.Join(t.TempDir(), "host_key")
	cfg.Server.LogLevel = "info"
	cfg.Server.LogFormat = "text"
	cfg.Backends = []BackendConfig{{
		Name:    "bad",
		Bucket:  "bucket",
		Region:  "us-east-1",
		Timeout: "not-a-duration",
	}}

	_, err := buildServerState(cfg, NewMetrics(prometheus.NewRegistry()))
	if err == nil {
		t.Fatal("expected vfs init error")
	}
}

func TestBuildServerStateValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := &Config{}
	cfg.Server.HostKey = filepath.Join(t.TempDir(), "host_key")
	cfg.Server.LogLevel = "info"
	cfg.Server.LogFormat = "text"
	cfg.Backends = []BackendConfig{{
		Name:            "primary",
		Bucket:          "bucket",
		Region:          "us-east-1",
		EndpointURL:     srv.URL,
		UsePathStyle:    true,
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
	}}
	cfg.Users = []UserConfig{{
		Username: "alice",
		Password: "secret",
	}}

	_, err := buildServerState(cfg, NewMetrics(prometheus.NewRegistry()))
	if err == nil {
		t.Fatal("expected backend validation error")
	}
}

func TestBuildServerStateSSHConfigError(t *testing.T) {
	cfg := &Config{}
	cfg.Server.HostKey = filepath.Join(t.TempDir(), "host_key")
	cfg.Server.LogLevel = "info"
	cfg.Server.LogFormat = "text"
	cfg.Backends = []BackendConfig{{
		Name:            "primary",
		Bucket:          "bucket",
		Region:          "us-east-1",
		EndpointURL:     "http://s3mock",
		UsePathStyle:    true,
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
	}}
	cfg.Users = []UserConfig{{
		Username:       "alice",
		AuthorizedKeys: []string{"not-a-key"},
	}}

	_, err := buildServerState(cfg, NewMetrics(prometheus.NewRegistry()))
	if err == nil {
		t.Fatal("expected ssh config error")
	}
}

func TestInitLogger(t *testing.T) {
	tests := []struct {
		level  string
		format string
	}{
		{"debug", "text"},
		{"info", "json"},
		{"warn", "text"},
		{"error", "json"},
		{"unknown", "text"},
	}
	for _, tt := range tests {
		logger := initLogger(tt.level, tt.format)
		if logger == nil {
			t.Fatalf("initLogger(%q, %q) returned nil", tt.level, tt.format)
		}
		if logger.Enabled(nil, slog.LevelInfo) && tt.level == "error" {
			t.Fatalf("expected info to be disabled for error level")
		}
	}
}
