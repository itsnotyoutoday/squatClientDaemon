module github.com/itsnotyoutoday/squatClientDaemon

go 1.24

require (
	github.com/coder/websocket v1.8.15
	github.com/yuin/gopher-lua v1.1.2
)

require github.com/itsnotyoutoday/gtfparser v0.0.1

// LOCAL DEVELOPMENT ONLY: unreleased engine changes ("core:" commits listed in
// docs/TF-ENGINE.md) live in this worktree. Delete this line once they are
// released and bump the require above to the new tag.
replace github.com/itsnotyoutoday/gtfparser => /Users/rock/mush/squat/gtfparser-squat2
