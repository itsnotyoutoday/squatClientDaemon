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
port=0
if grep -q '^;@net' "$1"; then
  # dummy local server: accepts connections and stays silent
  "$orc" sends "$1" > "$tmp/sends.txt"
  python3 -I "$here/dummyserver.py" "$tmp/port" "$tmp/sends.txt" 2>/dev/null &
  srv=$!
  trap 's=$?; kill $srv 2>/dev/null; wait $srv 2>/dev/null || true; rm -rf "$tmp"; exit $s' EXIT
  n=0; while [ ! -s "$tmp/port" ] && [ $n -lt 50 ]; do sleep 0.1; n=$((n+1)); done
  port=$(cat "$tmp/port")
fi
"$orc" gen "$1" "$tmp/out.log" "$port" > "$tmp/case.tf"
if grep -q '^;@screen' "$1"; then
  # what is displayed, not what is logged (tf logs gagged lines)
  TF_PTY_DUMP="$tmp/screen.bin" python3 -I "$here/ptyrun.py" "$tmp/home" "$lib" "${TF_TIMEOUT:-8}" "$tfbin" -n -f"$tmp/case.tf" >/dev/null
  grep -a -q ORACLE_END "$tmp/screen.bin" || { echo "run-tf.sh: tf died or hung before the end of $1" >&2; exit 3; }
  [ -n "$TF_RAW" ] && tr "\r" "\n" < "$tmp/screen.bin" | grep -a "^%" >&2
  [ -n "$TF_RAW" ] && cp "$tmp/screen.bin" /tmp/last-screen.bin
  "$orc" normscreen < "$tmp/screen.bin"
  exit 0
fi
python3 -I "$here/ptyrun.py" "$tmp/home" "$lib" "${TF_TIMEOUT:-8}" "$tfbin" -n -f"$tmp/case.tf" >/dev/null
[ -f "$tmp/out.log" ] || { echo "run-tf.sh: tf produced no log for $1" >&2; exit 1; }
grep -q ORACLE_END "$tmp/out.log" || { echo "run-tf.sh: tf died or hung before the end of $1 (see docs/TF-ORACLE.md, crashes)" >&2; exit 3; }
[ -n "$TF_RAW" ] && cat "$tmp/out.log" >&2
"$orc" norm < "$tmp/out.log"
