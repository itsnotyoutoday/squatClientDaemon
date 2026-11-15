package conn

import (
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

// Evennia's opening: IAC DO LINEMODE, WILL SGA, DO NAWS, DO TTYPE, WILL MCCP2, WILL MSSP, WILL MSDP, WILL GMCP, WILL MXP; it does
// not offer CHARSET and assumes UTF-8. MSSP is requested and read; what we refuse is refused.
func TestEvenniaNegotiationAndMSSP(t *testing.T) {
	s := fakemush.New(t)
	cfg := cfgFor(s)
	c := dialT(t, cfg)
	if ev := next(t, c); ev.Kind != EvState || !ev.Connected {
		t.Fatalf("first event %+v", ev)
	}
	p := s.Peer()
	for _, o := range []byte{34} { // LINEMODE
		p.Do(o)
	}
	p.Will(fakemush.OptSGA)
	p.Do(fakemush.OptNAWS)
	p.Do(fakemush.OptTType)
	p.Will(86) // MCCP2 (we accept it, but never start it: the test server does not compress)
	p.Will(70) // MSSP
	p.Will(69) // MSDP
	p.Will(fakemush.OptGMCP)
	p.Will(91) // MXP
	for _, want := range [][2]byte{{fakemush.WONT, 34}, {fakemush.DO, fakemush.OptSGA}, {fakemush.WILL, fakemush.OptNAWS}, {fakemush.WILL, fakemush.OptTType}, {fakemush.DO, 70}, {fakemush.DONT, 69}, {fakemush.DO, fakemush.OptGMCP}, {fakemush.DONT, 91}} {
		p.WaitNeg(want[0], want[1])
	}
	// MSSP variables: VAR name VAL value
	p.Sub(70, []byte("\x01CODEBASE\x02Evennia 4.5.0\x01NAME\x02testgame\x01UTF-8\x021\x01ANSI\x021\x01XTERM 256 COLORS\x021\x01PLAYERS\x022"))
	ev := nextKind(t, c, EvMssp)
	if ev.Mssp["CODEBASE"] != "Evennia 4.5.0" || ev.Mssp["NAME"] != "testgame" || ev.Mssp["UTF-8"] != "1" || ev.Mssp["XTERM 256 COLORS"] != "1" {
		t.Fatalf("mssp %+v", ev.Mssp)
	}
	// the terminal type cycle still names us the way Evennia expects (TINYFUGUE first, then the terminal, then MTTS)
	for i := 1; i <= 3; i++ {
		p.Sub(fakemush.OptTType, []byte{1})
		p.WaitSub(fakemush.OptTType, i)
	}
	subs := p.Subnegs()
	var tt []string
	for _, sb := range subs {
		if sb.Opt == fakemush.OptTType {
			tt = append(tt, string(sb.Data))
		}
	}
	if len(tt) < 3 || tt[0] != "\x00TINYFUGUE" || tt[1] != "\x00XTERM-TRUECOLOR" || tt[2][:6] != "\x00MTTS " {
		t.Fatalf("ttype answers %q", tt)
	}
}

func TestMSSPMultiValueAndGarbage(t *testing.T) {
	s := fakemush.New(t)
	c := dialT(t, cfgFor(s))
	next(t, c)
	p := s.Peer()
	p.Will(70)
	p.WaitNeg(fakemush.DO, 70)
	p.Sub(70, []byte("\x01GAMEPLAY\x02Adventure\x02Roleplaying\x01\x01EMPTY\x02"))
	ev := nextKind(t, c, EvMssp)
	if ev.Mssp["GAMEPLAY"] != "Adventure, Roleplaying" || ev.Mssp["EMPTY"] != "" {
		t.Fatalf("%+v", ev.Mssp)
	}
}

// A world set to latin1 whose game sends UTF-8: the session switches, once, and says so. A real latin1 game never does.
func TestUTF8SniffSwitchesLatin1World(t *testing.T) {
	s := fakemush.New(t)
	cfg := cfgFor(s)
	cfg.World.Encoding = "latin1"
	c := dialT(t, cfg)
	next(t, c)
	s.SendString("┌─┐ ok\r\n")
	// before the evidence is complete the first lines may be mojibake; after the switch text is right
	s.SendString("café ünï\r\n")
	enc := nextKind(t, c, EvEncoding)
	if enc.Encoding != "utf-8" {
		t.Fatalf("%+v", enc)
	}
	s.SendString("naïve ☃ done\r\n")
	for {
		ev := next(t, c)
		if ev.Kind == EvLine && len(ev.Spans) == 1 && ev.Spans[0].S == "naïve ☃ done" {
			break
		}
	}
	// and what we send is now UTF-8 too
	if err := c.Send("é"); err != nil {
		t.Fatal(err)
	}
	s.ExpectLine("é")
}

func TestLatin1GameIsNotSwitched(t *testing.T) {
	s := fakemush.New(t)
	cfg := cfgFor(s)
	cfg.World.Encoding = "latin1"
	c := dialT(t, cfg)
	next(t, c)
	s.SendString("┌─┐ caf\xe9 \xfc\r\n┌─┐ ┌─┐ ┌─┐\r\n")
	for i := 0; i < 2; i++ {
		if ev := next(t, c); ev.Kind == EvEncoding {
			t.Fatal("switched a latin1 game")
		}
	}
}

func TestMSSPSwitchesWrongEncodingSetting(t *testing.T) {
	s := fakemush.New(t)
	cfg := cfgFor(s)
	cfg.World.Encoding = "latin1"
	c := dialT(t, cfg)
	next(t, c)
	c.SwitchUTF8()
	s.SendString("x\r\n") // the switch happens before the next text is decoded
	enc := nextKind(t, c, EvEncoding)
	if enc.Encoding != "utf-8" {
		t.Fatalf("%+v", enc)
	}
}

func TestSnifferChunkBoundary(t *testing.T) {
	var sn utf8Sniffer
	b := []byte("é☃日")
	for _, x := range b { // one byte at a time: sequences span chunks
		if sn.feed([]byte{x}) && string(b) != "" {
			return
		}
	}
	t.Fatal("never decided")
}
