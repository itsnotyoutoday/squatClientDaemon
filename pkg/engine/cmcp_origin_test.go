package engine

import (
	"testing"

	"github.com/itsnotyoutoday/cmcp/go/fetch"
)

func fetchOrigin(o string) fetch.Origin { return fetch.Origin{Origin: o} }

// The pack origin rule (spec 8.4): the game's own host, or an https origin the player allowed. The "Always" pack setting allows the origin this connection's
// Welcome announced (a CDN such as the game's Pages host); Ask and Never allow nothing beyond the game host; http and other origins stay refused.
func TestOriginPolicy(t *testing.T) {
	const cdn = "https://cmcp-game-packs.pages.dev"
	for _, c := range []struct {
		name, consent, origin string
		ok                    bool
	}{
		{"always allows the announced CDN", "always", cdn, true},
		{"always, mixed case", "always", "https://CMCP-game-packs.pages.dev", true},
		{"ask refuses a CDN", "ask", cdn, false},
		{"never refuses a CDN", "never", cdn, false},
		{"always still refuses another origin than the announced one", "always", "https://evil.example", false},
		{"the game's own host is always fine", "ask", "https://game.example.org", true},
		{"http is refused (no dev flag)", "always", "http://cmcp-game-packs.pages.dev", false},
	} {
		announced := cdn
		if c.name == "always, mixed case" {
			announced = "https://CMCP-game-packs.pages.dev"
		}
		p := originPolicy("game.example.org", announced, c.consent, false, false, false)
		_, err := p.CheckOrigin(fetchOrigin(c.origin))
		if (err == nil) != c.ok {
			t.Errorf("%s: origin %s: ok=%v err=%v", c.name, c.origin, err == nil, err)
		}
	}
}
