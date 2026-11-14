#!/bin/sh
# Installs squatterd and/or tf-squatterd for the CURRENT USER ONLY. No root, no Go, nothing system-wide.
#
#   curl -fsSL https://raw.githubusercontent.com/itsnotyoutoday/squatClientDaemon/main/install.sh | sh
#
# It asks five questions (what to install, auto-updates, which folder; answers are read from your terminal, even when piped from curl). Every question
# has a switch, so it also runs unattended:
#
#   --standalone / --no-standalone    squatterd, the always-on session holder          (default: yes)
#   --tinyfugue / --no-tinyfugue      build the SquatClient-ready TinyFugue from source (default: no)
#   --tf / --no-tf                    tf-squatterd, the TinyFugue bridge               (default: yes if
#                                     TinyFugue is being installed, else no)
#   --auto-update / --no-auto-update  squatterd checks daily and updates itself        (default: no)
#   --yes                             do not ask, use the defaults for anything not given as a switch
#   --version v0.0.1                  pick a release (default: newest, pre-releases included)
#   --dir ~/bin                       install somewhere else (default ~/.local/bin; 'tf' goes there too)
#   --tf-ref squatterbridge           TinyFugue branch or tag to build (default: squatterbridge)
#
# With no terminal and no switches the defaults are used. Re-run it any time to upgrade (an up-to-date
# binary is left alone); your data in ~/.squatterd is never touched.
#
# The TinyFugue build is entirely inside your home directory: the program goes into the install folder
# (as 'tf'), its library into ~/.local/share/tf-lib. It needs a C compiler, make and the development
# headers for OpenSSL, zlib, PCRE2, ncurses and ICU; if something is missing the installer says what to
# install and offers to carry on without TinyFugue.
#
# Testing: SQUAT_RELEASE_BASE (download base URL) and SQUAT_API_BASE (releases API base) override GitHub;
# SQUAT_TF_BASE overrides the TinyFugue source download base (<base>/<ref>.tar.gz); SQUAT_TF_PREFIXES
# (space separated, e.g. "/opt/homebrew") adds include/lib search prefixes for the TinyFugue build.
set -eu

REPO="itsnotyoutoday/squatClientDaemon"
API_BASE="${SQUAT_API_BASE:-https://api.github.com}"
DL_BASE="${SQUAT_RELEASE_BASE:-https://github.com/$REPO/releases/download}"
TF_BASE="${SQUAT_TF_BASE:-https://github.com/itsnotyoutoday/tinyfugue/archive}"
TF_REF="squatterbridge"
TF_SHARE="$HOME/.local/share"
DIR="$HOME/.local/bin"
CFGDIR="$HOME/.squatterd"
dir_given=n
VERSION=""
want_sa="" want_tf="" want_tfsrc="" want_au="" yes=n

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
usage() { sed -n '2,/^set -eu/p' "$0" 2>/dev/null | sed '$d; s/^# \{0,1\}//' ; }

while [ $# -gt 0 ]; do
  case "$1" in
    --standalone) want_sa=y ;;
    --no-standalone) want_sa=n ;;
    --tinyfugue) want_tfsrc=y ;;
    --no-tinyfugue) want_tfsrc=n ;;
    --tf-ref) [ $# -ge 2 ] || die "--tf-ref needs a value"; TF_REF="$2"; shift ;;
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
ask want_tfsrc "Install the SquatClient-ready TinyFugue (builds it from source into your home directory)?" n
ask want_tf "Install the TinyFugue bridge (tf-squatterd)?" "$([ "$want_tfsrc" = y ] && echo y || echo n)"
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
[ "$want_sa" = y ] || [ "$want_tf" = y ] || [ "$want_tfsrc" = y ] || die "nothing to install (you chose none of squatterd, TinyFugue, tf-squatterd)"

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

# --- which binaries need installing ----------------------------------------------------------------
bins=""
[ "$want_sa" = y ] && bins="squatterd"
[ "$want_tf" = y ] && bins="$bins tf-squatterd"

if [ -n "$bins" ]; then
  case "$VERSION" in ""|*-*) ;; *) VERSION="${VERSION}-rel" ;; esac   # --version v0.0.1 means the release tag v0.0.1-rel
  if [ -z "$VERSION" ]; then
    # newest release, pre-releases included (the API lists newest first)
    VERSION=$(get "$API_BASE/repos/$REPO/releases?per_page=1" |
      sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1) || true
    [ -n "$VERSION" ] || die "could not find a release; see https://github.com/$REPO/releases"
  fi
  num="${VERSION#v}"; num="${num%-rel}"   # the tag v0.0.1-rel ships files named 0.0.1
  file="squatclientdaemon-$num-$label.tar.gz"

