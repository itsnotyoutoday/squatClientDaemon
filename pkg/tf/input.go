package tf

import (
	"strings"
	"unicode"
)

// The input buffer: the kb* functions, /input and the editing actions of
// /dokey. When the Host implements InputBuffer the GUI input line is used;
// otherwise a private shadow buffer.

func (i *Interp) kbGet() ([]rune, int) {
	if ib, ok := i.host.(InputBuffer); ok {
		t, c := ib.InputBuffer()
		r := []rune(t)
		return r, clampInt(c, 0, len(r))
	}
	return i.kbText, clampInt(i.kbPos, 0, len(i.kbText))
}

func (i *Interp) kbSet(r []rune, pos int) {
	pos = clampInt(pos, 0, len(r))
	if ib, ok := i.host.(InputBuffer); ok {
		if i.live {
			ib.SetInputBuffer(string(r), pos)
		}
		return
	}
	i.kbText, i.kbPos = r, pos
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (i *Interp) inWord(c rune) bool {
	return unicode.IsLetter(c) || unicode.IsDigit(c) || strings.ContainsRune(i.getVarStr("wordpunct"), c)
}

// kbWord finds the end (dir>0) or start (dir<0) of the word at start.
func (i *Interp) kbWord(r []rune, start, dir int) int {
	stop := len(r)
	if dir < 0 {
		stop = -1
	}
	p := clampInt(start, 0, len(r))
	if dir < 0 {
		p--
	}
	for p != stop && !i.inWord(r[p]) {
		p += dir
	}
	for p != stop && i.inWord(r[p]) {
		p += dir
	}
	if dir < 0 {
		p++
	}
	return p
}

// kbMatch finds a bracket at or after start and returns its partner.
func kbMatch(r []rune, start int) int {
	const braces = "(){}[]"
	p := clampInt(start, 0, len(r))
	var k int
	for {
		if p >= len(r) {
			return -1
		}
		if k = strings.IndexRune(braces, r[p]); k >= 0 {
			break
		}
		p++
	}
	dir, stop := 1, len(r)
	if k%2 == 1 {
		dir, stop = -1, -1
	}
	open, closer := rune(braces[k]), rune(braces[k+dir])
	depth := 0
	for {
		switch r[p] {
		case open:
			depth++
		case closer:
			depth--
		}
		if depth == 0 {
			return p
		}
		if p += dir; p == stop {
			return -1
		}
	}
}

// kbDel deletes between the cursor and place and returns the cursor.
func (i *Interp) kbDel(place int) int {
	r, pos := i.kbGet()
	switch {
	case place >= 0 && place < pos:
		r = append(append([]rune(nil), r[:place]...), r[pos:]...)
		pos = place
	case place > pos && place <= len(r):
		r = append(append([]rune(nil), r[:pos]...), r[place:]...)
	}
	i.kbSet(r, pos)
	_, pos = i.kbGet()
	return pos
}

func (i *Interp) initInputFuncs() {
	i.def("kbhead", 0, 0, func(i *Interp, _ []Value) (Value, error) {
		r, p := i.kbGet()
		return S(string(r[:p])), nil
	})
	i.def("kbtail", 0, 0, func(i *Interp, _ []Value) (Value, error) {
		r, p := i.kbGet()
		return S(string(r[p:])), nil
	})
	i.def("kbpoint", 0, 0, func(i *Interp, _ []Value) (Value, error) {
		_, p := i.kbGet()
		return I(int64(p)), nil
	})
	i.def("kblen", 0, 0, func(i *Interp, _ []Value) (Value, error) {
		r, _ := i.kbGet()
		return I(int64(len(r))), nil
	})
	i.def("kbgoto", 1, 1, func(i *Interp, a []Value) (Value, error) {
		r, _ := i.kbGet()
		i.kbSet(r, int(a[0].Int()))
		_, p := i.kbGet()
		return I(int64(p)), nil
	})
	i.def("kbdel", 1, 1, func(i *Interp, a []Value) (Value, error) {
		return I(int64(i.kbDel(int(a[0].Int())))), nil
	})
	i.def("kbmatch", 0, 1, func(i *Interp, a []Value) (Value, error) {
		r, p := i.kbGet()
		if len(a) > 0 {
			p = int(a[0].Int())
		}
		return I(int64(kbMatch(r, p))), nil
	})
	word := func(name string, dir int) {
		i.def(name, 0, 1, func(i *Interp, a []Value) (Value, error) {
			r, p := i.kbGet()
			if len(a) > 0 {
				p = int(a[0].Int())
			}
			return I(int64(i.kbWord(r, p, dir))), nil
		})
	}
	word("kbwordleft", -1)
	word("kbwordright", 1)
	i.def("kbcharback", 0, 1, func(i *Interp, a []Value) (Value, error) {
		_, p := i.kbGet()
		n := 1
		if len(a) > 0 {
			n = int(a[0].Int())
		}
		return I(int64(max(p-n, 0))), nil
	})
	i.def("kbcharfwd", 0, 1, func(i *Interp, a []Value) (Value, error) {
		r, p := i.kbGet()
		n := 1
		if len(a) > 0 {
			n = int(a[0].Int())
		}
		return I(int64(min(p+n, len(r)))), nil
	})
}

func (i *Interp) initInputCmds() {
	i.reg("input", "/input text   (insert text into the input line at the cursor)", false, func(i *Interp, a string) error {
		if !i.live {
			return nil
		}
		r, p := i.kbGet()
		ins := []rune(a)
		nr := append(append(append([]rune(nil), r[:p]...), ins...), r[p:]...)
		i.kbSet(nr, p+len(ins))
		if _, ok := i.host.(InputBuffer); !ok {
			i.host.Input(i.curWorld(), a)
		}
		return nil
	})
	i.reg("dokey", "/dokey name   (editing and cursor actions on the input line; others go to the host)", false, func(i *Interp, a string) error {
		orig := strings.TrimSpace(a)
		name := strings.ToUpper(orig)
		if !i.live {
			return nil
		}
		r, p := i.kbGet()
		switch name {
		case "LEFT":
			i.kbSet(r, p-1)
		case "RIGHT":
			i.kbSet(r, p+1)
		case "HOME":
			i.kbSet(r, 0)
		case "END":
			i.kbSet(r, len(r))
		case "WLEFT":
			i.kbSet(r, i.kbWord(r, p, -1))
		case "WRIGHT":
			i.kbSet(r, i.kbWord(r, p, 1))
		case "BSPC":
			i.kbDel(p - 1)
		case "DCH":
			i.kbDel(p + 1)
		case "BWORD":
			i.kbDel(i.kbWord(r, p, -1))
		case "DWORD":
			i.kbDel(i.kbWord(r, p, 1))
		case "DEOL":
			i.kbDel(len(r))
		case "DLINE":
			i.kbSet(nil, 0)
		case "REDRAW", "REFRESH":
			if u, ok := i.host.(UIControl); ok {
				u.Redraw()
				return nil
			}
			i.host.DoKey(i.curWorld(), orig)
			return nil
		default:
			i.host.DoKey(i.curWorld(), orig)
			return nil
		}
		_, p = i.kbGet()
		i.last = I(int64(p))
		return nil
	})
	i.reg("suspend", "/suspend   (minimise the window)", false, func(i *Interp, _ string) error {
		if u, ok := i.host.(UIControl); ok && i.live {
			u.Suspend()
		}
		return nil
	})
}
