package engine

import "testing"

func TestSecretLineDetection(t *testing.T) {
	cases := []struct {
		in     string
		masked string
		secret bool
	}{
		{"connect bob pw", "connect bob ********", true},
		{"CONNECT bob pw with spaces", "CONNECT bob ********", true},
		{"co bob pw", "co bob ********", true},
		{`connect "Bob Two" pw`, `connect "Bob Two" ********`, true},
		{"create bob pw", "create bob ********", true},
		{"@password old=new", "@password ********", true},
		{"@newpassword bob=pw", "@newpassword ********", true},
		{"@pass old=new", "@pass ********", true},
		{"connect guest", "connect guest", false},
		{"say connect bob pw", "say connect bob pw", false},
		{"look", "look", false},
		{"@pemit bob=connect x y", "@pemit bob=connect x y", false},
		{"", "", false},
	}
	for _, c := range cases {
		m, ok := secretLine(c.in)
		if ok != c.secret || m != c.masked {
			t.Errorf("secretLine(%q) = (%q,%v), want (%q,%v)", c.in, m, ok, c.masked, c.secret)
		}
	}
	// multi-line: any secret line masks that line and flags the whole text
	if m, ok := secretLine("say hi\nconnect bob pw"); !ok || m != "say hi\nconnect bob ********" {
		t.Errorf("multiline: %q %v", m, ok)
	}
}