fi

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

# --- TinyFugue (build from source, user level) ------------------------------------------------------
# Everything lands under $HOME: 'tf' in $DIR, its library in $TF_SHARE/tf-lib. No symlink, no manpage,
# nothing system-wide.
tf_built=n
tf_skipped=""

print_missing_help() {
  say ""
  say "TinyFugue cannot be built yet. Missing: $1"
  say "Install the build tools and development headers, then run this installer again:"
  say "  Debian/Ubuntu:  sudo apt install build-essential libssl-dev zlib1g-dev libpcre2-dev libncurses-dev libicu-dev"
  say "  Fedora/RHEL:    sudo dnf install gcc make openssl-devel zlib-devel pcre2-devel ncurses-devel libicu-devel"
  say "  Arch:           sudo pacman -S base-devel openssl zlib pcre2 ncurses icu"
  say "  FreeBSD:        sudo pkg install gmake pcre2 icu     (OpenSSL, zlib and ncurses come with the base system)"
  say "No root on this machine? Ask the administrator to install those packages."
  say ""
}

# probe_hdr HEADER: does the C compiler find it?
probe_hdr() { printf '#include <%s>\n' "$1" | $CC $INCFLAGS -E -x c - >/dev/null 2>&1; }

build_tinyfugue() {
  CC="${CC:-}"
  if [ -z "$CC" ]; then
    for c in cc gcc clang; do command -v "$c" >/dev/null 2>&1 && { CC=$c; break; }; done
  fi
  MAKE_CMD=""
  [ "$os" = freebsd ] && command -v gmake >/dev/null 2>&1 && MAKE_CMD=gmake
  [ -n "$MAKE_CMD" ] || { command -v make >/dev/null 2>&1 && MAKE_CMD=make; }
  PFX="${SQUAT_TF_PREFIXES:-}"
  [ "$os" = freebsd ] && PFX="/usr/local $PFX"
  INCFLAGS=""; for p in $PFX; do INCFLAGS="$INCFLAGS -I$p/include"; done

  missing=""
  [ -n "$CC" ] || missing="$missing, a C compiler (cc/gcc/clang)"
  [ -n "$MAKE_CMD" ] || missing="$missing, make"
  command -v tar >/dev/null 2>&1 || missing="$missing, tar"
  if [ -n "$CC" ]; then
    probe_hdr openssl/ssl.h || missing="$missing, OpenSSL development headers (openssl/ssl.h)"
    probe_hdr zlib.h || missing="$missing, zlib development headers (zlib.h)"
    if ! command -v pcre2-config >/dev/null 2>&1 && ! probe_hdr pcre2.h; then
      missing="$missing, PCRE2 development headers (pcre2.h; PCRE2, not the old PCRE)"
    fi
    if ! probe_hdr ncurses.h && ! probe_hdr curses.h && ! probe_hdr termcap.h; then
      missing="$missing, ncurses development headers"
    fi
    probe_hdr unicode/ucnv.h || missing="$missing, ICU development headers (unicode/ucnv.h)"
  fi
  if [ -n "$missing" ]; then
    print_missing_help "${missing#, }"
    return 1
  fi

  btmp=$(mktemp -d)
  mkdir -p "$btmp/src"
  say "Downloading the TinyFugue source ($TF_REF) ..."
  if ! getfile "$TF_BASE/$TF_REF.tar.gz" "$btmp/tf.tar.gz"; then
    say "Could not download $TF_BASE/$TF_REF.tar.gz"; rm -rf "$btmp"; return 1
  fi
  if ! tar -xzf "$btmp/tf.tar.gz" -C "$btmp/src"; then
    say "The TinyFugue download is not a valid archive."; rm -rf "$btmp"; return 1
  fi
  srcdir=""
  for d in "$btmp"/src/*/; do [ -f "${d}configure" ] && srcdir="${d%/}"; done
  if [ -z "$srcdir" ]; then say "The TinyFugue download has no configure script."; rm -rf "$btmp"; return 1; fi

  log="$CFGDIR/tinyfugue-build.log"
  mkdir -p "$CFGDIR"; chmod 700 "$CFGDIR" 2>/dev/null || true
  : >"$log"
  cargs="--prefix=$HOME/.local --bindir=$DIR --datadir=$TF_SHARE --disable-version --disable-symlink --enable-gmcp"
  if command -v flex >/dev/null 2>&1; then cargs="$cargs --enable-msdp"
  else say "(flex not found: building without MSDP; GMCP is included.)"; fi
  [ -z "$PFX" ] || cargs="$cargs --with-inclibpfx=$(echo $PFX | tr ' ' ',')"
  say "Building TinyFugue (a minute or so; log: $log) ..."
  if ! ( cd "$srcdir" && ./configure $cargs && $MAKE_CMD && mkdir -p "$DIR" "$TF_SHARE" &&
         $MAKE_CMD install ) >>"$log" 2>&1; then
    say "The TinyFugue build failed. Last lines of $log:"
    tail -n 15 "$log" | sed 's/^/    /'
    rm -rf "$btmp"; return 1
  fi
  rm -rf "$btmp"
  if [ ! -x "$DIR/tf" ]; then say "The TinyFugue build did not produce $DIR/tf."; return 1; fi
  # smoke test: it must start, load its library and quit on /quit (guarded so a hang cannot stall us)
  TERM="${TERM:-dumb}" "$DIR/tf" -n -f -v -c/quit </dev/null >>"$log" 2>&1 &
  tfpid=$!; waited=0
  while kill -0 "$tfpid" 2>/dev/null && [ "$waited" -lt 20 ]; do sleep 1; waited=$((waited+1)); done
  if kill -0 "$tfpid" 2>/dev/null; then kill "$tfpid" 2>/dev/null; wait "$tfpid" 2>/dev/null || true
    say "Built $DIR/tf but it did not start cleanly (see $log)."; return 1
  fi
  if ! wait "$tfpid"; then say "Built $DIR/tf but it exited with an error on start (see $log)."; return 1; fi
  say "Installed TinyFugue: $DIR/tf (starts and quits cleanly), library in $TF_SHARE/tf-lib"
  return 0
}

if [ "$want_tfsrc" = y ]; then
  if build_tinyfugue; then
    tf_built=y
  else
    tf_skipped=y
    if [ "$have_tty" = y ]; then
      printf 'Continue without TinyFugue? [y/n] (default y): ' >/dev/tty
      ans=""; read -r ans </dev/tty || ans=""
      case "$ans" in n|N|no|NO|No) die "stopped; nothing else was changed after the TinyFugue step" ;; esac
    fi
    say "Continuing without TinyFugue."
  fi
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
if [ "$tf_built" = y ]; then
  say "  $n. Start TinyFugue:                                            $DIR/tf"; n=$((n+1))
fi
if [ "$want_tf" = y ]; then
  if [ "$tf_built" = y ]; then
    say "  $n. In TinyFugue turn the bridge on and pair:                  /squat on    then    /squat pair"
  else
    say "  $n. In a TinyFugue that has the squat patch: /squat on, then /squat pair"
  fi
  say "     TinyFugue finds tf-squatterd through your PATH (put $DIR on it), or: /set squat_bin=$DIR/tf-squatterd"
  say "     (TinyFugue keeps the bridge's data in ~/.tf-squatter unless you /set squat_dir; pair from inside TinyFugue.)"
  n=$((n+1))
fi
if [ "$tf_skipped" = y ]; then
  say "  -  TinyFugue was not installed (see above); fix the missing pieces and run this installer again."
fi
say ""
if [ "$au" = true ]; then
  say "Automatic updates: ON for squatterd (checks at most once a day, installs only when no session would end)."
else
  say "Automatic updates: off. Check any time with:  $DIR/squatterd update --check   (then: squatterd update)"
fi
say "Settings (auto_update, update_channel): $cfg"
