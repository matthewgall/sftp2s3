package main

import (
	"os"
	"testing"
)

func TestEnvSubstitute(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "no substitution",
			input: "plain text",
			want:  "plain text",
		},
		{
			name:  "bare variable",
			env:   map[string]string{"FOO": "bar"},
			input: "$FOO",
			want:  "bar",
		},
		{
			name:  "braced variable",
			env:   map[string]string{"FOO": "bar"},
			input: "${FOO}",
			want:  "bar",
		},
		{
			name:  "default used when unset",
			input: "${FOO:-default}",
			want:  "default",
		},
		{
			name:  "default ignored when set",
			env:   map[string]string{"FOO": "bar"},
			input: "${FOO:-default}",
			want:  "bar",
		},
		{
			name:    "required variable unset",
			input:   "${FOO:?missing foo}",
			wantErr: true,
		},
		{
			name:  "required variable set",
			env:   map[string]string{"FOO": "bar"},
			input: "${FOO:?missing foo}",
			want:  "bar",
		},
		{
			name:  "mixed text",
			env:   map[string]string{"USER": "alice", "HOST": "box"},
			input: "user=${USER} host=$HOST",
			want:  "user=alice host=box",
		},
		{
			name:  "unset variable becomes empty",
			input: "x${MISSING}x",
			want:  "xx",
		},
		{
			name:  "dollar without identifier is literal",
			input: "price is $5",
			want:  "price is $5",
		},
		{
			name:  "unclosed brace is literal",
			input: "${FOO",
			want:  "${FOO",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Start with known-empty variables, then apply test-specific values.
			for _, k := range []string{"FOO", "USER", "HOST", "MISSING"} {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := envSubstitute([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadConfigEnvSubstitution(t *testing.T) {
	t.Setenv("SFTP_TEST_USER", "backup")
	t.Setenv("SFTP_TEST_PASS", "secret")
	t.Setenv("SFTP_TEST_BACKEND", "primary")

	content := []byte(`
server:
  host: 127.0.0.1
  port: 2222
  host_key: host_key
users:
  - username: ${SFTP_TEST_USER}
    password: ${SFTP_TEST_PASS}
backends:
  - name: ${SFTP_TEST_BACKEND}
    bucket: mybucket
    access_key_id: key
    secret_access_key: secret
`)

	tmp, err := os.CreateTemp("", "sftp2s3-config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	cfg, err := loadConfig(tmp.Name())
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Username != "backup" || cfg.Users[0].Password != "secret" {
		t.Fatalf("user not substituted: %+v", cfg.Users)
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0].Name != "primary" {
		t.Fatalf("backend not substituted: %+v", cfg.Backends)
	}
}

func TestResolveEnvExpr(t *testing.T) {
	tests := []struct {
		inner string
		name  string
		mod   string
		arg   string
	}{
		{"VAR", "VAR", "", ""},
		{"VAR:-default", "VAR", "-", "default"},
		{"VAR:?msg", "VAR", "?", "msg"},
	}
	for _, tt := range tests {
		name, mod, arg := parseEnvExpr(tt.inner)
		if name != tt.name || mod != tt.mod || arg != tt.arg {
			t.Fatalf("parseEnvExpr(%q) = (%q,%q,%q), want (%q,%q,%q)",
				tt.inner, name, mod, arg, tt.name, tt.mod, tt.arg)
		}
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	_, err := loadConfig("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	tmp, err := os.CreateTemp("", "bad-config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString("not: [valid yaml"); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	_, err = loadConfig(tmp.Name())
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestResolveEnv(t *testing.T) {
	t.Setenv("SET_VAR", "value")
	t.Setenv("UNSET_VAR", "")

	val, err := resolveEnv("SET_VAR", "", "")
	if err != nil || val != "value" {
		t.Fatalf("got %q, err=%v", val, err)
	}

	val, err = resolveEnv("UNSET_VAR", "-", "default")
	if err != nil || val != "default" {
		t.Fatalf("got %q, err=%v", val, err)
	}

	_, err = resolveEnv("UNSET_VAR", "?", "missing")
	if err == nil {
		t.Fatalf("expected error for required unset variable")
	}
}
