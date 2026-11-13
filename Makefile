# squatterd / squatClientDaemon
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
FUZZTIME ?= 20s
STATICCHECK ?= $(shell command -v staticcheck 2>/dev/null || echo $(HOME)/go/bin/staticcheck)

.PHONY: all build test race fuzz lint fmt vet dist clean verify-live

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o squatterd ./cmd/squatterd

test:
	go test ./...

race:
	go test -race -count=1 ./...

fuzz:
	go test ./pkg/wire -run '^$$' -fuzz FuzzDecodeClient -fuzztime $(FUZZTIME)

fmt:
	gofmt -w cmd internal pkg scripts

vet:
	go vet ./...

lint: vet
	@test -z "$$(gofmt -l cmd internal pkg scripts)" || (echo "gofmt needed:"; gofmt -l cmd internal pkg scripts; exit 1)
	$(STATICCHECK) ./...

# Cross-compile static binaries into dist/ for linux/darwin/windows x amd64/arm64.
dist:
	@mkdir -p dist
	@set -e; for os in linux darwin windows; do for arch in amd64 arm64; do \
	  ext=""; [ $$os = windows ] && ext=".exe"; \
	  echo "building dist/squatterd-$$os-$$arch$$ext"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/squatterd-$$os-$$arch$$ext ./cmd/squatterd; \
	done; done

# Needs network access: pairs with a real squatterd and talks to a live MUD.
verify-live:
	./scripts/verify-live.sh

clean:
	rm -f squatterd
	rm -f dist/squatterd-*
