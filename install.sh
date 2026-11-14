#!/bin/sh
# Installs squatterd and/or tf-squatterd for the CURRENT USER ONLY. No root, no Go, nothing system-wide.
#
#   curl -fsSL https://raw.githubusercontent.com/itsnotyoutoday/squatClientDaemon/squatterbridge/install.sh | sh
#
# It asks four questions (what to install, auto-updates, which folder; answers are read from your terminal, even when piped from curl). Every question
# has a switch, so it also runs unattended:
#
#   --standalone / --no-standalone    squatterd, the always-on session holder          (default: yes)
#   --tf / --no-tf                    tf-squatterd, the TinyFugue bridge               (default: no)
#   --auto-update / --no-auto-update  squatterd checks daily and updates itself        (default: no)
#   --yes                             do not ask, use the defaults for anything not given as a switch
#   --version v0.0.1                  pick a release (default: newest, pre-releases included)
#   --dir ~/bin                       install somewhere else (default ~/.local/bin)
#
# With no terminal and no switches the defaults are used. Re-run it any time to upgrade (an up-to-date
# binary is left alone); your data in ~/.squatterd is never touched.
#
# Testing: SQUAT_RELEASE_BASE (download base URL) and SQUAT_API_BASE (releases API base) override GitHub.
set -eu

REPO="itsnotyoutoday/squatClientDaemon"
API_BASE="${SQUAT_API_BASE:-https://api.github.com}"
DL_BASE="${SQUAT_RELEASE_BASE:-https://github.com/$REPO/releases/download}"
DIR="$HOME/.local/bin"
CFGDIR="$HOME/.squatterd"
dir_given=n
VERSION=""
want_sa="" want_tf="" want_au="" yes=n

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
usage() { sed -n '2,/^set -eu/p' "$0" 2>/dev/null | sed '$d; s/^# \{0,1\}//' ; }

while [ $# -gt 0 ]; do
  case "$1" in
    --standalone) want_sa=y ;;
    --no-standalone) want_sa=n ;;
    --tf) want_tf=y ;;
    --no-tf) want_tf=n ;;
    --auto-update) want_au=y ;;
    --no-auto-update) want_au=n ;;
    --yes|-y) yes=y ;;
    --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift ;;
    --dir) [ $# -ge 2 ] || die "--dir needs a value"; DIR="$2"; dir_given=y; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
  shift
done

# --- questions -------------------------------------------------------------------------------------
have_tty=n
if [ "$yes" = n ] && (: </dev/tty) 2>/dev/null; then have_tty=y; fi

# ask VAR "question" default: keeps a value already given by a switch; otherwise asks (terminal) or
# uses the default (--yes, or no terminal).
ask() {
  eval "cur=\${$1}"
  [ -z "$cur" ] || return 0
  if [ "$have_tty" = y ]; then
    while :; do
      printf '%s [y/n] (default %s): ' "$2" "$3" >/dev/tty
      ans=""
      read -r ans </dev/tty || ans=""
      case "$ans" in
        "") ans="$3" ;;
        y|Y|yes|YES|Yes) ans=y ;;
        n|N|no|NO|No) ans=n ;;
        *) printf 'please answer y or n\n' >/dev/tty; continue ;;
      esac
      break
    done
    eval "$1=\$ans"
  else
    eval "$1=\$3"
  fi
}

ask want_sa "Set up standalone squatterd?" y
ask want_tf "Set up TinyFugue support (tf-squatterd)?" n
if [ "$want_sa" = y ]; then
  ask want_au "Enable automatic updates?" n
else
  want_au=${want_au:-n}
fi
if [ "$dir_given" = n ] && [ "$have_tty" = y ]; then
  printf 'Install the programs into which folder? (default %s): ' "$DIR" >/dev/tty
  ans=""; read -r ans </dev/tty || ans=""
  if [ -n "$ans" ]; then
    case "$ans" in "~") ans="$HOME" ;; "~/"*) ans="$HOME/${ans#\~/}" ;; esac
    DIR="$ans"
  fi
