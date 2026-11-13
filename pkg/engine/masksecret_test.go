package engine_test

import (
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
)

func TestMaskSecret(t *testing.T) {
	cases := []struct {
		in, out string
		secret  bool
	}{
		{"look", "look", false},
		{"connect guest", "connect guest", false},
		{"connect bob hunter2", "connect bob ********", true},
		{"@password old=new", "@password ********", true},
		{"say hi\nconnect \"A B\" pw\nsay bye", "say hi\nconnect \"A B\" ********\nsay bye", true},
		{"", "", false},
	}
	for _, c := range cases {
		got, sec := engine.MaskSecret(c.in)
		if got != c.out || sec != c.secret {
			t.Errorf("MaskSecret(%q) = %q,%v want %q,%v", c.in, got, sec, c.out, c.secret)
		}
	}
}
