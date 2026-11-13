#!/bin/sh
# Run one case through REAL TinyFugue and print canonical output.
#   usage: scripts/oracle/run-tf.sh testdata/tf-oracle/NAME.tf
# Env: TF_ORACLE_DIR (default $TMPDIR-ish scratch dir holding src/ built tf).
# See docs/TF-ORACLE.md.
set -e
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
dir=${TF_ORACLE_DIR:-$root/.tf-oracle}
tfbin=${TF_BIN:-$dir/src/src/tf}
lib=${TF_LIBDIR:-$dir/src/lib/tf}
[ -x "$tfbin" ] || { echo "run-tf.sh: no tf binary at $tfbin (see docs/TF-ORACLE.md)" >&2; exit 1; }
orc=${ORACLE_BIN:-$dir/oracle}
if [ ! -x "$orc" ] || [ -n "$(find "$here" -name '*.go' -newer "$orc")" ]; then
  (cd "$root" && go build -o "$orc" ./scripts/oracle)
fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/home"
"$orc" gen "$1" "$tmp/out.log" > "$tmp/case.tf"
python3 -I "$here/ptyrun.py" "$tmp/home" "$lib" "${TF_TIMEOUT:-8}" "$tfbin" -n -f"$tmp/case.tf" >/dev/null
[ -f "$tmp/out.log" ] || { echo "run-tf.sh: tf produced no log for $1" >&2; exit 1; }
grep -q ORACLE_END "$tmp/out.log" || { echo "run-tf.sh: tf died or hung before the end of $1 (see docs/TF-ORACLE.md, crashes)" >&2; exit 3; }
[ -n "$TF_RAW" ] && cat "$tmp/out.log" >&2
"$orc" norm < "$tmp/out.log"
