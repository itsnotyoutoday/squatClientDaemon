package engine

import (
	"testing"
	"time"
)

func TestBusNeverDropsLinesCoalescesStatus(t *testing.T) {
	b := newBus(100000)
	ch, cancel := b.subscribe()
	defer cancel()
	// publish before anyone reads: producers must not block
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ {
			b.publish(Event{Kind: EvLine, Session: "s", Text: itoa(i)})
			b.publish(Event{Kind: EvSession, Session: "s", Text: itoa(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a slow subscriber")
	}
	lines, sess := 0, 0
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case ev := <-ch:
			if ev.Kind == EvLine {
				if ev.Text != itoa(lines) {
					t.Fatalf("line order broken at %d: %s", lines, ev.Text)
				}
				lines++
			} else {
				sess++
			}
		case <-deadline:
			break loop
		}
	}
	if lines != 5000 {
		t.Fatalf("lines dropped: %d", lines)
	}
	if sess >= 5000 || sess == 0 {
		t.Fatalf("status events should be coalesced, got %d", sess)
	}
}

func TestBusOverflowClosesSubscriber(t *testing.T) {
	b := newBus(10)
	ch, cancel := b.subscribe()
	defer cancel()
	for i := 0; i < 1000; i++ {
		b.publish(Event{Kind: EvLine})
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("overflowing subscriber was not closed")
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var s []byte
	for ; i > 0; i /= 10 {
		s = append([]byte{byte('0' + i%10)}, s...)
	}
	return string(s)
}
