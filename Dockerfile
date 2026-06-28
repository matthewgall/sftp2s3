# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.Version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" \
    -o sftp2s3 .

# Runtime stage
FROM alpine:latest

RUN apk add --no-cache ca-certificates && \
    adduser -D -h /var/lib/sftp2s3 sftp2s3

WORKDIR /var/lib/sftp2s3

COPY --from=builder /src/sftp2s3 /usr/local/bin/sftp2s3
COPY --from=builder /src/config.example.yaml /etc/sftp2s3/config.yaml

USER sftp2s3

EXPOSE 2222 2112

ENTRYPOINT ["/usr/local/bin/sftp2s3"]
CMD ["-c", "/etc/sftp2s3/config.yaml"]
