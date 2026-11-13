package wire

import (
	"strings"
	"testing"
)

func TestDecodeClient(t *testing.T) {
	cases := []struct {
		in      string
		known   bool
		wantErr bool
	}{
		{`{"v":1,"t":"pong"}`, true, false},
		{`{"v":1,"t":"session.list","id":"1"}`, true, false},
		{`{"v":1,"t":"session.connect","id":"1","world":{"host":"a.b","port":4201}}`, true, false},
		{`{"v":1,"t":"session.connect","world":{"host":"","port":4201}}`, true, true},
		{`{"v":1,"t":"session.connect","world":{"host":"a b","port":4201}}`, true, true},
		{`{"v":1,"t":"session.connect","world":{"host":"a","port":4201,"encoding":"ebcdic"}}`, true, true},
		{`{"v":1,"t":"attach","session":"s","maxBacklog":-1}`, true, true},
		{`{"v":1,"t":"attach","session":"s","since":0}`, true, false},
		{`{"v":1,"t":"attach"}`, true, true},
		{`{"v":1,"t":"resize","session":"s","cols":0,"rows":5}`, true, true},
		{`{"v":1,"t":"resize","session":"s","cols":80,"rows":24}`, true, false},
		{`{"v":1,"t":"ack","session":"s","seq":-4}`, true, true},
		{`{"v":1,"t":"nope"}`, false, false},
		{`{"v":1}`, false, true},
		{`[1,2]`, false, true},
		{"\xff\xfe", false, true},
	}
	for _, c := range cases {
		_, known, err := DecodeClient([]byte(c.in))
		if known != c.known || (err != nil) != c.wantErr {
			t.Errorf("%q: known=%v err=%v", c.in, known, err)
		}
	}
	cf, _, _ := DecodeClient([]byte(`{"v":1,"t":"attach","session":"s","maxBacklog":999999}`))
	if cf.Msg.(AttachReq).MaxBacklog != MaxBacklog {
		t.Fatal("maxBacklog not clamped")
	}
	cf, _, _ = DecodeClient([]byte(`{"v":1,"t":"attach","session":"s"}`))
	if cf.Msg.(AttachReq).MaxBacklog != DefaultBacklog {
		t.Fatal("default maxBacklog")
	}
	if _, _, err := DecodeClient([]byte(strings.Repeat("a", MaxFrameBytes+1))); err == nil {
		t.Fatal("oversize accepted")
	}
}

// FuzzDecodeClient: the frame parser must never panic and must keep its invariants.
func FuzzDecodeClient(f *testing.F) {
	for _, s := range []string{
		`{"v":1,"t":"hello","publicKey":"x"}`,
		`{"v":1,"t":"attach","session":"s-1","since":5,"maxBacklog":10}`,
		`{"v":1,"t":"send","session":"s","text":"look"}`,
		`{"v":1,"t":"session.connect","id":"1","world":{"name":"n","host":"h","port":1,"tls":true,"tlsVerify":"strict","encoding":"utf-8"}}`,
		`{"v":1,"t":"resize","session":"s","cols":80,"rows":24}`,
		`{"t":"caps","caps":{"truecolor":true}}`,
		`{`, `null`, `[]`, `"x"`, `{"t":5}`, `{"v":"a","t":"pong"}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		cf, known, err := DecodeClient(b)
		if err == nil && known && cf.T == "" {
			t.Fatal("known frame without type")
		}
		if m, ok := cf.Msg.(AttachReq); ok && err == nil && (m.MaxBacklog < 1 || m.MaxBacklog > MaxBacklog) {
			t.Fatalf("maxBacklog out of range: %d", m.MaxBacklog)
		}
		if m, ok := cf.Msg.(ResizeReq); ok && err == nil && (m.Cols < 1 || m.Cols > 1000 || m.Rows < 1 || m.Rows > 1000) {
			t.Fatalf("resize out of range: %+v", m)
		}
	})
}
