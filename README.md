# sftp2s3

[![CI](https://github.com/matthewgall/sftp2s3/actions/workflows/ci.yml/badge.svg)](https://github.com/matthewgall/sftp2s3/actions)
[![Go Reference](https://pkg.go.dev/badge/github.com/matthewgall/sftp2s3.svg)](https://pkg.go.dev/github.com/matthewgall/sftp2s3)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A small SFTP server that proxies uploads, listings, and deletions to one or more S3 (or S3-compatible) backends.

Each backend appears as a top-level folder, e.g.:

```
/primary/config.bin
/minio/firmware.bin
```

## Build

```bash
go mod tidy
make build
```

For a static Linux binary (e.g. Alpine containers):

```bash
make build-static
```

## Configure

```bash
cp config.example.yaml config.yaml
# edit config.yaml
```

Environment variables are substituted in the config file:

```yaml
access_key_id: ${AWS_ACCESS_KEY_ID}
secret_access_key: ${AWS_SECRET_ACCESS_KEY:?set AWS_SECRET_ACCESS_KEY}
bucket: ${S3_BUCKET:-defaultbucket}
```

- `${VAR}` — replaced by the value of `VAR` (empty string if unset)
- `${VAR:-default}` — uses `default` if `VAR` is unset or empty
- `${VAR:?message}` — fails to start with `message` if `VAR` is unset or empty

## Authentication

Password auth and public-key auth are supported. You can use one or both.

```yaml
users:
  - username: backup
    password: changeme
    authorized_keys:
      - /home/backup/.ssh/authorized_keys
      - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... backup@example
```

## Per-user backend restrictions

```yaml
users:
  - username: backup
    password: changeme
    backends:
      - primary
```

Omit `backends` (or leave it empty) to allow access to every configured backend.

## Dynamic keys via sshid.io

Instead of (or in addition to) static `authorized_keys`, you can point a user at an sshid.io username. sftp2s3 fetches `https://sshid.io/<username>` using the curl User-Agent (`curl/8.14.1`) and `Accept: */*`, caches the result, and refreshes it on startup and on `SIGHUP`.

```yaml
server:
  cache_dir: /var/cache/sftp2s3  # general cache directory; currently used for sshid.io keys

users:
  - username: backup
    sshid:
      username: matthewgall
      # algorithms is optional; omit it to accept ED25519, ECDSA and RSA keys.
      # algorithms:
      #   - ed25519
      #   - ecdsa
      #   - rsa
```

If the sshid.io endpoint is unreachable, sftp2s3 falls back to a previously cached copy (even if it is technically stale) so users can still authenticate.

## Per-user permissions

You can restrict which operations a user is allowed to perform. Omit `permissions` (or leave it empty) to grant full access.

```yaml
users:
  - username: backup
    password: changeme
    permissions:
      - read
      - write
      - delete
```

Supported permissions:

- `read` — required for `ls`, `stat`, and downloading files.
- `write` — required for uploading files and creating directories.
- `delete` — required for removing files/directories and renaming (renaming deletes the source).

Cross-backend renames additionally require `read` on the source backend because the object is streamed through the server.

## Per-user connection and rate limits

You can cap concurrent SFTP sessions per user and throttle their transfer speed:

```yaml
users:
  - username: backup
    password: changeme
    max_connections: 5
    rate_limit_bytes_per_sec: 1048576
```

- `max_connections` — rejects new sessions once the limit is reached.
- `rate_limit_bytes_per_sec` — token-bucket throttle applied to reads and writes for that user.

## Backend timeouts

Each backend has an independent request timeout (default `60s`). Slow or non-responsive S3 calls time out instead of pinning a goroutine forever.

```yaml
backends:
  - name: primary
    timeout: 60s
```

## Startup validation

On startup each configured backend is checked by listing one object under its prefix. If a backend is unreachable or the bucket is inaccessible, the server exits immediately with a clear error.

## Logging

Structured logging is enabled by default. Configure the level and format:

```yaml
server:
  log_level: info   # debug, info, warn, error
  log_format: text  # text or json
```

## Metrics

If `metrics_addr` is set, sftp2s3 exposes Prometheus metrics and a health endpoint:

```yaml
server:
  metrics_addr: :2112
```

Available endpoints:

- `http://:2112/metrics` — Prometheus metrics
- `http://:2112/healthz` — health check

Metrics include:

- `sftp2s3_connections_active`
- `sftp2s3_connections_total`
- `sftp2s3_upload_bytes_total`
- `sftp2s3_download_bytes_total`
- `sftp2s3_s3_operations_total` (labelled by operation, backend, status)
- `sftp2s3_s3_operation_duration_seconds`
- `sftp2s3_auth_failures_total`
- `sftp2s3_backend_healthy` (labelled by backend; 1 = healthy, 0 = unhealthy)

## Backend health monitoring

Without any SFTP user interaction, sftp2s3 periodically issues a lightweight `ListObjectsV2` request to every configured backend. The results update the `sftp2s3_backend_healthy` gauge and the `/healthz` endpoint returns `503 Service Unavailable` while any backend is unhealthy.

```yaml
server:
  backend_health_interval: 30s  # set to 0 to disable
```

## Downloads / reads

Downloads are served via S3 ranged `GetObject` requests, so the server never loads an entire object into memory. Large files are read in chunks on demand.

## Per-user path prefix (chroot)

You can transparently chroot a user under each backend by setting a `prefix`. The user still sees `/backend/...` as normal, but their view is rooted at `backend/<prefix>/`.

```yaml
users:
  - username: site1
    password: changeme
    prefix: site1
```

## Auth failure tarpit

Failed auth attempts are tracked per source IP. After `max_attempts` failures within `window`, the IP is blocked for `block_duration`. While blocked, each attempt sleeps for `tarpit_delay` before failing. State is persisted to `state_file` and saved every `save_interval` (and on shutdown), so blocks survive restarts.

```yaml
server:
  auth_failures:
    max_attempts: 5
    window: 5m
    block_duration: 15m
    tarpit_delay: 2s
    state_file: /opt/sftp2s3/auth_state.json
    save_interval: 1m
```

## Config reload

Send `SIGHUP` to reload users, backends, logging, timeouts, auth-failure state, and the host key without dropping existing connections:

```bash
kill -HUP $(pidof sftp2s3)
```

Note: changes to the listener address (`host`/`port`) still require a restart.

## Graceful shutdown

On `SIGINT`/`SIGTERM` the server stops accepting new connections and waits for existing SFTP sessions to finish. The wait timeout is configurable (default `30s`):

```yaml
server:
  shutdown_timeout: 30s
```

## Run

```bash
./sftp2s3 -c config.yaml
```

The host key file is generated automatically if it does not exist.

Check the version with:

```bash
./sftp2s3 -version
```

## Running with Docker

Build and run with a mounted config:

```bash
docker build -t sftp2s3 .
docker run -p 2222:2222 -p 2112:2112 \
  -v $(pwd)/config.yaml:/etc/sftp2s3/config.yaml:ro \
  sftp2s3
```

The image uses an unprivileged `sftp2s3` user and exposes ports `2222` (SFTP) and `2112` (metrics/health).

## Running with systemd

1. Create a dedicated user and install the binary + config:

```bash
sudo useradd --system --no-create-home --home-dir /opt/sftp2s3 sftp2s3
sudo mkdir -p /opt/sftp2s3
sudo cp sftp2s3-static /opt/sftp2s3/sftp2s3
sudo cp config.yaml /opt/sftp2s3/config.yaml
sudo cp sftp2s3.env.example /opt/sftp2s3/sftp2s3.env
sudo chown -R sftp2s3:sftp2s3 /opt/sftp2s3
sudo chmod 600 /opt/sftp2s3/config.yaml /opt/sftp2s3/sftp2s3.env
```

2. Install and start the service:

```bash
sudo cp sftp2s3.service /etc/systemd/system/sftp2s3.service
sudo systemctl daemon-reload
sudo systemctl enable --now sftp2s3
sudo systemctl status sftp2s3
```

The included unit uses `CacheDirectory=sftp2s3` and `ReadWritePaths=/opt/sftp2s3 /var/cache/sftp2s3`, so the default `cache_dir: /var/cache/sftp2s3` is writable. If you change `cache_dir` in `config.yaml`, add that path to `ReadWritePaths=` in the unit file as well.

Logs:

```bash
sudo journalctl -u sftp2s3 -f
```

## Test

```bash
sftp -P 2222 backup@localhost
sftp> ls
primary   minio
sftp> cd primary
sftp> put config.bin
sftp> ls
sftp> rm config.bin
```

## Supported operations

- Password authentication
- `ls` / directory listing
- `stat` / `lstat`
- File upload (sequential or out-of-order writes; buffered to a temp file then uploaded via S3 multipart when larger than `part_size`)
- File and directory removal
- `mkdir` (creates a directory placeholder object)
- `rename` / `mv` within a backend (server-side `CopyObject`)
- `rename` / `mv` across backends (streamed download + upload, then source is deleted)
- `copy` / `cp` within a backend (server-side `CopyObject`, source is preserved)
- `copy` / `cp` across backends (streamed download + upload, source is preserved)
- Per-user backend restrictions and per-user permissions (`read` / `write` / `delete`)

Not supported: symlinks, chmod/chown, server-side copies.

## License

sftp2s3 is released under the [MIT License](LICENSE).
