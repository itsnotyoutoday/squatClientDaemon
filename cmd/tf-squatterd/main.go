// Command tf-squatterd is the remote-control bridge for TinyFugue: it serves
// TFSP (docs/TF-SQUATTER.md) to SquatClient while TinyFugue, which started it
// with "/squat on", owns the game connections.
//
//	tf-squatterd serve --fd 3 [--port 10328] [--bind ADDR] [--data-dir ~/.squatterd/tf]
//	tf-squatterd reload | pair [--role operator] | devices | revoke <deviceId> | status | update [--check] | version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/tfbridge"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/selfupdate"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// version is injected with -ldflags "-X main.version=...".
var version = "dev"

const usage = `tf-squatterd %s - remote-control bridge for TinyFugue

Usage:
  tf-squatterd serve --fd 3 [flags]   run the bridge (TinyFugue starts this: /squat on)
  tf-squatterd serve --knock [flags]  run a bridge TinyFugue did not start: it knocks at TinyFugue's rendezvous
  tf-squatterd reload                 restart the running bridge (new binary, same connections, nothing lost); if none
                                      runs, start one that attaches to TinyFugue
  tf-squatterd pair [flags]           create a one-time pairing code (offline)
  tf-squatterd devices [flags]        list paired devices
  tf-squatterd revoke <deviceId>      remove a paired device
  tf-squatterd status [flags]         show data dir, TLS fingerprint, devices
  tf-squatterd update [--check]       look for a newer release; without --check install it and reload the running
                                      bridge onto it. Settings: ~/.squatterd/config.json
  tf-squatterd version

Flags: --data-dir DIR (default ~/.squatterd/tf)  --port N (default 10328)  --bind ADDR (default 0.0.0.0; 127.0.0.1 with --insecure-loopback)
`

type options struct {
	fd               int
	port             int
	bind             string
	dataDir          string
	insecureLoopback bool
	certFile         string
	keyFile          string
	logLevel         string
	maxClients       int
	pairValid        time.Duration
	role             string
	check            bool
	channel          string
	configDir        string // where config.json lives (shared with squatterd)
	knock            bool
	args             []string // the arguments this process was started with (a reload starts the new binary with them)
}

