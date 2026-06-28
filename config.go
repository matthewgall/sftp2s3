package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultHost     = "0.0.0.0"
	defaultPort     = 2222
	defaultHostKey  = "host_rsa_key"
	defaultRegion   = "us-east-1"
	defaultPartSize = 8 * 1024 * 1024
)

// Config is the top-level application configuration.
type Config struct {
	Server struct {
		Host                  string             `yaml:"host"`
		Port                  int                `yaml:"port"`
		HostKey               string             `yaml:"host_key"`
		LogLevel              string             `yaml:"log_level"`
		LogFormat             string             `yaml:"log_format"`
		ShutdownTimeout       string             `yaml:"shutdown_timeout"`
		MetricsAddr           string             `yaml:"metrics_addr"`
		CacheDir              string             `yaml:"cache_dir"`
		BackendHealthInterval string             `yaml:"backend_health_interval"`
		AuthFailures          AuthFailuresConfig `yaml:"auth_failures"`
	} `yaml:"server"`
	Users    []UserConfig    `yaml:"users"`
	Backends []BackendConfig `yaml:"backends"`
}

// SSHIDConfig configures dynamic authorized-key lookup via sshid.io.
type SSHIDConfig struct {
	Username   string   `yaml:"username"`
	Algorithms []string `yaml:"algorithms"`
	CacheDir   string   `yaml:"cache_dir"`
}

// UserConfig defines a single SFTP user and their restrictions.
type UserConfig struct {
	Username             string       `yaml:"username"`
	Password             string       `yaml:"password"`
	Backends             []string     `yaml:"backends"`
	AuthorizedKeys       []string     `yaml:"authorized_keys"`
	Prefix               string       `yaml:"prefix"`
	Permissions          []string     `yaml:"permissions"`
	SSHID                *SSHIDConfig `yaml:"sshid"`
	MaxConnections       int          `yaml:"max_connections"`
	RateLimitBytesPerSec int64        `yaml:"rate_limit_bytes_per_sec"`
}

// BackendConfig defines an S3-compatible backend.
type BackendConfig struct {
	Name            string `yaml:"name"`
	EndpointURL     string `yaml:"endpoint_url"`
	Region          string `yaml:"region"`
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	Bucket          string `yaml:"bucket"`
	Prefix          string `yaml:"prefix"`
	UsePathStyle    bool   `yaml:"use_path_style"`
	PartSize        int64  `yaml:"part_size"`
	Timeout         string `yaml:"timeout"`
}

// loadConfig reads and parses the YAML config file, substitutes environment
// variables, and applies defaults for missing fields.
func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, err = envSubstitute(b)
	if err != nil {
		return nil, fmt.Errorf("env substitution: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = defaultHost
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = defaultPort
	}
	if cfg.Server.HostKey == "" {
		cfg.Server.HostKey = defaultHostKey
	}
	if cfg.Server.LogLevel == "" {
		cfg.Server.LogLevel = "info"
	}
	if cfg.Server.LogFormat == "" {
		cfg.Server.LogFormat = "text"
	}
	for i := range cfg.Backends {
		if cfg.Backends[i].Region == "" {
			cfg.Backends[i].Region = defaultRegion
		}
		if cfg.Backends[i].PartSize == 0 {
			cfg.Backends[i].PartSize = defaultPartSize
		}
	}
	return &cfg, nil
}

// envSubstitute replaces $VAR and ${VAR} with environment values.
// ${VAR:-default} uses a default if the variable is empty/unset.
// ${VAR:?message} returns an error if the variable is empty/unset.
func envSubstitute(data []byte) ([]byte, error) {
	var out bytes.Buffer
	for i := 0; i < len(data); {
		if data[i] != '$' {
			out.WriteByte(data[i])
			i++
			continue
		}
		if i+1 >= len(data) {
			out.WriteByte('$')
			break
		}

		if data[i+1] == '{' {
			end := bytes.IndexByte(data[i+2:], '}')
			if end < 0 {
				out.WriteByte('$')
				i++
				continue
			}
			end += i + 2
			name, mod, arg := parseEnvExpr(string(data[i+2 : end]))
			val, err := resolveEnv(name, mod, arg)
			if err != nil {
				return nil, err
			}
			out.WriteString(val)
			i = end + 1
			continue
		}

		if isIdentStart(data[i+1]) {
			j := i + 2
			for j < len(data) && isIdentChar(data[j]) {
				j++
			}
			out.WriteString(os.Getenv(string(data[i+1 : j])))
			i = j
			continue
		}

		out.WriteByte('$')
		i++
	}
	return out.Bytes(), nil
}

func parseEnvExpr(inner string) (name, mod, arg string) {
	parts := strings.SplitN(inner, ":", 2)
	name = parts[0]
	if len(parts) == 1 {
		return name, "", ""
	}
	rest := parts[1]
	if strings.HasPrefix(rest, "-") {
		return name, "-", rest[1:]
	}
	if strings.HasPrefix(rest, "?") {
		return name, "?", rest[1:]
	}
	return name, "", ""
}

func resolveEnv(name, mod, arg string) (string, error) {
	val := os.Getenv(name)
	if val == "" {
		switch mod {
		case "-":
			return arg, nil
		case "?":
			if arg == "" {
				arg = fmt.Sprintf("environment variable %q is required", name)
			}
			return "", fmt.Errorf("%s", arg)
		}
	}
	return val, nil
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
