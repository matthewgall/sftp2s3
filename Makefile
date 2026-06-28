.PHONY: all build build-static test clean run version

BINARY := sftp2s3
CONFIG := config.yaml

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILDDATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildDate=$(BUILDDATE)

all: build

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) .

build-static:
	CGO_ENABLED=0 GOOS=linux go build -ldflags="$(LDFLAGS)" -o $(BINARY)-static .

test:
	go test ./...

clean:
	rm -f $(BINARY) $(BINARY)-static $(CONFIG) host_rsa_key

run: build
	./$(BINARY)

version:
	@go run -ldflags="$(LDFLAGS)" . -version
