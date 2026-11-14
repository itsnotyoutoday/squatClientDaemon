# squatClientDaemon: squatterd and the SquatClient engine

**squatterd** is an optional persistent session holder for MUDs/MUSHes, in the spirit of `screen` or `tinyfugue`. Run it as an ordinary user on any machine; the SquatClient desktop app (or any SWP client) attaches, replays what it missed while away, and continues live. When you close the app the game connection stays up.

* The daemon **never logs in for you** and **stores no game credentials**: your client sends login lines.
* It **never reconnects automatically**. If a game connection ends, the session becomes `ended` with the reason and its full history until you dismiss it ("if it ended it ended").
* Full UTF-8, xterm-256 and 24-bit colour pass through untouched; the daemon announces the union of attached clients' capabilities (MTTS, with the PROXY flag) to the game.

This repository also holds the shared Go engine packages (`pkg/...`) used by the SquatClient app. See `docs/STRUCTURE.md`, `docs/PROTOCOL.md` (SWP v1) and `docs/DAEMON.md`.

## Quickstart

```sh
make build                      # static binary ./squatterd (CGO_ENABLED=0)
./squatterd --port 10327        # serve (default port 10327, TLS, data in ~/.squatterd)

# on the same host, in another shell:
./squatterd pair                # prints a one-time code, its expiry, the TLS fingerprint and a host:port hint
```

In SquatClient add a squatter with the `host:port` and the pairing code, and confirm the TLS fingerprint matches what `squatterd pair` printed (it is pinned on first use). The client generates an ed25519 key; the daemon remembers its public key, so the code is needed only once per device.

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

## tf-squatterd: the TinyFugue bridge

`make build` also produces **`tf-squatterd`**, the helper that lets a running [TinyFugue](https://github.com/itsnotyoutoday/tinyfugue)
(branch `squatterbridge`) be paired with SquatClient: TinyFugue keeps the game connections and runs its own triggers,
and the app attaches to them. You do not start it yourself: put it on your `PATH` (or `/set squat_bin=...` in
TinyFugue) and run `/squat on` and `/squat pair` inside TinyFugue. It listens on `127.0.0.1:10328` (`/tfsp`) with the
same TLS pinning and ed25519 device keys as `squatterd`. Build and pairing steps are in the
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
