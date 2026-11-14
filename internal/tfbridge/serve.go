package tfbridge

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// ServeOptions configures Serve.
type ServeOptions struct {
	Conn             net.Conn // control channel to TinyFugue (the inherited socketpair end)
	DataDir          string
	Bind             string
	Port             int
	InsecureLoopback bool
	CertFile         string
	KeyFile          string
	Logger           *slog.Logger
	Version          string
	MaxClients       int
	PairTTL          time.Duration
	// Ready, if set, receives the listen address once serving.
	Ready func(addr string)

	// Knock: there is no inherited channel; obtain one from TinyFugue's rendezvous in DataDir (a bridge
	// started by hand after the old one died).
	Knock bool
	// Handoff: this process was started by the reload of another bridge (see reload_unix.go).
	Handoff *HandoffIn
	// Exe and Args are what a reload starts: this binary, with the arguments this process was started with.
	// Empty disables reload.
	Exe  string
	Args []string
}

// HandoffIn is what a reload gives the new process: the listening socket, a duplex channel to the old
// process and the state file.
type HandoffIn struct {
	Listener  net.Listener
	Ready     net.Conn
	StatePath string
}

// IsLoopback reports whether a bind address is loopback-only.
func IsLoopback(bind string) bool {
	if bind == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(bind, "[]"))
	return ip != nil && ip.IsLoopback()
}

// LoadCert returns the TLS certificate for a data dir (or the given files).
func LoadCert(dataDir, certFile, keyFile string) (tls.Certificate, error) {
	if certFile != "" {
		return tlsutil.Load(certFile, keyFile)
	}
	return tlsutil.LoadOrCreate(filepath.Join(dataDir, "tls"))
}

// service is one running tf-squatterd: the bridge, the HTTP/WebSocket server in front of it and the
// reload machinery.
type service struct {
	o   ServeOptions
	log *slog.Logger
	as  *auth.Store
	b   *Bridge

	tlsCfg   *tls.Config
	fp, hint string
	instance string

	mu       sync.Mutex
	hs       *http.Server
	srv      *wire.Server
	rawLn    *net.TCPListener
	serveErr chan error

	startedAt    int64
	serviceSince int64
	generation   int
	last         *proto.ReloadResult

	reloadMu   sync.Mutex
	reloading  atomic.Bool
	handedOver chan struct{}
	handOnce   sync.Once
	reloadEnd  chan error // a reload that passed the point of no return and failed
}

func (s *service) startHTTP(ln *net.TCPListener) {
	hs := &http.Server{ReadHeaderTimeout: 10 * time.Second, ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelWarn)}
	var l net.Listener = ln
	if s.tlsCfg != nil {
		hs.TLSConfig = s.tlsCfg
		l = tls.NewListener(ln, s.tlsCfg)
	}
	srv := wire.NewServer(s.b.ServerConfig(s.as, "tf-squatterd/"+s.o.Version, s.instance, s.o.MaxClients, s.log))
	hs.Handler = srv.Handler()
	s.mu.Lock()
	s.hs, s.srv, s.rawLn = hs, srv, ln
	s.mu.Unlock()
	go func() {
		if err := hs.Serve(l); !errors.Is(err, http.ErrServerClosed) {
			select {
			case s.serveErr <- err:
			default:
			}
		}
	}()
}

// stopHTTP stops accepting and drops every client (closing the listener).
func (s *service) stopHTTP() {
	s.mu.Lock()
	hs, srv := s.hs, s.srv
	s.hs, s.srv = nil, nil
	s.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
	if hs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = hs.Shutdown(ctx)
		cancel()
		_ = hs.Close()
	}
}

