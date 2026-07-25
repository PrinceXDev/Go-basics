// Package mathutils demonstrates a small reusable Go package.
//
// WHY THIS FILE EXISTS
// Every real Go project splits code across multiple packages instead of
// cramming everything into package main. A package is just a FOLDER whose
// .go files all declare the same `package <name>` at the top — the folder
// name and package name are conventionally (but not required to be) the
// same.
//
// JS/TS comparison: this folder is like a small local npm module. Instead
// of `import { add } from "./mathutils"`, Go uses the full IMPORT PATH,
// built from the module name in go.mod plus the folder path — see
// main.go in the parent folder for the actual import.
package mathutils

// Only EXPORTED (capitalized) names are usable from outside this package —
// same capital/lowercase visibility rule from lesson 01, now applied across
// package boundaries instead of just within one file.
func Add(a, b int) int {
	return a + b
}

func Multiply(a, b int) int {
	return a * b
}

// square is unexported (lowercase) — it can be used INSIDE this package
// (see Square below) but is invisible to any code outside mathutils.
func square(n int) int {
	return n * n
}

func Square(n int) int {
	return square(n)
}
