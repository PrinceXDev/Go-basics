package main

// ============================================================================
// CONCEPT: Constants (`const`) and formatted output (`fmt.Printf` verbs)
//
// PART A — CONSTANTS
// A `const` is a value that is fixed at compile time and can NEVER change
// while the program runs. Unlike a `var`, if you try to reassign a const,
// the compiler rejects it immediately (see the commented-out line below).
//
// WHY use const instead of var?
//   1. Intent: it tells every future reader "this value is not supposed to
//      change, ever" — self-documenting code.
//   2. Safety: the compiler enforces that intent for you. In JS, `const`
//      only prevents *reassignment* of the binding, but for objects/arrays
//      the contents can still mutate. Go's const is even stricter: it only
//      works on simple values (numbers, strings, bools) — never structs,
//      slices, or maps — precisely because those wouldn't be truly
//      "constant" in memory.
//
// JS/TS comparison: `const PI = 3.14159` in JS behaves almost identically
// for primitive values. The difference: Go's const values must be
// computable at compile time (no function calls, no runtime data).
//
// PART B — fmt.Printf VERBS
// `fmt.Println` just prints values with a space between them and a newline
// at the end — good for quick debugging, but no control over formatting.
// `fmt.Printf` ("print formatted") lets you build a template string with
// placeholders called VERBS, each starting with %, and lets you control
// EXACTLY how a value appears.
//
// Common verbs:
//   %d   -> integer (decimal)
//   %f   -> float (defaults to 6 decimal places, e.g. 3.140000)
//   %.2f -> float rounded to 2 decimal places
//   %s   -> string
//   %t   -> boolean
//   %v   -> "value" — prints ANY type in its default format (a great
//           fallback when you don't know/care about the exact verb)
//   %T   -> prints the TYPE of the value (useful for learning/debugging)
//   %q   -> a quoted string (wraps it in "double quotes", escapes specials)
//
// JS/TS comparison: this is Go's version of template literals /
// `String.prototype.padStart` / `toFixed()` combined. JS: `` `${price.toFixed(2)}` ``
// Go:  fmt.Printf("%.2f", price)
// ============================================================================

import "fmt"

// Constants declared at package level (outside any function) are visible
// to every function in this file — same visibility rule as `var` at this
// level (see appName in 02_variables_and_types).
const appName = "Go Learning App"
const maxUsers = 100

func main() {
	// Constants can also be declared inside a function, scoped to it.
	const taxRate = 0.18

	fmt.Println("App:", appName, "| Max users:", maxUsers)

	// Uncommenting the next line is a compile error:
	// maxUsers = 200 // cannot assign to maxUsers (declared const)

	// --- fmt.Printf examples ---
	price := 499.999
	quantity := 3
	productName := "Mechanical Keyboard"
	inStock := true

	// %s for string, %d for int, %.2f for float rounded to 2 decimals
	fmt.Printf("Product: %s | Qty: %d | Price: %.2f\n", productName, quantity, price)

	// %t for bool
	fmt.Printf("In stock: %t\n", inStock)

	// %v works for anything — handy when you're not sure of the type yet
	fmt.Printf("Generic value print -> name=%v qty=%v price=%v\n", productName, quantity, price)

	// %T shows you the underlying type — great learning tool
	fmt.Printf("Types -> %T, %T, %T, %T\n", productName, quantity, price, inStock)

	// %q wraps a string in quotes — useful to spot hidden whitespace
	fmt.Printf("Quoted: %q\n", "  spaced out  ")

	// Applying taxRate (const) in a calculation
	total := price * float64(quantity) * (1 + taxRate)
	fmt.Printf("Total with tax: %.2f\n", total)
	fmt.Printf("Price of the product: %.4f\n", total)

	// fmt.Sprintf: same as Printf but returns a STRING instead of printing.
	// Useful when you need the formatted text as a value (e.g. to store,
	// send elsewhere, or concatenate) rather than immediately printing it.
	summary := fmt.Sprintf("%s x%d = %.2f", productName, quantity, total)
	fmt.Println("Summary string:", summary)
}
