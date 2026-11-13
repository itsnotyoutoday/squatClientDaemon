package engine

import (
	"fmt"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func pal(n int) *proto.Color { c := proto.Palette(uint8(n)); return &c }
func rgb(r, g, b int) *proto.Color {
	c := proto.RGB(uint8(r), uint8(g), uint8(b))
	return &c
}

var ansiNames = [8]string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}

// colorTestLines builds the /colortest output: every colour mode the client
// supports, so it can be compared side by side with any terminal.
func colorTestLines() [][]proto.Span {
	var out [][]proto.Span
	add := func(sp ...proto.Span) { out = append(out, sp) }
	txt := func(s string) proto.Span { return proto.Span{S: s} }

	add(txt("=== Colour test: UTF-8, 16 colours, xterm-256, 24-bit truecolor, text styles ==="))
	add()
	add(txt("16 colours, foreground (slots 0-7 | 8-15; these come from the theme):"))
	var a, b []proto.Span
	for i := 0; i < 8; i++ {
		a = append(a, proto.Span{S: fmt.Sprintf("%-9s", ansiNames[i]), Fg: pal(i)})
		b = append(b, proto.Span{S: fmt.Sprintf("%-9s", ansiNames[i]), Fg: pal(i + 8)})
	}
	add(a...)
	add(b...)
	add()
	add(txt("16 colours, background:"))
	a, b = nil, nil
	for i := 0; i < 8; i++ {
		a = append(a, proto.Span{S: fmt.Sprintf(" %-7s", ansiNames[i]), Bg: pal(i), Fg: pal(map[bool]int{true: 15, false: 0}[i == 0 || i == 4])})
		b = append(b, proto.Span{S: fmt.Sprintf(" %-7s", ansiNames[i]), Bg: pal(i + 8), Fg: pal(0)})
	}
	add(a...)
	add(b...)
	add()
	add(txt("xterm-256 colour cube (16-231) and greys (232-255); exact xterm values:"))
	for row := 0; row < 6; row++ {
		var sp []proto.Span
		for col := 0; col < 36; col++ {
			n := 16 + row*36 + col
			sp = append(sp, proto.Span{S: "  ", Bg: pal(n)})
		}
		add(sp...)
	}
	var greys []proto.Span
	for n := 232; n <= 255; n++ {
		greys = append(greys, proto.Span{S: "   ", Bg: pal(n)})
	}
	add(greys...)
	add()
	add(txt("24-bit truecolor gradients (red-to-blue, green ramp, grey ramp):"))
	var g1, g2, g3 []proto.Span
	for i := 0; i < 64; i++ {
		v := i * 255 / 63
		g1 = append(g1, proto.Span{S: " ", Bg: rgb(255-v, 0, v)})
		g2 = append(g2, proto.Span{S: " ", Bg: rgb(0, v, 255-v)})
		g3 = append(g3, proto.Span{S: " ", Bg: rgb(v, v, v)})
	}
	add(g1...)
	add(g2...)
	add(g3...)
	add(proto.Span{S: "truecolor text: "}, proto.Span{S: "#ff8000 ", Fg: rgb(255, 128, 0)}, proto.Span{S: "#00ff80 ", Fg: rgb(0, 255, 128)}, proto.Span{S: "#8000ff", Fg: rgb(128, 0, 255)})
	add()
	add(txt("Text styles:"))
	add(proto.Span{S: "bold ", B: true}, proto.Span{S: "dim ", Dim: true}, proto.Span{S: "italic ", I: true},
		proto.Span{S: "underline ", U: true}, proto.Span{S: "strike ", St: true}, proto.Span{S: "blink ", Blink: true},
		proto.Span{S: "reverse", Rev: true})
	add()
	add(txt("UTF-8: héllo wörld ✓ → 日本語 한국어 \U0001F680 \U0001F600 ▓▒░ ⠀⣿"))
	return out
}
