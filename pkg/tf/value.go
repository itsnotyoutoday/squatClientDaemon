package tf

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// maxValueLen caps the size of any single string value so that runaway
// string-building scripts fail fast instead of exhausting memory.
const maxValueLen = 256 * 1024

// Value is a TF-style dynamically typed value. Every value has a string form;
// values whose text looks like a number also have a numeric form, and the
// two are coerced freely (so "007" prints as 007 but compares equal to 7).
type Value struct {
	s     string
	num   bool
	isInt bool
	i     int64
	f     float64
}

// S builds a value from text, classifying it as numeric when it looks like a
// decimal integer or float.
func S(s string) Value {
	v := Value{s: s}
	if i, ok := parseIntText(s); ok {
		v.num, v.isInt, v.i, v.f = true, true, i, float64(i)
	} else if f, ok := parseFloatText(s); ok {
		v.num, v.f = true, f
	}
	return v
}

// I builds an integer value.
func I(i int64) Value {
	return Value{s: strconv.FormatInt(i, 10), num: true, isInt: true, i: i, f: float64(i)}
}

// F builds a float value.
func F(f float64) Value {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return S("0")
	}
	return Value{s: formatFloat(f), num: true, f: f}
}

// B builds the integer 1 or 0.
func B(b bool) Value {
	if b {
		return I(1)
	}
	return I(0)
}

// formatFloat renders a float like TinyFugue: 15 significant digits (%g
// style, so 0.1+0.2 prints 0.3), and a trailing "." when the result would
// otherwise look like an integer (so a float stays distinguishable: 1024.).
func formatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'g', 15, 64)
	if strings.ContainsAny(s, "e") {
		// Go prints e+20 / e-07; C prints e+20 / e-07 as well, but with
		// at least two exponent digits, which Go also does.
		return s
	}
	// TF appends the dot only to all-digit text, so negative floats stay bare.
	if !strings.ContainsAny(s, ".-") {
		s += "."
	}
	return s
}

// numPrefix coerces any value to a number the way TF does for arithmetic: a
// leading numeric prefix ("12abc" is 12, "3.5x" is 3.5, " 5" is 5) and 0 for
// text with none. The second result reports whether a prefix was found.
func numPrefix(v Value) (Value, bool) {
	if v.num {
		return v, true
	}
	s := strings.TrimLeft(v.s, " \t")
	k := 0
	if k < len(s) && (s[k] == '-' || s[k] == '+') {
		k++
	}
	d := k
	for k < len(s) && s[k] >= '0' && s[k] <= '9' {
		k++
	}
	digits := k > d
	isFloat := false
	if k < len(s) && s[k] == '.' {
		j := k + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if digits || j > k+1 {
			digits = digits || j > k+1
			isFloat = true
			k = j
		}
	}
	if !digits {
		return I(0), false
	}
	if k < len(s) && (s[k] == 'e' || s[k] == 'E') {
		j := k + 1
		if j < len(s) && (s[j] == '-' || s[j] == '+') {
			j++
		}
		if j < len(s) && s[j] >= '0' && s[j] <= '9' {
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			k = j
			isFloat = true
		}
	}
	t := s[:k]
	if !isFloat {
		if i, err := strconv.ParseInt(t, 10, 64); err == nil {
			return I(i), true
		}
	}
	if f, err := strconv.ParseFloat(t, 64); err == nil && !math.IsInf(f, 0) {
		if isFloat {
			return F(f), true
		}
		return F(f), true
	}
	return I(0), false
}

func parseIntText(s string) (int64, bool) {
	if s == "" || len(s) > 20 {
		return 0, false
	}
	b := s
	if b[0] == '-' || b[0] == '+' {
		b = b[1:]
	}
	if b == "" {
		return 0, false
	}
	for k := 0; k < len(b); k++ {
		if b[k] < '0' || b[k] > '9' {
			return 0, false
		}
	}
	i, err := strconv.ParseInt(s, 10, 64)
	return i, err == nil
}

func parseFloatText(s string) (float64, bool) {
	if s == "" || len(s) > 64 {
		return 0, false
	}
	digit := false
	for k := 0; k < len(s); k++ {
		c := s[k]
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c == '+' || c == '-' || c == '.' || c == 'e' || c == 'E':
		default:
			return 0, false
		}
	}
	if !digit {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

// String returns the text form.
func (v Value) String() string { return v.s }

// IsNum reports whether the value has a numeric form.
func (v Value) IsNum() bool { return v.num }

// IsInt reports whether the value is an integer.
func (v Value) IsInt() bool { return v.num && v.isInt }

// Float returns the numeric form (0 for non-numeric text).
func (v Value) Float() float64 {
	if v.num {
		return v.f
	}
	return 0
}

// Int returns the integer form (floats truncate; non-numeric text is 0).
func (v Value) Int() int64 {
	if !v.num {
		return 0
	}
	if v.isInt {
		return v.i
	}
	if v.f >= math.MaxInt64 || v.f <= math.MinInt64 {
		return 0
	}
	return int64(v.f)
}

// Truth is TF truthiness: the value's numeric form is non-zero. Text is read
// by its leading number, so plain words (and the empty string) are false.
func (v Value) Truth() bool {
	if v.num {
		return v.f != 0
	}
	n, _ := numPrefix(v)
	return n.Float() != 0
}

// typeName is what whatis() reports.
func (v Value) typeName() string {
	switch {
	case v.num && v.isInt:
		return "int"
	case v.num:
		return "float"
	}
	return "string"
}

func cmpValues(a, b Value) int {
	if a.num && b.num {
		if a.isInt && b.isInt {
			switch {
			case a.i < b.i:
				return -1
			case a.i > b.i:
				return 1
			}
			return 0
		}
		switch {
		case a.f < b.f:
			return -1
		case a.f > b.f:
			return 1
		}
		return 0
	}
	return strings.Compare(a.s, b.s)
}

// protoSpan is a local alias keeping command files terse.
type protoSpan = proto.Span

func durSeconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
