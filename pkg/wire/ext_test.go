package wire

import (
	"context"
	"reflect"
	"testing"
)

func TestIntersectNeverExceedsTheOffer(t *testing.T) {
	got := intersect([]string{"a", "b"}, []string{"b", "c", "a"})
	if !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Fatalf("%v", got)
	}
	if got := intersect(nil, []string{"x"}); len(got) != 0 {
		t.Fatalf("a server cannot accept what was not offered: %v", got)
	}
}

func TestExtensionTrafficRefusedUntilNegotiated(t *testing.T) {
	c := &Client{}
	if err := c.SendExt(context.Background(), "x.put", nil); err == nil {
		t.Fatal("SendExt without a negotiated extension was not refused")
	}
	if _, err := c.RequestExt(context.Background(), "x.get", nil); err == nil {
		t.Fatal("RequestExt without a negotiated extension was not refused")
	}
	c.accepted = []string{"x"}
	if !c.HasNegotiated("x") || c.HasNegotiated("y") {
		t.Fatal("HasNegotiated")
	}
}
