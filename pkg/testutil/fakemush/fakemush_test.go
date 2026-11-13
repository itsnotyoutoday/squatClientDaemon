package fakemush_test

import (
	"bytes"
	"compress/zlib"
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

func dial(t *testing.T, s *fakemush.Server) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestBasicScript(t *testing.T) {
	s := fakemush.New(t, fakemush.WithGreeting("hi\r\n"))
	c := dial(t, s)
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hi\r\n" {
		t.Fatalf("%q %v", buf, err)
	}
	s.Do(fakemush.OptNAWS)
	s.SendLine("ok")
	c.Write([]byte{fakemush.IAC, fakemush.WILL, fakemush.OptNAWS, fakemush.IAC, fakemush.SB, fakemush.OptNAWS, 0, 80, 0, 24, fakemush.IAC, fakemush.SE})
	c.Write([]byte("look\r\nsay \xff\xff\r\n"))
	s.WaitNeg(fakemush.WILL, fakemush.OptNAWS)
	if got := s.WaitSub(fakemush.OptNAWS, 1); !bytes.Equal(got, []byte{0, 80, 0, 24}) {
		t.Fatal(got)
	}
	s.ExpectLine("look")
	if got := s.ReadLine(); got != "say \xff" {
		t.Fatalf("%q", got)
	}
}

func TestDelayChunksMCCPAndReset(t *testing.T) {
	s := fakemush.New(t, fakemush.WithFirstByteDelay(80*time.Millisecond))
	c := dial(t, s)
	start := time.Now()
	s.SendString("a")
	b := make([]byte, 1)
	if _, err := io.ReadFull(c, b); err != nil || time.Since(start) < 60*time.Millisecond {
		t.Fatalf("delay not honoured: %v %v", err, time.Since(start))
	}
	s.StartMCCP2()
	s.SendString("squeezed")
	s.EndMCCP2()
	s.SendString("plain")
	all, _ := io.ReadAll(io.LimitReader(c, 0)) // no-op; keep conn open
	_ = all
	c.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, 0, 256)
	tmp := make([]byte, 256)
	for !bytes.HasSuffix(got, []byte("plain")) {
		n, err := c.Read(tmp)
		got = append(got, tmp[:n]...)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.HasPrefix(got, []byte{fakemush.IAC, fakemush.SB, fakemush.OptMCCP2, fakemush.IAC, fakemush.SE}) {
		t.Fatalf("%v", got)
	}
	zr, err := zlib.NewReader(bytes.NewReader(got[5:]))
	if err != nil {
		t.Fatal(err)
	}
	dec, _ := io.ReadAll(zr)
	if string(dec) != "squeezed" {
		t.Fatalf("%q", dec)
	}
	s.Reset()
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(tmp); err == nil {
		t.Fatal("expected error after reset")
	}
}

func TestTLS(t *testing.T) {
	s := fakemush.New(t, fakemush.WithTLS())
	if len(s.Fingerprint()) != 64 {
		t.Fatal("fingerprint")
	}
	c, err := tls.Dial("tcp", s.Addr(), &tls.Config{RootCAs: s.CertPool(), ServerName: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s.SendLine("secure")
	b := make([]byte, 8)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "secure\r\n" {
		t.Fatalf("%q %v", b, err)
	}
}
