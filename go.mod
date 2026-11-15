module github.com/itsnotyoutoday/squatClientDaemon

go 1.24

require (
	github.com/coder/websocket v1.8.15
	github.com/yuin/gopher-lua v1.1.2
	golang.org/x/crypto v0.33.0
	golang.org/x/sys v0.30.0
)

require github.com/itsnotyoutoday/gtfparser v0.0.10

require github.com/itsnotyoutoday/cmcp/go v0.0.0-00010101000000-000000000000

// cmcp is not on GitHub yet: resolve it from the sibling directory (CI needs it checked out next to this repo).
replace github.com/itsnotyoutoday/cmcp/go => ../cmcp/go
