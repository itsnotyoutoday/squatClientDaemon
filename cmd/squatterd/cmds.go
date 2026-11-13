package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
)

type tlsCert = tls.Certificate

func tlsListener(ln net.Listener, cfg *tls.Config) net.Listener { return tls.NewListener(ln, cfg) }

func cmdPair(o *options, w io.Writer) error {
	as, err := auth.Open(o.dataDir)
	if err != nil {
		return err
	}
	cert, err := o.cert()
	if err != nil {
		return err
	}
	code, exp, err := as.NewPairingTTL(o.pairValid)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "<this-host>"
	}
	fmt.Fprintf(w, "Pairing code:    %s\n", code)
	fmt.Fprintf(w, "Valid until:     %s (%s, single use)\n", exp.Format(time.RFC1123), time.Until(exp).Round(time.Minute))
	fmt.Fprintf(w, "TLS fingerprint: %s\n", fingerprint(cert))
	fmt.Fprintf(w, "Connect to:      wss://%s:%d/swp\n", host, o.port)
	fmt.Fprintln(w, "In SquatClient: add a squatter with that address and enter the code; check the fingerprint matches.")
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
		fmt.Fprintln(w, "no paired devices (run: squatterd pair)")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "DEVICE ID\tNAME\tPAIRED\tLAST SEEN")
	for _, d := range devs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.DeviceID, d.Name, ts(d.CreatedAt), ts(d.LastSeen))
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
		return fmt.Errorf("usage: squatterd revoke <deviceId>")
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

func fingerprint(c tlsCert) string {
	return tlsFingerprint(c)
}

func cmdStatus(o *options, w io.Writer) error {
	fmt.Fprintf(w, "version:     %s\n", version)
	fmt.Fprintf(w, "data dir:    %s\n", o.dataDir)
	if _, err := os.Stat(o.dataDir); err != nil {
		fmt.Fprintln(w, "             (does not exist yet; created on first run)")
		return nil
	}
	if o.certFile != "" || fileExists(filepath.Join(o.dataDir, "tls", "cert.pem")) {
		if c, err := o.cert(); err == nil {
			fmt.Fprintf(w, "fingerprint: %s\n", fingerprint(c))
		}
	} else {
		fmt.Fprintln(w, "fingerprint: (no certificate yet; generated on first serve)")
	}
	if as, err := auth.Open(o.dataDir); err == nil {
		if devs, err := as.Devices(); err == nil {
			fmt.Fprintf(w, "devices:     %d paired\n", len(devs))
		}
	}
	ss, err := daemon.ScanDisk(o.dataDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "sessions:    %d on disk\n", len(ss))
	for _, s := range ss {
		st := s.State
		if s.EndReason != "" {
			st += "/" + s.EndReason
		}
		fmt.Fprintf(w, "  %s  %-10s %s (%s:%d)\n", s.ID, st, strings.TrimSpace(s.Name), s.Host, s.Port)
	}
	return nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
