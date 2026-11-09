GO ?= go
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_FLAGS = -trimpath -buildvcs=false -ldflags '-s -w -X loop/pkg/buildinfo.Version=$(VERSION) -X loop/pkg/buildinfo.Commit=$(COMMIT)'

.PHONY: build check
build:
	$(GO) build $(BUILD_FLAGS) -o loop .

check:
	$(GO) vet ./...
	$(GO) build $(BUILD_FLAGS) -o loop .