func newFlags(name string, w io.Writer) (*flag.FlagSet, *options) {
	o := &options{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(w)
	fs.IntVar(&o.fd, "fd", 3, "inherited control socket (TinyFugue's end of the socketpair)")
	fs.IntVar(&o.port, "port", proto.DefaultTFPort, "TCP port to listen on")
	fs.StringVar(&o.bind, "bind", "", "address to bind (default 0.0.0.0, every interface; 127.0.0.1 with --insecure-loopback)")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory (default ~/.squatterd/tf; an existing ~/.tf-squatter keeps being used)")
	fs.BoolVar(&o.insecureLoopback, "insecure-loopback", false, "serve plain ws:// (only with a loopback --bind)")
	fs.StringVar(&o.certFile, "cert", "", "TLS certificate file (default: auto-generated self-signed)")
	fs.StringVar(&o.keyFile, "key", "", "TLS key file")
	fs.StringVar(&o.logLevel, "log-level", "info", "debug|info|warn|error")
	fs.IntVar(&o.maxClients, "max-clients", 32, "maximum simultaneously connected clients")
	fs.DurationVar(&o.pairValid, "valid", 0, "pair: how long the code stays valid (default 10m, max 168h)")
	fs.StringVar(&o.role, "role", "operator", "pair: role of the device (viewer|operator|admin)")
	fs.StringVar(&o.configDir, "config-dir", "", "directory of the shared config.json (default ~/.squatterd)")
	fs.BoolVar(&o.knock, "knock", false, "serve: get the control channel by knocking at TinyFugue's rendezvous instead of an inherited --fd")
	fs.BoolVar(&o.check, "check", false, "update: only look for a newer release, install nothing")
	fs.StringVar(&o.channel, "channel", "", "update: stable|prerelease (default: update_channel in config.json)")
	return fs, o
}

func (o *options) resolve() error {
	if o.bind == "" { // not given: every interface (the public address, Tailscale, the LAN); plain ws:// is only allowed on loopback
		o.bind = "0.0.0.0"
		if o.insecureLoopback {
			o.bind = "127.0.0.1"
		}
	}
	h, herr := os.UserHomeDir()
	if o.dataDir == "" {
		if herr != nil {
			return fmt.Errorf("cannot determine home directory, use --data-dir: %w", herr)
		}
		o.dataDir = DefaultDataDir(h)
	}
	if o.configDir == "" { // shared with squatterd, whatever --data-dir says (TinyFugue passes its own)
		if herr != nil {
			return fmt.Errorf("cannot determine home directory, use --config-dir: %w", herr)
		}
		o.configDir = filepath.Join(h, ".squatterd")
	}
	if (o.certFile == "") != (o.keyFile == "") {
		return errors.New("--cert and --key must be given together")
	}
	return nil
}

func parseLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.ToUpper(s))); err != nil {
		return slog.LevelInfo
	}
	return l
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	args0 := append([]string(nil), args...)
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "help":
		fmt.Fprintf(stdout, usage, version)
		return 0
	case "version":
		fmt.Fprintf(stdout, "tf-squatterd %s (TFSP over SWP v%d)\n", version, proto.Version)
		return 0
	case "serve", "pair", "devices", "revoke", "status", "update", "reload":
	default:
		fmt.Fprintf(stderr, "tf-squatterd: unknown command %q\n\n", cmd)
		fmt.Fprintf(stderr, usage, version)
		return 2
	}
	fs, o := newFlags(cmd, stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	o.args = args0
	if err := o.resolve(); err != nil {
		fmt.Fprintln(stderr, "tf-squatterd:", err)
		return 2
	}
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(ctx, o, stderr)
	case "pair":
		err = cmdPair(o, stdout)
	case "devices":
		err = cmdDevices(o, stdout)
	case "revoke":
		err = cmdRevoke(o, fs.Args(), stdout)
	case "status":
		err = cmdStatus(o, stdout)
	case "update":
		err = cmdUpdate(ctx, o, stdout, stderr)
	case "reload":
		err = cmdReload(o, stdout)
	}
	if err != nil {
		fmt.Fprintln(stderr, "tf-squatterd:", err)
		return 1
	}
	return 0
}

func cmdServe(ctx context.Context, o *options, stderr io.Writer) error {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: parseLevel(o.logLevel)}))
	so := tfbridge.ServeOptions{DataDir: o.dataDir, Bind: o.bind, Port: o.port,
		InsecureLoopback: o.insecureLoopback, CertFile: o.certFile, KeyFile: o.keyFile, Logger: log, Version: version,
		MaxClients: o.maxClients, PairTTL: o.pairValid, Knock: o.knock}
	if exe := exePath(); exe != "" {
		so.Exe, so.Args = exe, o.args
		if len(so.Args) == 0 || strings.HasPrefix(so.Args[0], "-") { // started without the word "serve"
			so.Args = append([]string{"serve"}, so.Args...)
		}
	}
	if p := os.Getenv(tfbridge.HandoffEnv); p != "" {
		// started by a reload: fd 3 is the listening socket and fd 4 the channel to the old process
		_ = os.Unsetenv(tfbridge.HandoffEnv)
		ho, err := inheritHandoff(p)
		if err != nil {
			return err
		}
		so.Handoff = ho
		return tfbridge.Serve(ctx, so)
	}
	if o.knock {
		return tfbridge.Serve(ctx, so)
	}
	f := os.NewFile(uintptr(o.fd), "tfctl")
	if f == nil {
		return fmt.Errorf("bad --fd %d", o.fd)
	}
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("--fd %d is not a socket (tf-squatterd is started by TinyFugue: /squat on): %w", o.fd, err)
	}
	so.Conn = conn
	return tfbridge.Serve(ctx, so)
}

// exePath is this binary's path as it was when the process started; a binary replaced on disk later
// (the upgrade) is then found at the same path, not as "path (deleted)".
func exePath() string {
	p, err := os.Executable()
	if err != nil {
		p = os.Args[0]
	}
	return strings.TrimSuffix(p, " (deleted)")
}

