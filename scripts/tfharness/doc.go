// Command tfharness exercises the whole tf-squatter stack against real games.
//
// It starts a private copy of CobraMUSH (and, if present, Evennia), builds
// tf-squatterd, launches a real TinyFugue with the bridge, pairs a TFSP client
// and runs a battery of feature checks, printing a PASS/FAIL table. With
// --interactive it then leaves everything running and gives you a small
// console acting as the remote client.
//
//	go run ./scripts/tfharness --tf-bin ~/tf/src/tf --tf-lib ~/tf/lib/tf
//
// See scripts/tfharness/README.md.
package main
