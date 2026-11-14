package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

func rootCtx() context.Context { return context.Background() }

type options struct {
	port             int
	bind             string
	dataDir          string
	insecureLoopback bool
	certFile         string
	keyFile          string
	logLevel         string
	maxSessions      int
	maxClients       int
	pairValid        time.Duration
}

func newFlags(name string, w io.Writer) (*flag.FlagSet, *options) {
	o := &options{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(w)
	fs.IntVar(&o.port, "port", proto.DefaultPort, "TCP port to listen on")
	fs.StringVar(&o.bind, "bind", "0.0.0.0", "address to bind")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory (default ~/.squatterd)")
	fs.BoolVar(&o.insecureLoopback, "insecure-loopback", false, "serve plain ws:// (only allowed with a loopback --bind)")
	fs.StringVar(&o.certFile, "cert", "", "TLS certificate file (default: auto-generated self-signed)")
	fs.StringVar(&o.keyFile, "key", "", "TLS key file")
	fs.StringVar(&o.logLevel, "log-level", "info", "debug|info|warn|error")
	fs.IntVar(&o.maxSessions, "max-sessions", 64, "maximum sessions (live + ended)")
	fs.IntVar(&o.maxClients, "max-clients", 32, "maximum simultaneously connected clients")
	fs.DurationVar(&o.pairValid, "valid", 0, "pair: how long the code stays valid, e.g. 30m, 2h, 24h (default 10m, max 168h)")
	return fs, o
}

func defaultDataDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".squatterd"), nil
}

func (o *options) resolve() error {
	if o.dataDir == "" {
		d, err := defaultDataDir()
		if err != nil {
			return fmt.Errorf("cannot determine home directory, use --data-dir: %w", err)
		}
		o.dataDir = d
	}
	if (o.certFile == "") != (o.keyFile == "") {
		return errors.New("--cert and --key must be given together")
	}
	return nil
}

func isLoopbackBind(bind string) bool {
	if bind == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(bind, "[]"))
	return ip != nil && ip.IsLoopback()
}

// checkTransport enforces: plain ws only with --insecure-loopback AND a loopback bind.
func (o *options) checkTransport() error {
	if o.insecureLoopback && !isLoopbackBind(o.bind) {
		return fmt.Errorf("--insecure-loopback requires --bind to be a loopback address (got %q)", o.bind)
	}
	return nil
}

func (o *options) cert() (tlsCert, error) {
	if o.certFile != "" {
		return tlsutil.Load(o.certFile, o.keyFile)
	}
	return tlsutil.LoadOrCreate(filepath.Join(o.dataDir, "tls"))
}

func parseLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.ToUpper(s))); err != nil {
		return slog.LevelInfo
	}
	return l
}

// run is the testable entry point. ready, if non-nil, receives the listen address once serving.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, ready func(addr string)) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "help":
		printUsage(stdout)
		return 0
	case "version":
		fmt.Fprintf(stdout, "squatterd %s (SWP v%d)\n", version, proto.Version)
		return 0
	case "user":
		return cmdUser(args, os.Stdin, stdout, stderr)
	case "serve", "pair", "devices", "revoke", "status":
	default:
		fmt.Fprintf(stderr, "squatterd: unknown command %q\n\n", cmd)
		printUsage(stderr)
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
		fmt.Fprintln(stderr, "squatterd:", err)
		return 2
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, o, stderr, ready)
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
		fmt.Fprintln(stderr, "squatterd:", err)
		return 1
	}
	return 0
}

func serve(ctx context.Context, o *options, stderr io.Writer, ready func(string)) error {
	if err := o.checkTransport(); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: parseLevel(o.logLevel)}))
	if err := os.MkdirAll(o.dataDir, 0o700); err != nil {
		return err
	}
	as, err := auth.Open(o.dataDir)
	if err != nil {
		return err
	}
	d, err := daemon.New(daemon.Config{DataDir: o.dataDir, Logger: log, MaxSessions: o.maxSessions})
	if err != nil {
		return err
	}
	// the TLS certificate is loaded first: its fingerprint is part of what an ssh-key login signs
	var cert tlsCert
	var binding []byte
	if !o.insecureLoopback {
		if cert, err = o.cert(); err != nil {
			_ = d.Shutdown(context.Background())
			return err
		}
		binding = []byte(tlsutil.CertFingerprint(cert))
	}
	srv := wire.NewServer(wire.ServerConfig{Backend: d, Auth: as, ServerName: "squatterd/" + version, Logger: log, MaxClients: o.maxClients, TLSBinding: binding})

	ln, err := net.Listen("tcp", net.JoinHostPort(o.bind, fmt.Sprint(o.port)))
	if err != nil {
		_ = d.Shutdown(context.Background())
		return err
	}
	scheme := "wss"
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn)}
	if o.insecureLoopback {
		scheme = "ws"
	} else {
		hs.TLSConfig = tlsutil.ServerConfig(cert)
		log.Info("TLS fingerprint", "sha256", tlsutil.CertFingerprint(cert))
		ln = tlsListener(ln, hs.TLSConfig)
	}
	log.Info("squatterd listening", "version", version, "url", fmt.Sprintf("%s://%s/swp", scheme, ln.Addr()), "data-dir", o.dataDir)
	if ready != nil {
		ready(ln.Addr().String())
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()

	select {
	case <-ctx.Done():
		log.Info("shutting down: ending live sessions as squatter_stopped")
	case err := <-errc:
		log.Error("server stopped", "err", err)
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Shutdown(sctx); err != nil { // clients still connected see the final session.state
		log.Error("daemon shutdown", "err", err)
	}
	srv.Close()
	_ = hs.Shutdown(sctx)
	log.Info("bye")
	return nil
}

func tlsFingerprint(c tlsCert) string { return tlsutil.CertFingerprint(c) }