func inheritHandoff(statePath string) (*tfbridge.HandoffIn, error) {
	lf := os.NewFile(3, "listener")
	ln, err := net.FileListener(lf)
	_ = lf.Close()
	if err != nil {
		return nil, fmt.Errorf("inherited listener: %w", err)
	}
	rf := os.NewFile(4, "handoff")
	rc, err := net.FileConn(rf)
	_ = rf.Close()
	if err != nil {
		return nil, fmt.Errorf("inherited handoff channel: %w", err)
	}
	return &tfbridge.HandoffIn{Listener: ln, Ready: rc, StatePath: statePath}, nil
}

func cmdPair(o *options, w io.Writer) error {
	switch o.role {
	case wire.RoleViewer, wire.RoleOperator, wire.RoleAdmin:
	default:
		return fmt.Errorf("--role must be viewer, operator or admin")
	}
	as, err := auth.Open(o.dataDir)
	if err != nil {
		return err
	}
	cert, err := tfbridge.LoadCert(o.dataDir, o.certFile, o.keyFile)
	if err != nil {
		return err
	}
	code, exp, err := as.NewPairingRole(o.pairValid, o.role)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "<this-host>"
	}
	fmt.Fprintf(w, "Pairing code:    %s   (role: %s)\n", code, o.role)
	fmt.Fprintf(w, "Valid until:     %s (%s, single use)\n", exp.Format(time.RFC1123), time.Until(exp).Round(time.Minute))
	fmt.Fprintf(w, "TLS fingerprint: %s\n", tlsutil.CertFingerprint(cert))
	fmt.Fprintf(w, "Connect to:      wss://%s:%d/tfsp\n", host, o.port)
	return nil
}

func cmdDevices(o *options, w io.Writer) error {
	as, err := auth.Open(o.dataDir)
	if err != nil {
		return err
	}
	devs, err := as.Devices()
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		fmt.Fprintln(w, "no paired devices (in TinyFugue: /squat pair)")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "DEVICE ID\tNAME\tROLE\tPAIRED\tLAST SEEN")
	for _, d := range devs {
		role := d.Role
		if role == "" {
			role = "owner"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.DeviceID, d.Name, role, ts(d.CreatedAt), ts(d.LastSeen))
	}
	return tw.Flush()
}

func ts(ms int64) string {
	if ms == 0 {
		return "-"
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04")
}

func cmdRevoke(o *options, args []string, w io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: tf-squatterd revoke <deviceId>")
	}
	as, err := auth.Open(o.dataDir)
	if err != nil {
		return err
	}
	if err := as.Revoke(args[0]); err != nil {
		return err
	}
	fmt.Fprintf(w, "revoked %s (an already-connected client stays connected until it disconnects)\n", args[0])
	return nil
}

func cmdStatus(o *options, w io.Writer) error {
	fmt.Fprintf(w, "version:     %s\n", version)
	fmt.Fprintf(w, "data dir:    %s\n", o.dataDir)
	if v := selfupdate.LoadState(selfupdate.StatePath(o.configDir)).AvailableFor(version); v != "" {
		fmt.Fprintf(w, "update:      %s available (run: tf-squatterd update)\n", v)
	}
	if _, err := os.Stat(o.dataDir); err != nil {
		fmt.Fprintln(w, "             (does not exist yet; created on first run)")
		return nil
	}
	if o.certFile != "" || fileExists(filepath.Join(o.dataDir, "tls", "cert.pem")) {
		if c, err := tfbridge.LoadCert(o.dataDir, o.certFile, o.keyFile); err == nil {
			fmt.Fprintf(w, "fingerprint: %s\n", tlsutil.CertFingerprint(c))
		}
	} else {
		fmt.Fprintln(w, "fingerprint: (no certificate yet; generated on first serve)")
	}
	if as, err := auth.Open(o.dataDir); err == nil {
		if devs, err := as.Devices(); err == nil {
			fmt.Fprintf(w, "devices:     %d paired\n", len(devs))
		}
	}
	printRunning(o, w)
	return nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
