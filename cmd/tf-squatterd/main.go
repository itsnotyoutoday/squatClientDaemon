// Command tf-squatterd is the remote-control bridge for TinyFugue: it serves
// TFSP (docs/TF-SQUATTER.md) to SquatClient while TinyFugue, which started it
// with "/squat on", owns the game connections.
//
//	tf-squatterd serve --fd 3 [--port 10328] [--bind 127.0.0.1] [--data-dir ~/.tf-squatter]
//	tf-squatterd pair [--role operator] | devices | revoke <deviceId> | status | version
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
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// version is injected with -ldflags "-X main.version=...".
var version = "dev"

const usage = `tf-squatterd %s - remote-control bridge for TinyFugue

Usage:
  tf-squatterd serve --fd 3 [flags]   run the bridge (TinyFugue starts this: /squat on)
  tf-squatterd pair [flags]           create a one-time pairing code (offline)
  tf-squatterd devices [flags]        list paired devices
  tf-squatterd revoke <deviceId>      remove a paired device
  tf-squatterd status [flags]         show data dir, TLS fingerprint, devices
  tf-squatterd version

Flags: --data-dir DIR (default ~/.tf-squatter)  --port N (default 10328)  --bind ADDR (default 127.0.0.1)
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
}

func newFlags(name string, w io.Writer) (*flag.FlagSet, *options) {
	o := &options{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(w)
	fs.IntVar(&o.fd, "fd", 3, "inherited control socket (TinyFugue's end of the socketpair)")
	fs.IntVar(&o.port, "port", proto.DefaultTFPort, "TCP port to listen on")
	fs.StringVar(&o.bind, "bind", "127.0.0.1", "address to bind")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory (default ~/.tf-squatter)")
	fs.BoolVar(&o.insecureLoopback, "insecure-loopback", false, "serve plain ws:// (only with a loopback --bind)")
	fs.StringVar(&o.certFile, "cert", "", "TLS certificate file (default: auto-generated self-signed)")
	fs.StringVar(&o.keyFile, "key", "", "TLS key file")
	fs.StringVar(&o.logLevel, "log-level", "info", "debug|info|warn|error")
	fs.IntVar(&o.maxClients, "max-clients", 32, "maximum simultaneously connected clients")
	fs.DurationVar(&o.pairValid, "valid", 0, "pair: how long the code stays valid (default 10m, max 168h)")
	fs.StringVar(&o.role, "role", "operator", "pair: role of the device (viewer|operator|admin)")
	return fs, o
}

func (o *options) resolve() error {
	if o.dataDir == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot determine home directory, use --data-dir: %w", err)
		}
		o.dataDir = filepath.Join(h, ".tf-squatter")
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
	case "serve", "pair", "devices", "revoke", "status":
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
	}
	if err != nil {
		fmt.Fprintln(stderr, "tf-squatterd:", err)
		return 1
	}
	return 0
}

func cmdServe(ctx context.Context, o *options, stderr io.Writer) error {
	f := os.NewFile(uintptr(o.fd), "tfctl")
	if f == nil {
		return fmt.Errorf("bad --fd %d", o.fd)
	}
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("--fd %d is not a socket (tf-squatterd is started by TinyFugue: /squat on): %w", o.fd, err)
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: parseLevel(o.logLevel)}))
	return tfbridge.Serve(ctx, tfbridge.ServeOptions{Conn: conn, DataDir: o.dataDir, Bind: o.bind, Port: o.port,
		InsecureLoopback: o.insecureLoopback, CertFile: o.certFile, KeyFile: o.keyFile, Logger: log, Version: version,
		MaxClients: o.maxClients, PairTTL: o.pairValid})
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
	return nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
