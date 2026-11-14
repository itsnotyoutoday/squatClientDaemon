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
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// ServeOptions configures Serve.
type ServeOptions struct {
	Conn             net.Conn // control channel to TinyFugue
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

// Serve runs tf-squatterd until ctx is cancelled or TinyFugue closes the
// control channel. It returns nil on a clean stop.
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

	ln, err := net.Listen("tcp", net.JoinHostPort(o.Bind, fmt.Sprint(o.Port)))
	if err != nil {
		return err
	}
	defer ln.Close()
	scheme, fp := "wss", ""
	hs := &http.Server{ReadHeaderTimeout: 10 * time.Second, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn)}
	if o.InsecureLoopback {
		scheme = "ws"
	} else {
		cert, err := LoadCert(o.DataDir, o.CertFile, o.KeyFile)
		if err != nil {
			return err
		}
		hs.TLSConfig = tlsutil.ServerConfig(cert)
		fp = tlsutil.CertFingerprint(cert)
		ln = tls.NewListener(ln, hs.TLSConfig)
	}
	host := o.Bind
	if ip := net.ParseIP(strings.Trim(o.Bind, "[]")); host == "" || (ip != nil && ip.IsUnspecified()) {
		if h, _ := os.Hostname(); h != "" {
			host = h
		} else {
			host = "<this-host>"
		}
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	hint := fmt.Sprintf("%s://%s:%s/tfsp", scheme, host, port)

	b, err := New(Config{DataDir: o.DataDir, Logger: log, Auth: as, Fingerprint: fp, Hint: hint, PairTTL: o.PairTTL, Version: o.Version}, o.Conn)
	if err != nil {
		return err
	}
	runErr := make(chan error, 1)
	go func() { runErr <- b.Run() }()

	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	instance, err := b.WaitReady(wctx)
	wcancel()
	if err != nil {
		b.Close()
		return err
	}

	srv := wire.NewServer(b.ServerConfig(as, "tf-squatterd/"+o.Version, instance, o.MaxClients, log))
	hs.Handler = srv.Handler()
	log.Info("tf-squatterd listening", "url", hint, "instance", instance, "data-dir", o.DataDir)
	if fp != "" {
		log.Info("TLS fingerprint", "sha256", fp)
	}
	if o.Ready != nil {
		o.Ready(ln.Addr().String())
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- hs.Serve(ln) }()

	var result error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case <-b.Done():
		log.Info("TinyFugue closed the control channel; stopping")
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Close()
	_ = hs.Shutdown(sctx)
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