// Serve runs tf-squatterd until ctx is cancelled or TinyFugue closes the
// control channel. It returns nil on a clean stop (and when a reload handed
// everything to a new process).
func Serve(ctx context.Context, o ServeOptions) error {
	if o.InsecureLoopback && !IsLoopback(o.Bind) {
		return fmt.Errorf("--insecure-loopback requires --bind to be a loopback address (got %q)", o.Bind)
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return err
	}
	as, err := auth.Open(o.DataDir)
	if err != nil {
		return err
	}
	s := &service{o: o, log: log, as: as, startedAt: nowMs(), serviceSince: nowMs(), serveErr: make(chan error, 1),
		handedOver: make(chan struct{}), reloadEnd: make(chan error, 1)}

	// the TLS identity first: a bad certificate must fail before anything else is touched
	scheme := "wss"
	if o.InsecureLoopback {
		scheme = "ws"
	} else {
		cert, err := LoadCert(o.DataDir, o.CertFile, o.KeyFile)
		if err != nil {
			return err
		}
		s.tlsCfg = tlsutil.ServerConfig(cert)
		s.fp = tlsutil.CertFingerprint(cert)
	}

	var restore *State
	var ln *net.TCPListener
	var k *Knocked
	var ho *HandoffIn = o.Handoff
	switch {
	case ho != nil:
		// started by a reload: validate everything that can fail BEFORE saying ready, so the old
		// process can still roll back
		draft, err := ReadState(ho.StatePath)
		if err != nil {
			return fmt.Errorf("handoff state refused: %w", err)
		}
		tl, ok := ho.Listener.(*net.TCPListener)
		if !ok {
			return errors.New("handoff: inherited listener is not TCP")
		}
		ln = tl
		if k, err = Knock(o.DataDir, 5*time.Second); err != nil {
			return err
		}
		if draft.Bridge.Instance != k.Instance {
			k.Close()
			return fmt.Errorf("handoff state is from another TinyFugue (instance %s, this one is %s)", draft.Bridge.Instance, k.Instance)
		}
		s.generation = draft.Generation
		restore = &draft
	case o.Knock:
		if err := checkNoOtherServer(o.DataDir); err != nil {
			return err
		}
		if k, err = Knock(o.DataDir, 5*time.Second); err != nil {
			return err
		}
		l, err := net.Listen("tcp", net.JoinHostPort(o.Bind, fmt.Sprint(o.Port)))
		if err != nil {
			k.Close()
			return err
		}
		ln = l.(*net.TCPListener)
	default:
		if err := checkNoOtherServer(o.DataDir); err != nil {
			return err
		}
		l, err := net.Listen("tcp", net.JoinHostPort(o.Bind, fmt.Sprint(o.Port)))
		if err != nil {
			return err
		}
		ln = l.(*net.TCPListener)
	}
	defer func() {
		s.mu.Lock()
		hs := s.hs
		s.mu.Unlock()
		if hs == nil && ln != nil {
			_ = ln.Close()
		}
	}()

	host := o.Bind
	if ip := net.ParseIP(strings.Trim(o.Bind, "[]")); host == "" || (ip != nil && ip.IsUnspecified()) {
		if h, _ := os.Hostname(); h != "" {
			host = h
		} else {
			host = "<this-host>"
		}
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	s.hint = fmt.Sprintf("%s://%s:%s/tfsp", scheme, host, port)

	cfg := Config{DataDir: o.DataDir, Logger: log, Auth: as, Fingerprint: s.fp, Hint: s.hint, PairTTL: o.PairTTL, Version: o.Version}
	runErr := make(chan error, 1)
	switch {
	case ho != nil:
		final, err := s.handoffIn(ho, k, restore)
		if err != nil {
			k.Close()
			return err
		}
		cfg.Restore = final
		if s.b, err = NewAttached(cfg, k); err != nil {
			k.Close()
			return err
		}
		if final != nil {
			s.last = &proto.ReloadResult{At: nowMs(), Ok: true, Generation: s.generation, Carried: len(final.Sessions),
				DurationMs: nowMs() - final.CreatedAt}
		}
		go func() { runErr <- s.b.Run() }()
		if final == nil { // the old process never delivered its state: ask TinyFugue to tell us everything again
			s.log.Warn("tfbridge: no state from the previous process; asking TinyFugue to announce its sessions again")
			_ = s.b.ctl.send("RESYNC")
		}
		s.instance = k.Instance
	case o.Knock:
		if s.b, err = NewAttached(cfg, k); err != nil {
			k.Close()
			return err
		}
		if err := k.Attach(); err != nil {
			k.Close()
			return err
		}
		go func() { runErr <- s.b.Run() }()
		_ = s.b.ctl.send("RESYNC") // a fresh bridge knows no session
		s.instance = k.Instance
	default:
		if s.b, err = New(cfg, o.Conn); err != nil {
			return err
		}
		go func() { runErr <- s.b.Run() }()
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		s.instance, err = s.b.WaitReady(wctx)
		wcancel()
		if err != nil {
			s.b.Close()
			return err
		}
	}
	b := s.b
	if o.Exe != "" {
		b.SetReloader(s)
	}

	s.startHTTP(ln)
	log.Info("tf-squatterd listening", "url", s.hint, "instance", s.instance, "data-dir", o.DataDir)
	if s.fp != "" {
		log.Info("TLS fingerprint", "sha256", s.fp)
	}
	stopControl := s.startControl(ctx)
	defer stopControl()
	if ho != nil {
		_, _ = fmt.Fprintln(ho.Ready, "up") // the old process may exit now
	}
	if o.Ready != nil {
		o.Ready(ln.Addr().String())
	}
	stopSignals := s.watchSignals()
	defer stopSignals()

	var result error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case <-b.Done():
		if s.reloading.Load() {
			// the channel ended because TinyFugue switched to a new bridge: wait for the outcome
			select {
			case <-s.handedOver:
				log.Info("handed over to a new tf-squatterd")
				return nil
			case result = <-s.reloadEnd:
				log.Error("reload failed after TinyFugue switched channels", "err", result)
				s.stopHTTP()
				return result
			}
		}
		log.Info("TinyFugue closed the control channel; stopping")
	case <-s.handedOver:
		log.Info("handed over to a new tf-squatterd")
		return nil
	case err := <-s.serveErr:
		result = err
	}
	s.stopHTTP()
	b.Close()
	<-runErr
	return result
}

// featuresFor maps TinyFugue's HELLO features to TFSP feature names.
func featuresFor(tf []string) []string {
	f := []string{wire.FeatureTF, wire.FeatureControl, wire.FeatureWorlds, wire.FeatureState}
	for _, x := range tf {
		switch x {
		case "worldedit":
			f = append(f, wire.FeatureWorldEdit)
		case "gmcp":
			f = append(f, wire.FeatureGmcp)
		case "msdp":
			f = append(f, wire.FeatureMsdp)
		case "exec":
			f = append(f, wire.FeatureExec)
		case "ask":
			f = append(f, wire.FeatureAsk)
		case "scripts":
			f = append(f, wire.FeatureScripts) // TinyFugue's squat_scripts is on: clients may send scripts
		}
	}
	return f
}

// ServerConfig is the wire server configuration for this bridge: kind, path,
// features (as TinyFugue announced them) and instance. Call it after WaitReady.
func (b *Bridge) ServerConfig(as *auth.Store, serverName, instance string, maxClients int, log *slog.Logger) wire.ServerConfig {
	if maxClients <= 0 {
		maxClients = 32
	}
	return wire.ServerConfig{Backend: b, Auth: as, ServerName: serverName, Logger: log, MaxClients: maxClients,
		Kind: proto.KindTFSquatterd, Path: "/tfsp", Instance: instance, ExtraFeatures: featuresFor(b.Features())}
}
