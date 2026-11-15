# squatClientDaemon: squatterd and the SquatClient engine

## Quickstart

You need a Linux or FreeBSD machine that stays on (a shell account, a VPS, a home server). No Go, no root, nothing system-wide: everything goes in your home directory.

**1. Install.** As your normal user:

```sh
curl -fsSL https://raw.githubusercontent.com/itsnotyoutoday/squatClientDaemon/main/install.sh | sh
```

(`wget -qO- <same url> | sh` works too.) It picks the right download for your CPU, checks its checksum and asks five questions (answers come from your terminal, even when piped from curl). Press Enter to accept a default:

```
Set up standalone squatterd? [y/n]                                  (default y)
Install the SquatClient-ready TinyFugue (builds it from source
  into your home directory)? [y/n]                                  (default n)
Install the TinyFugue bridge (tf-squatterd)? [y/n]                  (default y if you
                                                                     said yes above, else n)
Enable automatic updates? [y/n]                                     (default n)
Install the programs into which folder?                             (default ~/.local/bin)
```

**2. Start it** and leave it running (a `tmux` or `screen` window is fine):

```sh
squatterd
```

If the command is not found, `~/.local/bin` is not on your PATH; the installer prints the one line to add, or run `~/.local/bin/squatterd`.

**3. Pair SquatClient.** In a second window:

```sh
squatterd pair
```

It prints a one-time code, the TLS fingerprint and a `host:port`. In SquatClient add a squatter with that `host:port` and code, and check that the fingerprint matches (it is pinned on first use). The code is needed once per device.

