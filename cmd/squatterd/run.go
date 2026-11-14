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
	"sync"
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
	case "serve", "pair", "devices", "revoke", "status", "reload":
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
	case "reload":
		err = cmdReload(o, stdout)
	}
	if err != nil {
		fmt.Fprintln(stderr, "squatterd:", err)
		return 1
	}
	return 0
}

// server is the running squatterd: the daemon, the wire server and the HTTP server around one TCP
// listener. It can be handed to a new process (reload_unix.go) and, if that fails, started again.
type server struct {
	o       *options
	log     *slog.Logger
	as      *auth.Store
	cert    tlsCert
	binding []byte
	scheme  string

	mu         sync.Mutex
	d          *daemon.Daemon
	srv        *wire.Server
	hs         *http.Server
	rawLn      *net.TCPListener
	generation int // 0 for a fresh start, n after n reloads
	errc       chan error
	handedOver chan struct{} // closed when a new process took over: this one just exits
	reloadMu   sync.Mutex
}

// startServing creates the daemon (continuing a handoff if h is set) and serves on rawLn.
func (s *server) startServing(rawLn *net.TCPListener, h *daemon.Handoff) error {
	d, err := daemon.New(daemon.Config{DataDir: s.o.dataDir, Logger: s.log, MaxSessions: s.o.maxSessions, Handoff: h})
	if err != nil {
		return err
	}
	srv := wire.NewServer(wire.ServerConfig{Backend: d, Auth: s.as, ServerName: "squatterd/" + version, Logger: s.log, MaxClients: s.o.maxClients, TLSBinding: s.binding})
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelWarn)}
	var ln net.Listener = rawLn
	if !s.o.insecureLoopback {
		hs.TLSConfig = tlsutil.ServerConfig(s.cert)
		ln = tlsListener(ln, hs.TLSConfig)
	}
	s.mu.Lock()
	s.d, s.srv, s.hs, s.rawLn = d, srv, hs, rawLn
	s.mu.Unlock()
	go func() {
		// closing the server on purpose (a reload) is not a failure
		if err := hs.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			s.errc <- err
		}
	}()
	return nil
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
	s := &server{o: o, log: log, as: as, scheme: "wss", errc: make(chan error, 4), handedOver: make(chan struct{})}
	// the TLS certificate is loaded first: its fingerprint is part of what an ssh-key login signs
	if o.insecureLoopback {
		s.scheme = "ws"
	} else {
		if s.cert, err = o.cert(); err != nil {
			return err
		}
		s.binding = []byte(tlsutil.CertFingerprint(s.cert))
		log.Info("TLS fingerprint", "sha256", tlsutil.CertFingerprint(s.cert))
	}

	// started by "squatterd reload": take over the listener and the game connections of the old process
	var (
		h      *daemon.Handoff
		rawLn  *net.TCPListener
		readyW *os.File
	)
	if path := os.Getenv(handoffEnv); path != "" {
		if h, rawLn, readyW, err = takeOver(path, s); err != nil {
			return err
		}
		defer h.Close()
	} else {
		if err := checkNoOtherServer(o.dataDir); err != nil {
			return err
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(o.bind, fmt.Sprint(o.port)))
		if err != nil {
			return err
		}
		rawLn = ln.(*net.TCPListener)
	}
	if err := s.startServing(rawLn, h); err != nil {
		_ = rawLn.Close()
		return err
	}
	log.Info("squatterd listening", "version", version, "url", fmt.Sprintf("%s://%s/swp", s.scheme, rawLn.Addr()), "data-dir", o.dataDir, "generation", s.generation)
	stopControl, err := s.startControl(ctx)
	if err != nil {
		log.Warn("control socket not available (squatterd reload will not work)", "err", err)
	}
	if readyW != nil { // tell the old process we are serving
		_, _ = readyW.WriteString("ready\n")
		_ = readyW.Close()
		_ = os.Remove(filepath.Join(o.dataDir, handoffFile))
	}
	if ready != nil {
		ready(rawLn.Addr().String())
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down: ending live sessions as squatter_stopped")
	case err := <-s.errc:
		log.Error("server stopped", "err", err)
	case <-s.handedOver:
		log.Info("handed over to a new squatterd process; exiting")
		if stopControl != nil {
			stopControl(false)
		}
		return nil
	}
	if stopControl != nil {
		stopControl(true)
	}
	s.mu.Lock()
	d, srv, hs := s.d, s.srv, s.hs
	s.mu.Unlock()
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
