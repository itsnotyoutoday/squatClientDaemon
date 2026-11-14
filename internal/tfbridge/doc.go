// Package tfbridge is the Backend behind tf-squatterd: it serves TFSP to remote
// clients (pkg/wire) while TinyFugue, reached through a local socketpair using
// the TFCTL line protocol, owns the game connections. See docs/TF-SQUATTER.md.
package tfbridge
