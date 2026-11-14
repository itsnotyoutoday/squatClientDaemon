# squatterd / squatClientDaemon
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
FUZZTIME ?= 20s
STATICCHECK ?= $(shell command -v staticcheck 2>/dev/null || echo $(HOME)/go/bin/staticcheck)

.PHONY: all build test race fuzz lint fmt vet dist clean verify-live test-tf harness

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o squatterd ./cmd/squatterd
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o tf-squatterd ./cmd/tf-squatterd

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
	  echo "building dist/tf-squatterd-$$os-$$arch$$ext"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/tf-squatterd-$$os-$$arch$$ext ./cmd/tf-squatterd; \
	done; done

# Drives a real TinyFugue (squatterbridge branch) with the real tf-squatterd:
#   make test-tf TF_BIN=/path/to/tf TF_LIB=/path/to/tinyfugue/lib/tf
test-tf:
	@test -n "$(TF_BIN)" -a -n "$(TF_LIB)" || (echo "usage: make test-tf TF_BIN=/path/to/tf TF_LIB=/path/to/lib/tf"; exit 2)
	TF_BIN=$(TF_BIN) TF_LIB=$(TF_LIB) go test -count=1 -run RealTinyFugue -v ./internal/tfbridge/

# Everything against real games (private CobraMUSH + Evennia): see scripts/tfharness/README.md
harness:
	@test -n "$(TF_BIN)" -a -n "$(TF_LIB)" || (echo "usage: make harness TF_BIN=/path/to/tf TF_LIB=/path/to/lib/tf"; exit 2)
	go run ./scripts/tfharness --tf-bin $(TF_BIN) --tf-lib $(TF_LIB)

# Needs network access: pairs with a real squatterd and talks to a live MUD.
verify-live:
	./scripts/verify-live.sh

clean:
	rm -f squatterd tf-squatterd
	rm -f dist/squatterd-* dist/tf-squatterd-*
