#!/bin/sh
# Show how the vendored crypto/tls (pkg/tlsmigrate/tls) differs from the crypto/tls of the Go toolchain
# on PATH: after a Go upgrade this is the list of local patches to re-apply onto the new upstream.
# Usage: pkg/tlsmigrate/scripts/diff-upstream.sh [GOROOT]
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
goroot=${1:-$(go env GOROOT)}
echo "vendored: $(cat "$here/tls/UPSTREAM_GO_VERSION")   toolchain: $(go version)"
for f in "$goroot"/src/crypto/tls/*.go; do
	b=$(basename "$f")
	case $b in *_test.go|generate_cert.go) continue ;; esac
	if [ -f "$here/tls/$b" ]; then
		diff -u "$f" "$here/tls/$b" || true
	else
		echo "NEW UPSTREAM FILE (vendor it): $b"
	fi
done
for f in "$here"/tls/*.go; do
	b=$(basename "$f")
	[ -f "$goroot/src/crypto/tls/$b" ] || echo "local-only file: $b"
done
