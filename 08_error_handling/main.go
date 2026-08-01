package main

// ============================================================================
// CONCEPT: Error handling — Go's `error` type, and why Go has NO try/catch.
//
// WHY GO DOES THIS DIFFERENTLY
// JS/TS uses exceptions: `throw new Error(...)`, caught by `try/catch`.
// Control flow can jump arbitrarily far up the call stack, and it's easy to
// forget to catch something.
//
// Go treats errors as ORDINARY VALUES, returned alongside a result (see
// lesson 07's `safeDivide`). There is no `throw`, no `catch`. You simply
// check `if err != nil` right after every call that can fail. This is more
// verbose, but the tradeoff is deliberate: you can SEE every point where
// something might fail, directly in the code, and the compiler makes it
// hard to silently ignore an error (unused variables don't compile).
//
// Go does have `panic`/`recover`, which look like throw/catch, but they are
// reserved for truly exceptional, unrecoverable situations (e.g. programmer
// bugs like array-out-of-bounds) — NOT for expected failure cases like
// "file not found" or "invalid input". Using an error return is idiomatic;
// panic is the exception (pun intended), not the rule.
// ============================================================================

import (
	"errors"
	"fmt"
)

// The `error` type is just an interface with one method: Error() string.
// You rarely build custom error types by hand as a beginner — instead you
// create them with helper functions:
//   errors.New("message")         -> simple static error
//   fmt.Errorf("template %s", x)  -> formatted error message

// Define specific, reusable error value you can check
// identity against later. Common idiom for "known" error conditions.
var ErrInsufficientFunds = errors.New("insufficient funds")

func withdraw(balance, amount int) (int, error) {
	if amount > balance {
		return balance, ErrInsufficientFunds
	}
	return balance - amount, nil
}

// Custom error with more context, built using fmt.Errorf and the special
// %w verb, which "wraps" an underlying error so the original cause is
// preserved and can be checked later with errors.Is / errors.As.
func processOrder(itemCount, stock int) error {
	if itemCount > stock {
		return fmt.Errorf("processing order failed: %w", errors.New("not enough stock"))
	}
	return nil
}

func main() {
	// ---- Pattern 1: the standard "check err immediately" idiom ----
	newBalance, err := withdraw(100, 30)
	if err != nil {
		fmt.Println("Withdrawal failed:", err)
	} else {
		fmt.Println("New balance:", newBalance)
	}

	// Trigger the actual failure path
	_, err = withdraw(100, 500)
	if err != nil {
		fmt.Println("Withdrawal failed:", err)
	}

	// ---- Pattern 2: checking a SPECIFIC known error with errors.Is ----
	// This is how you compare against a sentinel error like
	// ErrInsufficientFunds, rather than comparing error message strings
	// (comparing strings is fragile — messages can change wording).
	if errors.Is(err, ErrInsufficientFunds) {
		fmt.Println("-> Specifically: the customer needs to add funds.")
	}

	// ---- Pattern 3: wrapped errors ----
	err = processOrder(10, 5)
	if err != nil {
		fmt.Println("Order error:", err)
	}

	// ---- Pattern 4: panic/recover — for truly exceptional cases only ----
	// `defer` schedules a function to run when the surrounding function
	// returns (more on defer later) — here it's paired with `recover()`
	// to catch a panic and prevent the whole program from crashing.
	fmt.Println("-- demonstrating panic/recover --")
	safeDivide(10, 0)
	fmt.Println("Program continues normally after the recovered panic.")
}

func safeDivide(a, b int) {
	defer func() {
		// recover() only does something useful inside a deferred function,
		// and only if a panic actually happened.
		if r := recover(); r != nil {
			fmt.Println("Recovered from panic:", r)
		}
	}()

	if b == 0 {
		// Dividing an int by zero in Go doesn't return Infinity like JS —
		// it PANICS immediately (a runtime crash), because Go considers
		// this a programmer error, not a recoverable condition.
		panic("attempted division by zero")
	}

	fmt.Println(a / b)
}
