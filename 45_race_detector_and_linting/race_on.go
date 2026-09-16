//go:build race

package main

// BUILD TAGS (a bonus concept)
// The line `//go:build race` above is a build constraint: this file is only
// compiled when the `race` tag is set, which `go test -race` and
// `go build -race` do automatically. race_off.go has the opposite
// constraint, so exactly one of the two is ever compiled.
//
// The directive must be at the very top of the file, followed by a BLANK
// LINE before `package`. Without the blank line it's just a comment and
// silently does nothing.
//
// Other common tags: //go:build linux, //go:build integration
// (`go test -tags=integration` is the usual way to gate slow tests).
const raceEnabled = true
