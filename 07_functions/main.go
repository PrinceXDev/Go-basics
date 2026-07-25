package main

// ============================================================================
// CONCEPT: Functions — parameters, MULTIPLE return values, named returns,
// and variadic functions.
//
// WHY THIS MATTERS
// Go functions look similar to JS at a glance, but one feature is totally
// foreign to JS: a Go function can return more than one value NATIVELY,
// without wrapping them in an object or array. This single feature is the
// backbone of Go's entire error-handling philosophy (next lesson, 08).
// ============================================================================

import "fmt"

// Basic function: name, then (parameters), then return TYPE, then body.
// Note the type comes AFTER the parameter name — opposite of TS.
// TS:  function add(a: number, b: number): number
// Go:  func add(a int, b int) int
func add(a int, b int) int {
	return a + b
}

// When consecutive parameters share a type, you can omit the type on all
// but the last one. `a, b int` means "both a and b are int".
func multiply(a, b int) int {
	return a * b
}

// MULTIPLE RETURN VALUES — this is the big one.
// A function can return more than one value, comma-separated. No array,
// no object wrapper — it's a first-class language feature.
// JS/TS comparison: the closest JS gets is `return [a, b]` + array
// destructuring, or returning `{a, b}` — both are workarounds. Go builds
// this in at the language level.
func divide(a, b int) (int, int) {
	quotient := a / b
	remainder := a % b // % is the modulo (remainder) operator
	return quotient, remainder
}

// The MOST common use of multiple returns: returning a result AND an error
// side by side. This pattern appears in almost every Go standard library
// function and is the foundation of lesson 08 (error handling).
// By convention, the error is always the LAST return value.
func safeDivide(a, b int) (int, error) {
	if b == 0 {
		// fmt.Errorf builds an error with a formatted message — like
		// throwing an Error in JS, but returned as a value, not thrown.
		return 0, fmt.Errorf("cannot divide %d by zero", a)
	}
	return a / b, nil // `nil` means "no error" — the Go equivalent of null,
	// but ONLY valid for types that support it (pointers, interfaces,
	// slices, maps, channels, functions — not basic types like int/string).
}

// NAMED RETURN VALUES — you can name the return values in the function
// signature. They start at their zero value automatically, and a bare
// `return` (no arguments) returns whatever they currently hold.
// This is a Go-specific convenience; use sparingly, it can hurt readability
// in longer functions, but it's common in short ones.
func rectangleStats(width, height float64) (area, perimeter float64) {
	area = width * height
	perimeter = 2 * (width + height)
	return // "naked return" — returns area and perimeter automatically
}

// VARIADIC FUNCTIONS — a function that accepts a variable number of
// arguments of the same type, using `...` before the type.
// JS/TS comparison: exactly like JS rest parameters — `function sum(...nums)`.
// Inside the function, `nums` behaves like a slice (Go's dynamic array,
// covered in lesson 09).
func sum(nums ...int) int {
	total := 0
	for _, n := range nums {
		total += n
	}
	return total
}

func main() {
	fmt.Println("add(2, 3) =", add(2, 3))
	fmt.Println("multiply(4, 5) =", multiply(4, 5))

	// Multiple return values are captured with comma-separated variables.
	q, r := divide(17, 5)
	fmt.Println("17 / 5 -> quotient:", q, "remainder:", r)

	// The idiomatic Go pattern: check `err` immediately after the call.
	// You'll write this shape of code constantly in Go.
	result, err := safeDivide(10, 2)
	if err != nil {
		fmt.Println("Error:", err)
	} else {
		fmt.Println("10 / 2 =", result)
	}

	// Now trigger the actual error path:
	result, err = safeDivide(10, 0)
	if err != nil {
		fmt.Println("Error:", err)
	} else {
		fmt.Println("10 / 0 =", result)
	}

	area, perimeter := rectangleStats(4.0, 3.0)
	fmt.Printf("Rectangle -> area: %.2f, perimeter: %.2f\n", area, perimeter)

	fmt.Println("sum(1,2,3) =", sum(1, 2, 3))
	fmt.Println("sum(10,20,30,40) =", sum(10, 20, 30, 40))
	fmt.Println("sum() with no args =", sum()) // valid — total stays 0

	// If you already have a slice (covered next in 09), you can "spread"
	// it into a variadic function using `...` — same idea as JS spread.
	nums := []int{5, 10, 15}
	fmt.Println("sum(nums...) =", sum(nums...))

	// If you don't need a return value at all, discard it with `_`,
	// exactly like the for-range index in lesson 06.
	_, remainderOnly := divide(20, 3)
	fmt.Println("remainder only:", remainderOnly)
}
