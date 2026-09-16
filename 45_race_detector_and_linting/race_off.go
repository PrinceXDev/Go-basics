//go:build !race

package main

// The counterpart to race_on.go — compiled when the race detector is OFF.
// See that file for how build tags work.
const raceEnabled = false
