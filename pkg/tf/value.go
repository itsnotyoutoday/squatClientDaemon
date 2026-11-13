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

func formatFloat(f float64) string {
	if math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
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

// Truth is TF truthiness: numbers are true when non-zero, other text when
// non-empty.
func (v Value) Truth() bool {
	if v.num {
		return v.f != 0
	}
	return v.s != ""
}

// typeName is what whatis() reports.
func (v Value) typeName() string {
	switch {
	case v.num && v.isInt:
		return "int"
	case v.num:
		return "float"
	}
	return "str"
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
