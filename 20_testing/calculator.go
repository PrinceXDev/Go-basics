package calculator

import "errors"

var errDivideByZero = errors.New("division by zero")

// ============================================================================
// CONCEPT: Testing — Go's built-in `testing` package.
//
// WHY THIS FOLDER IS DIFFERENT
// Every previous lesson was `package main` run with `go run`. Testing
// works differently: Go's test tooling operates per-PACKAGE, and by
// convention test code lives in a file ending in `_test.go` right next to
// the code it tests, in the SAME package. So this lesson has two files:
//   calculator.go       -> the code being tested (this file)
//   calculator_test.go  -> the tests for it
// Run tests with: go test ./20_testing/
//
// JS/TS comparison: no separate test framework needed (no Jest/Mocha/Vitest
// install) — `testing` ships with the language itself. `go test` is the
// direct equivalent of `npm test`.
// ============================================================================

func Add(a, b int) int {
	return a + b
}

func Divide(a, b int) (int, error) {
	if b == 0 {
		return 0, errDivideByZero
	}
	return a / b, nil
}

// IsEven reports whether n is even.
func IsEven(n int) bool {
	return n%2 == 0
}
