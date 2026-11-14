package wire

import (
	"context"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Protocol extensions: the one place where a client announces optional protocol features and
// learns which of them the server accepted.
//
//	hello   {... "offer": ["name", ...]}        ClientOptions.Offer, empty by default (field omitted)
//	welcome {... "negotiated": ["name", ...]}   Client.Negotiated(): the part of the offer the server accepts
//
// A feature is usable only when it is in Negotiated(): a server that does not know the field (every
// squatterd, tf-squatterd and gtf before negotiation) answers without it, so nothing is negotiated
// and the connection behaves exactly as SWP v1. After negotiation the extension's own messages
// arrive as the Ext event (unknown message types) and are sent with SendExt / RequestExt.
//
// The field names "offer" and "negotiated" are this client's placeholders until the gtf extension
// specification (docs/SWP-GTF.md in gtf) fixes the real handshake; only this file and the two
// struct fields in msgs.go need to change then.

// Offered returns the extensions this client announced in hello.
func (c *Client) Offered() []string { return append([]string(nil), c.offered...) }

// Negotiated returns the extensions the server accepted (always a subset of Offered).
func (c *Client) Negotiated() []string { return append([]string(nil), c.accepted...) }

// HasNegotiated reports whether name was offered and accepted.
func (c *Client) HasNegotiated(name string) bool {
	for _, n := range c.accepted {
		if n == name {
			return true
		}
	}
	return false
}

// SendExt sends one extension message (fire and forget). fields are the message's own fields; v
// and t are added. It refuses to send when nothing was negotiated, so extension traffic never
// reaches a server that did not agree to it.
func (c *Client) SendExt(ctx context.Context, t string, fields map[string]any) error {
	if len(c.accepted) == 0 {
		return &ProtoError{Code: proto.ErrBadRequest, Message: "no protocol extension was negotiated with this server"}
	}
	m := map[string]any{"v": proto.Version, "t": t}
	for k, v := range fields {
		m[k] = v
	}
	return c.write(ctx, m)
}

// RequestExt sends one extension message with a request id and waits for the answer: the Ext
// event with the same id (returned) or an error.
func (c *Client) RequestExt(ctx context.Context, t string, fields map[string]any) (Ext, error) {
	if len(c.accepted) == 0 {
		return Ext{}, &ProtoError{Code: proto.ErrBadRequest, Message: "no protocol extension was negotiated with this server"}
	}
	r, err := c.ext(ctx, t, func(string) map[string]any {
		m := map[string]any{}
		for k, v := range fields {
			m[k] = v
		}
		return m
	})
	if err != nil {
		return Ext{}, err
	}
	if e, ok := r.(Ext); ok {
		return e, nil
	}
	return Ext{}, &ProtoError{Code: proto.ErrBadRequest, Message: "unexpected answer to an extension request"}
}

func intersect(offer, accepted []string) []string {
	var out []string
	for _, a := range accepted {
		for _, o := range offer {
			if a == o {
				out = append(out, a)
				break
			}
		}
	}
	return out
}
