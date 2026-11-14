GO ?= go
GOFMT ?= gofmt
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_FLAGS = -trimpath -buildvcs=false -ldflags '-s -w -X loop/pkg/buildinfo.Version=$(VERSION) -X loop/pkg/buildinfo.Commit=$(COMMIT)'

.PHONY: build check fmt-check
build:
	$(GO) build $(BUILD_FLAGS) -o loop .

fmt-check:
	@unformatted="$$($(GOFMT) -l .)" && \
		if [ -n "$$unformatted" ]; then \
			printf 'Run gofmt on:\n%s\n' "$$unformatted"; \
			exit 1; \
		fi

check: fmt-check
	$(GO) vet ./...
	$(GO) test -race ./...
	$(GO) build $(BUILD_FLAGS) -o loop .