Done: your game connections now stay up when SquatClient is closed. **TinyFugue users:** see [TinyFugue](#tinyfugue) below.

### Keeping it up to date

* **By hand (default).** `squatterd update --check` says whether a newer release exists; `squatterd update` downloads it, verifies its checksum, swaps the binary (the old one stays as `squatterd.old`) and reloads the running daemon onto it.
* **Automatically.** Answer yes to "automatic updates" (or set `"auto_update": true` in `~/.squatterd/config.json`). squatterd then checks once a day and installs a new release itself, but only at a moment when it would not end any of your game sessions; otherwise it waits and tries again later.
* **Never an interruption.** A reload keeps plain game connections up. Plain and TLS game connections (TLS 1.3, or TLS 1.2 with an AES-GCM or ChaCha20 suite, which is what every current game server uses) are carried across a reload without a new handshake. The exceptions are a TLS 1.2 connection with an old CBC cipher suite and a connection still being set up: an update that would end one of those is held back and told to you instead (`squatterd status` shows "update X available"; SquatClient is told too). `squatterd update --force` does it anyway.
* A `dev` build (made with `go build`) never updates.

Run the installer again at any time to upgrade or add the other program; your data in `~/.squatterd` is never touched.

### TinyFugue

Two separate questions, because they are separate things:

* **The SquatClient-ready TinyFugue** (`--tinyfugue`) is our modified TinyFugue ([fork](https://github.com/itsnotyoutoday/tinyfugue), branch `squatterbridge`) that can be paired with SquatClient. The installer downloads its source, builds it in a temporary folder and installs `tf` into the install folder you chose and its library into `~/.local/share/tf-lib`. Nothing outside your home directory is touched and nothing needs root. It takes a minute or so; the build log is kept in `~/.squatterd/tinyfugue-build.log`. Run the installer again to rebuild or update it.
* **The bridge** (`--tf`) is `tf-squatterd`, the helper TinyFugue starts when you type `/squat on`. You need it for the first one to be useful, so it defaults to yes when you chose the first. You can also install it alone to use with a TinyFugue you built yourself.

Start it with `tf` (or `~/.local/bin/tf`), then inside TinyFugue:

```
/squat on        start the bridge (TinyFugue finds tf-squatterd on your PATH, or: /set squat_bin=~/.local/bin/tf-squatterd)
/squat pair      print a one-time pairing code and the TLS fingerprint for SquatClient
```

TinyFugue keeps the bridge's keys and paired devices in `~/.tf-squatter` (override with `/set squat_dir=...`), so pair from inside TinyFugue with `/squat pair`. (A standalone `tf-squatterd pair` run from a shell defaults to `~/.squatterd/tf`, or to `~/.tf-squatter` when that exists; pass the same `--data-dir` to be sure.)

**Build requirements.** A C compiler, `make`, and the development headers for OpenSSL, zlib, PCRE2 (not the old PCRE), ncurses and ICU. The installer checks first; if something is missing it lists it and offers to carry on without TinyFugue instead of failing the whole install. Install them with:

| System | Command |
|---|---|
| Debian / Ubuntu | `sudo apt install build-essential libssl-dev zlib1g-dev libpcre2-dev libncurses-dev libicu-dev` |
| Fedora / RHEL | `sudo dnf install gcc make openssl-devel zlib-devel pcre2-devel ncurses-devel libicu-devel` |
| Arch | `sudo pacman -S base-devel openssl zlib pcre2 ncurses icu` |
| FreeBSD | `sudo pkg install gmake pcre2 icu` (OpenSSL, zlib and ncurses are in the base system) |

No root on the machine? Ask the administrator for those packages. MSDP support is included when `flex` is installed (GMCP always is).

### Installer switches

Every question has a switch, so it also runs unattended (with no terminal and no switches it uses the defaults):

| Switch | Meaning |
|---|---|
| `--standalone` / `--no-standalone` | install squatterd or not |
| `--tinyfugue` / `--no-tinyfugue` | build and install the SquatClient-ready TinyFugue (`tf`) from source, or not |
| `--tf` / `--no-tf` | install the bridge tf-squatterd or not (default: yes when TinyFugue is being installed, else no) |
| `--tf-ref REF` | TinyFugue branch or tag to build (default `squatterbridge`) |
| `--auto-update` / `--no-auto-update` | write `auto_update` into the config file |
| `--dir PATH` | install folder (default `~/.local/bin`) |
| `--version v0.0.1` | a specific release (default: the newest, pre-releases included) |
| `--yes` | do not ask; use the defaults for anything not given as a switch |

```sh
curl -fsSL https://raw.githubusercontent.com/itsnotyoutoday/squatClientDaemon/main/install.sh | sh -s -- --yes --standalone --tf --auto-update
```

Prefer to do it by hand? Download `squatclientdaemon-<version>-linux-amd64.tar.gz` (or `arm64`, `freebsd-amd64`, ...) from the [Releases page](https://github.com/itsnotyoutoday/squatClientDaemon/releases), unpack it and run the binaries. Or `make build` if you have Go. You can read the installer first: [`install.sh`](install.sh).

## About

**squatterd** is an optional persistent session holder for MUDs/MUSHes, in the spirit of `screen` or `tinyfugue`. Run it as an ordinary user on any machine; the SquatClient desktop app (or any SWP client) attaches, replays what it missed while away, and continues live. When you close the app the game connection stays up.

* The daemon **never logs in for you** and **stores no game credentials**: your client sends login lines.
* It **never reconnects automatically**. If a game connection ends, the session becomes `ended` with the reason and its full history until you dismiss it ("if it ended it ended").
* Full UTF-8, xterm-256 and 24-bit colour pass through untouched; the daemon announces the union of attached clients' capabilities (MTTS, with the PROXY flag) to the game.

This repository also holds the shared Go engine packages (`pkg/...`) used by the SquatClient app. See `docs/STRUCTURE.md`, `docs/PROTOCOL.md` (SWP v1) and `docs/DAEMON.md`.

### What is in the box

| Program | What it is | Who needs it |
|---|---|---|
| `squatterd` | A small always-on program that holds your game connections while SquatClient is closed (like `screen`). | Anyone who wants persistent sessions. |
| `tf-squatterd` | A helper that TinyFugue starts itself (`/squat on`) so SquatClient can attach to a running TinyFugue. | Only TinyFugue users. |

Both are single static files: **no Go, no root**.

## Commands

Other commands (`squatterd` with no arguments prints them all):

```
squatterd devices               list paired devices
squatterd revoke <deviceId>     remove a device (takes effect at its next connect)
squatterd status                data dir, fingerprint, paired devices, sessions on disk
squatterd reload                restart onto a new binary without losing the game connections (docs/RELOAD.md)
squatterd user ...              named users and their ssh keys (docs in SquatClient's SQUATTER-HUB.md)
squatterd version
```

`serve` flags: `--port 10327 --bind 0.0.0.0 --data-dir ~/.squatterd --cert F --key F --insecure-loopback --max-sessions 64 --max-clients 32 --log-level info`.

## Updates and settings

```
squatterd update --check        say whether a newer release exists (changes nothing)
squatterd update                download it, verify its SHA-256 against the release's SHA256SUMS, replace the
                                running binary (the old one stays as squatterd.old) and, if a squatterd is
                                running, reload it onto the new binary (docs/RELOAD.md) ...
squatterd update --force        ... even if that ends sessions
tf-squatterd update [--check]   the same for the TinyFugue bridge; a running bridge is reloaded onto the new binary
tf-squatterd reload             restart the TinyFugue bridge in place (game connections are TinyFugue's: nothing drops;
                                with no bridge running, start one that attaches; docs/TF-SQUATTER.md 13b)
```

The reload only happens when it would not end any session. A reload carries plain and TLS game connections across;
it cannot carry a TLS 1.2 CBC-suite connection or one still connecting: if there are such sessions, `squatterd update` installs the new binary, lists
those sessions, and leaves the running daemon alone; `squatterd reload` (or a restart) switches when you choose.
A `dev` build (`go build` without a version) never updates.

**Automatic updates** (`auto_update` on): the running squatterd looks for a newer release at most once every 24
hours and installs it only when no session would end; otherwise it logs it, remembers it, and tries again each
hour without asking the network again. The remembered version shows in `squatterd status` and is sent to
clients in the SWP `welcome` message as `updateAvailable`.

### Settings: `~/.squatterd/config.json`

One small file read by both programs (the installer writes it; edit it by hand any time, and restart for
`auto_update` to take effect):

```json
{
  "auto_update": false,
  "update_channel": "prerelease"
}
```

| Field | Values | Default |
|---|---|---|
| `auto_update` | `true` / `false` | `false` |
| `update_channel` | `"stable"` (ignore pre-releases) or `"prerelease"` (newest release of any kind) | `"prerelease"` while only pre-releases exist |

Command-line flags win over the file: `--auto-update` / `--no-auto-update` (serve) and `--channel stable|prerelease`.
A missing file or field means the default; unknown fields are ignored; an unreadable file gives a warning and the
defaults. When squatterd runs with `--data-dir DIR` its file is `DIR/config.json`; tf-squatterd always reads
`~/.squatterd/config.json` (override: `--config-dir`).

## TinyFugue scripting in the client engine

`pkg/engine` (the brain of SquatClient) scripts sessions with TinyFugue's language through the shared
[gtfparser](https://github.com/itsnotyoutoday/gtfparser) library, via the one adapter package `pkg/tfengine`
(the in-house interpreter that used to live in `pkg/tf` is gone). `squatterd` itself runs no TF scripts. See
`docs/TF-ENGINE.md` (architecture, policy and limits, upgrading the engine), `docs/TF-ROUTING.md` (what is run
locally and what reaches a remote TinyFugue; `/tf`, `/local`, `/stop`) and `docs/COMMAND-STATUS.md` (every
TinyFugue command and how this client answers it).

## tf-squatterd: the TinyFugue bridge

`make build` also produces **`tf-squatterd`**, the helper that lets a running [TinyFugue](https://github.com/itsnotyoutoday/tinyfugue)
(branch `squatterbridge`) be paired with SquatClient: TinyFugue keeps the game connections and runs its own triggers,
and the app attaches to them. You do not start it yourself: put it on your `PATH` (or `/set squat_bin=...` in
TinyFugue) and run `/squat on` and `/squat pair` inside TinyFugue. It listens on every interface, port `10328` (`/tfsp`; `--bind 127.0.0.1` keeps it on the machine, and plain `ws://` mode defaults to loopback), with the
same TLS pinning and ed25519 device keys as `squatterd`. Its data (devices, certificate, sessions) lives in `~/.squatterd/tf/`, apart from squatterd's files in `~/.squatterd/` (they would collide). An older install that used `~/.tf-squatter` keeps using that directory in place, untouched, as long as `~/.squatterd/tf` does not exist, so existing pairings and the TLS fingerprint survive; TinyFugue's `squat_dir` variable overrides the location. Build and pairing steps are in the
[SquatClient README](https://github.com/itsnotyoutoday/squatclient#tinyfugue-and-the-tf-bridge); the protocol is in
`docs/TF-SQUATTER.md`.

## Security model

* **Transport:** TLS by default with a self-signed ECDSA P-256 certificate generated on first run (`<data-dir>/tls/`, key mode 0600). Clients pin its SHA-256 fingerprint on first use. Use `--cert/--key` for your own certificate.
* **Authentication:** every connection gets a random 32-byte challenge; the client proves possession of an ed25519 key by signing it. Unknown keys are accepted only with a valid one-time pairing code (`ABCD-EFGH`, 10 minutes, single use) created by `squatterd pair` on the host. Failed attempts: 5 per 5 minutes per IP, then a 5 minute lockout. Secrets are compared in constant time.
* **Plain ws://** is refused unless you pass `--insecure-loopback` AND `--bind` is a loopback address (use it behind an ssh tunnel, below).
* Frames are capped at 1 MiB, every queue is bounded and a client that stops reading is dropped without affecting sessions. Anyone who can pair is the `owner` and can open connections to any host from this machine: only pair devices you trust, and revoke lost ones.

## Data directory

```
~/.squatterd/
  config.json                       settings shared with tf-squatterd (auto_update, update_channel)
  update-state.json                 when the last update check ran, and the newer version it found
  tf/                               tf-squatterd's own data (same layout: devices.json, tls/, sessions/)
  devices.json  pairing.json        paired devices / pending codes (0600)
  tls/cert.pem  tls/key.pem
  sessions/s-3f9a1c/meta.json       session description, end reason, per-device read marks
  sessions/s-3f9a1c/log.ndjson      full scrollback (append-only)
```

Details in `docs/DAEMON.md`.

## Reaching it through an ssh tunnel

On the server: `squatterd --bind 127.0.0.1 --insecure-loopback --port 10327`. On your desktop: `ssh -N -L 10327:127.0.0.1:10327 you@server`, then point SquatClient at `ws://127.0.0.1:10327/swp` (the app allows plain ws only to loopback). Pair as usual (`squatterd pair` on the server).

## systemd user unit

`~/.config/systemd/user/squatterd.service`:

```ini
[Unit]
Description=squatterd persistent MUD sessions

[Service]
ExecStart=%h/bin/squatterd serve --port 10327
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```

`systemctl --user enable --now squatterd` (and `loginctl enable-linger $USER` to keep it running after logout). Note that a restart ends live sessions as `squatter_stopped`; their history is kept.

## Docker

`docker build -t squatterd .` then `docker run -d -p 10327:10327 -v squatterd-data:/data squatterd`. Run `docker exec <ctr> /squatterd pair --data-dir /data`.

## FAQ

**What happens when the game closes the connection?** The session stays listed as `ended` with a reason (`remote_closed`, `reset`, `timeout`, `tls_error`, `connect_failed`, `client_quit`, `squatter_stopped`), the last text the game sent and the whole history. Dismiss it from the client to delete it. Nothing reconnects by itself.

**Does it autoreconnect or auto-login?** No, never. **Does it store passwords?** No. Lines you send are relayed; while a game asks for no echo (password prompts) the echo shown to clients and stored in history is masked.

**What if squatterd restarts?** Live game connections cannot survive a process restart. They are loaded as `ended` / `squatter_stopped` with full history.

**Can several clients attach to one session?** Yes. Input echo is shown to all of them, each device has its own read marker (`unread`), and the most recent window resize wins.

## Development

```sh
make race        # go test -race ./...
make fuzz        # SWP frame parser fuzz target (FUZZTIME=20s)
make lint        # go vet + gofmt + staticcheck
make dist        # cross-compile linux/darwin/windows x amd64/arm64 into dist/
make verify-live # pair with a real squatterd and talk to a live MUD (needs network)
```

Licensed under the terms in `LICENSE`.
