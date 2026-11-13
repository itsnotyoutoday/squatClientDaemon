package engine

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDescribeLossWording(t *testing.T) {
	// the network dropped: say the games are fine
	msg, hint := describeLoss("nexus", errors.New("read tcp 1.2.3.4:5->6.7.8.9:10: connection reset by peer"), true)
	if !strings.Contains(msg, "dropped") || !strings.Contains(msg, "game connections are held by the squatter") {
		t.Fatalf("network loss message: %q", msg)
	}
	if !strings.Contains(hint, "automatically") {
		t.Fatalf("auto hint: %q", hint)
	}
	// the squatter closed the connection (restart): do not promise the sessions survived
	msg, _ = describeLoss("nexus", io.EOF, true)
	if !strings.Contains(msg, "closed the connection") || !strings.Contains(msg, "may have ended") {
		t.Fatalf("EOF message: %q", msg)
	}
	// with automatic reconnect off the hint tells the user what to do
	_, hint = describeLoss("nexus", errors.New("reset"), false)
	if !strings.Contains(hint, "Reconnect") {
		t.Fatalf("manual hint: %q", hint)
	}
}
