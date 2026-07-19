BINARY ?= listnr
TARGET_ARCH ?= amd64
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
COMMIT_TIME ?= $(shell git show -s --format=%cI HEAD 2>/dev/null)

BUILDINFO = github.com/vrypan/listnr/internal/buildinfo
LDFLAGS = -X '$(BUILDINFO).Version=$(VERSION)' \
	-X '$(BUILDINFO).Commit=$(COMMIT)' \
	-X '$(BUILDINFO).CommitTime=$(COMMIT_TIME)'
RELEASE_LDFLAGS = -s -w $(LDFLAGS)

.PHONY: build build-debug build-linux build-linux-debug test

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(RELEASE_LDFLAGS)" \
		-o $(BINARY) .

build-debug:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(TARGET_ARCH) go build -trimpath \
		-ldflags "$(RELEASE_LDFLAGS)" -o $(BINARY) .

build-linux-debug:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(TARGET_ARCH) go build -trimpath \
		-ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...
