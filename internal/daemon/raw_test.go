package daemon_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// raw is a minimal SWP client with NO de-duplication, used to verify the
// server's own guarantees (seam without duplicates, slow-client handling).
type raw struct {
	t  *testing.T
	ws *websocket.Conn
}

func (e *env) rawClient() *raw {
	e.t.Helper()
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	cx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	ws, _, err := websocket.Dial(cx, e.url, &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		e.t.Fatal(err)
	}
	ws.SetReadLimit(8 << 20)
	e.t.Cleanup(func() { ws.CloseNow() })
	_, d, err := ws.Read(cx)
	if err != nil {
		e.t.Fatal(err)
	}
	var ch wire.Challenge
	_ = json.Unmarshal(d, &ch)
	key, _ := auth.GenerateKey()
	code, _, _ := e.as.NewPairing()
	sig, _ := auth.SignNonce(key, ch.Nonce)
	b, _ := json.Marshal(wire.Hello{V: 1, T: wire.THello, PublicKey: auth.PublicKeyB64(key), Sig: sig, PairCode: code})
	if err := ws.Write(cx, websocket.MessageText, b); err != nil {
		e.t.Fatal(err)
	}
	_, d, err = ws.Read(cx)
	if err != nil || !bytes.Contains(d, []byte(`"welcome"`)) {
		e.t.Fatalf("raw welcome: %v %s", err, d)
	}
	return &raw{t: e.t, ws: ws}
}

func (r *raw) send(v any) {
	r.t.Helper()
	b, _ := json.Marshal(v)
	cx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := r.ws.Write(cx, websocket.MessageText, b); err != nil {
		r.t.Fatal(err)
	}
}

// recv reads one frame as a generic map.
func (r *raw) recv(d time.Duration) (map[string]json.RawMessage, error) {
	cx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, b, err := r.ws.Read(cx)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		r.t.Fatalf("bad frame %s", b)
	}
	return m, nil
}

func str(m map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(m[k], &s)
	return s
}
func num(m map[string]json.RawMessage, k string) uint64 {
	var n uint64
	_ = json.Unmarshal(m[k], &n)
	return n
}

// rawDialOnly connects and returns a raw client that has NOT authenticated yet.
func (e *env) rawDialOnly() *raw {
	e.t.Helper()
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	cx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	ws, _, err := websocket.Dial(cx, e.url, &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { ws.CloseNow() })
	return &raw{t: e.t, ws: ws}
}

func (r *raw) challenge() wire.Challenge {
	r.t.Helper()
	cx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	_, d, err := r.ws.Read(cx)
	if err != nil {
		r.t.Fatal(err)
	}
	var ch wire.Challenge
	_ = json.Unmarshal(d, &ch)
	return ch
}
