#!/bin/sh
# Regenerate NAME.expected-tf (real tf output) for all cases, or the given ones.
#   scripts/oracle/refresh.sh [testdata/tf-oracle/NAME.tf ...]
set -e
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
cd "$root"
if [ $# -eq 0 ]; then set -- testdata/tf-oracle/*.tf; fi
for c in "$@"; do
  "$here/run-tf.sh" "$c" > "${c%.tf}.expected-tf.new" && mv "${c%.tf}.expected-tf.new" "${c%.tf}.expected-tf" || { rm -f "${c%.tf}.expected-tf.new"; echo "FAILED $c" >&2; }
done