fi
case "$DIR" in /*) ;; *) DIR="$PWD/$DIR" ;; esac
[ "$want_sa" = y ] || [ "$want_tf" = y ] || die "nothing to install (you chose neither squatterd nor tf-squatterd)"

# --- environment -----------------------------------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
  get() { curl -fsSL "$1"; }
  getfile() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  get() { wget -qO- "$1"; }
  getfile() { wget -qO "$2" "$1"; }
else
  die "need curl or wget"
fi

case "$(uname -s)" in
  Linux) os=linux ;;
  FreeBSD) os=freebsd ;;
  *) die "this installer supports Linux and FreeBSD servers (got $(uname -s))" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  i386|i686) arch=386 ;;
  armv7*|armv8l) arch=armv7 ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac
label="$os-$arch"

case "$VERSION" in ""|*-*) ;; *) VERSION="${VERSION}-rel" ;; esac   # --version v0.0.1 means the release tag v0.0.1-rel
if [ -z "$VERSION" ]; then
  # newest release, pre-releases included (the API lists newest first)
  VERSION=$(get "$API_BASE/repos/$REPO/releases?per_page=1" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1) || true
  [ -n "$VERSION" ] || die "could not find a release; see https://github.com/$REPO/releases"
fi
num="${VERSION#v}"; num="${num%-rel}"   # the tag v0.0.1-rel ships files named 0.0.1
file="squatclientdaemon-$num-$label.tar.gz"

# --- which binaries need installing ----------------------------------------------------------------
bins=""
[ "$want_sa" = y ] && bins="squatterd"
[ "$want_tf" = y ] && bins="$bins tf-squatterd"

todo=""
for b in $bins; do
  have=""
  if [ -x "$DIR/$b" ]; then have=$("$DIR/$b" version 2>/dev/null | sed -n "s/^$b \([^ ]*\).*/\1/p" | head -n1) || have=""; fi
  if [ -n "$have" ] && [ "$have" = "$num" ]; then
    say "$b $num is already installed in $DIR (up to date), skipping."
  else
    todo="$todo $b"
  fi
done

if [ -n "$todo" ]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  say "Downloading $file ..."
  getfile "$DL_BASE/$VERSION/$file" "$tmp/$file" || die "download failed: $DL_BASE/$VERSION/$file"
  getfile "$DL_BASE/$VERSION/SHA256SUMS" "$tmp/SHA256SUMS" || die "could not download SHA256SUMS; refusing to install an unverified file"
  want=$(grep " \*\{0,1\}$file\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
  [ -n "$want" ] || die "SHA256SUMS has no entry for $file"
  if command -v sha256sum >/dev/null 2>&1; then have=$(sha256sum "$tmp/$file" | cut -d' ' -f1)
  elif command -v sha256 >/dev/null 2>&1; then have=$(sha256 -q "$tmp/$file")
  elif command -v shasum >/dev/null 2>&1; then have=$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1)
  else die "no sha256sum/sha256/shasum found to verify the download"; fi
  [ "$have" = "$want" ] || die "checksum mismatch, not installing"
  say "Checksum OK."

  tar -xzf "$tmp/$file" -C "$tmp"
  mkdir -p "$DIR"
  for b in $todo; do
    cp "$tmp"/squatclientdaemon-*/"$b" "$DIR/$b.new"
    chmod 755 "$DIR/$b.new"
    mv "$DIR/$b.new" "$DIR/$b"   # replace in place (safe even while the old one runs)
    say "Installed $b $num into $DIR"
  done
fi

# --- config ----------------------------------------------------------------------------------------
# One shared ~/.squatterd/config.json for both programs. Only the auto_update choice is written here.
if [ "$want_au" = y ]; then au=true; else au=false; fi
cfg="$CFGDIR/config.json"
mkdir -p "$CFGDIR"; chmod 700 "$CFGDIR" 2>/dev/null || true
if [ ! -f "$cfg" ] || [ "$(tr -d ' \t\r\n' <"$cfg")" = "{}" ]; then
  printf '{\n  "auto_update": %s,\n  "update_channel": "prerelease"\n}\n' "$au" >"$cfg"
elif grep -q '"auto_update"' "$cfg"; then
  sed "s/\"auto_update\"[[:space:]]*:[[:space:]]*[a-z]*/\"auto_update\": $au/" "$cfg" >"$cfg.new" && mv "$cfg.new" "$cfg"
else
  say "Note: $cfg exists; add \"auto_update\": $au to it by hand."
fi
chmod 600 "$cfg" 2>/dev/null || true

# --- next steps ------------------------------------------------------------------------------------
say ""
case ":$PATH:" in
  *":$DIR:"*) ;;
  *) say "Note: $DIR is not on your PATH. Add it:  echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.profile" ;;
esac
say "Next steps"
n=1
if [ "$want_sa" = y ]; then
  say "  $n. Start squatterd (keep it running; tmux/screen is fine):   $DIR/squatterd"; n=$((n+1))
  say "  $n. In a second window, get a pairing code:                   $DIR/squatterd pair"; n=$((n+1))
  say "  $n. In SquatClient: Squatters > Add a squatter > this host, port 10327, that code."; n=$((n+1))
fi
if [ "$want_tf" = y ]; then
  say "  $n. TinyFugue: tf-squatterd is started by TinyFugue itself (/squat on); make sure $DIR is on the"
  say "     PATH TinyFugue sees, or in TinyFugue: /set squat_bin=$DIR/tf-squatterd"; n=$((n+1))
fi
say ""
if [ "$au" = true ]; then
  say "Automatic updates: ON for squatterd (checks at most once a day, installs only when no session would end)."
else
  say "Automatic updates: off. Check any time with:  $DIR/squatterd update --check   (then: squatterd update)"
fi
say "Settings (auto_update, update_channel): $cfg"
