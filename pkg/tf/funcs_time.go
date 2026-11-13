package tf

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var tzRe = regexp.MustCompile(`^(?:UTC|GMT)?([+-])(\d{1,2})(?::?(\d{2}))?$`)

// location interprets $TZ without touching the filesystem: UTC (default) or a
// fixed offset such as UTC+2, -0530.
func (i *Interp) location() *time.Location {
	tz := strings.TrimSpace(i.getVarStr("TZ"))
	if m := tzRe.FindStringSubmatch(tz); m != nil {
		h, mm := int(S(m[2]).Int()), int(S(m[3]).Int())
		off := h*3600 + mm*60
		if m[1] == "-" {
			off = -off
		}
		return time.FixedZone(tz, off)
	}
	return time.UTC
}

func strftime(t time.Time, f string) string {
	var sb strings.Builder
	for k := 0; k < len(f); k++ {
		if f[k] != '%' || k+1 >= len(f) {
			sb.WriteByte(f[k])
			continue
		}
		k++
		switch f[k] {
		case 'Y':
			sb.WriteString(t.Format("2006"))
		case 'y':
			sb.WriteString(t.Format("06"))
		case 'm':
			sb.WriteString(t.Format("01"))
		case 'd':
			sb.WriteString(t.Format("02"))
		case 'e':
			sb.WriteString(fmt.Sprintf("%2d", t.Day()))
		case 'H':
			sb.WriteString(t.Format("15"))
		case 'I':
			sb.WriteString(t.Format("03"))
		case 'M':
			sb.WriteString(t.Format("04"))
		case 'S':
			sb.WriteString(t.Format("05"))
		case 'p':
			sb.WriteString(t.Format("PM"))
		case 'a':
			sb.WriteString(t.Format("Mon"))
		case 'A':
			sb.WriteString(t.Format("Monday"))
		case 'b', 'h':
			sb.WriteString(t.Format("Jan"))
		case 'B':
			sb.WriteString(t.Format("January"))
		case 'Z':
			sb.WriteString(t.Format("MST"))
		case 'z':
			sb.WriteString(t.Format("-0700"))
		case 'j':
			sb.WriteString(fmt.Sprintf("%03d", t.YearDay()))
		case 's':
			sb.WriteString(fmt.Sprint(t.Unix()))
		case 'F':
			sb.WriteString(t.Format("2006-01-02"))
		case 'T':
			sb.WriteString(t.Format("15:04:05"))
		case 'R':
			sb.WriteString(t.Format("15:04"))
		case 'D':
			sb.WriteString(t.Format("01/02/06"))
		case 'c':
			sb.WriteString(t.Format("Mon Jan  2 15:04:05 2006"))
		case 'n':
			sb.WriteByte('\n')
		case 't':
			sb.WriteByte('\t')
		case '%':
			sb.WriteByte('%')
		default:
			sb.WriteByte('%')
			sb.WriteByte(f[k])
		}
	}
	return sb.String()
}

func (i *Interp) initTimeFuncs() {
	i.def("time", 0, 0, func(i *Interp, _ []Value) (Value, error) { return I(i.host.Now().Unix()), nil })
	i.def("mktime", 1, 7, func(i *Interp, a []Value) (Value, error) {
		// year [month [day [hour [minute [second [microsecond]]]]]]; omitted
		// month and day default to 1, the rest to 0.
		f := [7]int64{0, 1, 1, 0, 0, 0, 0}
		for k := range a {
			f[k] = a[k].Int()
		}
		if f[1] < 1 || f[1] > 12 || f[2] < 1 || f[2] > 31 || f[3] < 0 || f[3] > 23 || f[4] < 0 || f[4] > 59 || f[5] < 0 || f[5] > 59 {
			return I(-1), nil
		}
		t := time.Date(int(f[0]), time.Month(f[1]), int(f[2]), int(f[3]), int(f[4]), int(f[5]), int(f[6])*1000, i.location())
		if f[6] != 0 {
			return F(float64(t.Unix()) + float64(f[6])/1e6), nil
		}
		return I(t.Unix()), nil
	})
	i.def("ftime", 1, 2, func(i *Interp, a []Value) (Value, error) {
		t := i.host.Now()
		if len(a) > 1 {
			t = time.Unix(a[1].Int(), 0)
		}
		return S(strftime(t.In(i.location()), a[0].s)), nil
	})
	i.def("idle", 0, 1, func(i *Interp, a []Value) (Value, error) {
		last := i.lastAny
		if len(a) > 0 {
			last = i.lastInput[a[0].s]
		}
		if last.IsZero() {
			return I(0), nil
		}
		return I(int64(i.host.Now().Sub(last).Seconds())), nil
	})
	i.def("sidle", 0, 1, func(i *Interp, a []Value) (Value, error) {
		w := i.curWorld()
		if len(a) > 0 {
			w = a[0].s
		}
		last := i.lastSend[w]
		if last.IsZero() {
			return I(0), nil
		}
		return I(int64(i.host.Now().Sub(last).Seconds())), nil
	})
}
