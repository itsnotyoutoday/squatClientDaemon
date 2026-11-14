package tfengine

import (
	"testing"

	"github.com/itsnotyoutoday/gtfparser"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func TestApplyRangeSplitsSpans(t *testing.T) {
	red := proto.Palette(1)
	out := applyRange([]proto.Span{{S: "hello world"}}, 2, 7, Attr{Fg: &red, Bold: true})
	if len(out) != 3 || out[0].S != "he" || out[1].S != "llo w" || out[2].S != "orld" || out[1].Fg == nil || !out[1].B || out[0].Fg != nil {
		t.Fatalf("%+v", out)
	}
	if got := applyRange(out, 3, 3, Attr{Bold: true}); len(got) != len(out) {
		t.Fatal("an empty range must change nothing")
	}
}

func TestEngineAttributesBecomeSpans(t *testing.T) {
	a := gtfparser.AttrBold | gtfparser.AttrUnderline
	a = a.WithFg(9).WithBg(4)
	got := fromEngineAttr(a).Apply(proto.Span{S: "x"})
	if !got.B || !got.U || got.Fg == nil || got.Bg == nil {
		t.Fatalf("%+v", got)
	}
}
