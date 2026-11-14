package engine

import "github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"

// Extension is an optional protocol extension a squatter may accept (see pkg/wire/ext.go). The
// engine announces every registered extension's name in hello; for a squatter that accepts it,
// Negotiated is called once per connection and Message for each extension message the squatter
// sends. Nothing is registered by default, so no squatter is offered anything today.
//
// Calls come from the squatter's event loop: return quickly, and talk back through c.SendExt /
// c.RequestExt from another goroutine if that can block.
type Extension interface {
	// Name is the string announced in hello and matched in the welcome.
	Name() string
	// Negotiated says the squatter accepted the extension on this connection. c stays valid until
	// the connection ends (the next Negotiated call is a new connection).
	Negotiated(squatterID string, c *wire.Client)
	// Message delivers a message type the base protocol does not know, from a squatter that
	// accepted this extension.
	Message(squatterID string, m wire.Ext)
}

// RegisterExtension adds x to the extensions offered to squatters connected from now on.
func (e *Engine) RegisterExtension(x Extension) {
	e.extMu.Lock()
	e.extensions = append(e.extensions, x)
	e.extMu.Unlock()
}

func (e *Engine) offeredExtensions() []string {
	e.extMu.Lock()
	defer e.extMu.Unlock()
	var out []string
	for _, x := range e.extensions {
		out = append(out, x.Name())
	}
	return out
}

// dispatchNegotiated tells every registered extension the squatter accepted that it is live.
func (e *Engine) dispatchNegotiated(id string, c *wire.Client) {
	e.extMu.Lock()
	xs := append([]Extension(nil), e.extensions...)
	e.extMu.Unlock()
	for _, x := range xs {
		if c.HasNegotiated(x.Name()) {
			x.Negotiated(id, c)
		}
	}
}

// dispatchExt hands an unknown message to the extensions the squatter accepted.
func (e *Engine) dispatchExt(id string, c *wire.Client, m wire.Ext) {
	e.extMu.Lock()
	xs := append([]Extension(nil), e.extensions...)
	e.extMu.Unlock()
	for _, x := range xs {
		if c.HasNegotiated(x.Name()) {
			x.Message(id, m)
		}
	}
}
