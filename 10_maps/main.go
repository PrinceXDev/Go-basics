package main

// ============================================================================
// CONCEPT: Maps — Go's key/value store (like a JS object or a Map).
//
// JS/TS comparison: a Go `map[KeyType]ValueType` behaves much like a JS
// `Map<K, V>` or a plain object used as a dictionary. Differences:
//   - Every key must be the SAME declared type (no mixing string/number
//     keys), and every value must be the same declared type too.
//   - Iteration order is NOT guaranteed and deliberately randomized by Go
//     across runs — never rely on map order (JS objects/Maps preserve
//     insertion order; Go maps explicitly do not).
//   - Reading a missing key does NOT error or return undefined — it
//     returns the VALUE TYPE's zero value, which is why the "comma-ok"
//     idiom below exists (to tell "missing" apart from "present but zero").
// ============================================================================

import "fmt"

func main() {
	// Map literal: map[KeyType]ValueType{ key: value, ... }
	ages := map[string]int{
		"Alice": 30,
		"Bob":   25,
	}
	fmt.Println("ages:", ages)

	// Adding / updating a key — same syntax either way, Go figures out
	// whether it's an insert or an update.
	ages["Charlie"] = 35
	ages["Bob"] = 26 // update existing key
	fmt.Println("after add/update:", ages)

	// Reading a value
	fmt.Println("Alice's age:", ages["Alice"])

	// THE GOTCHA: reading a key that doesn't exist returns the zero value
	// for the value type (0 for int here) — NOT an error, NOT undefined.
	fmt.Println("Unknown person's age (zero value):", ages["Zoe"])

	// THE FIX: the "comma-ok" idiom. A second return value tells you
	// whether the key actually existed. This is the correct way to check
	// map membership in Go — you'll see `, ok` constantly in real code.
	age, ok := ages["Zoe"]
	fmt.Println("Zoe present?", ok, "| value:", age)

	age, ok = ages["Alice"]
	fmt.Println("Alice present?", ok, "| value:", age)

	// Deleting a key — built-in delete() function, no `delete obj[key]`
	// like JS.
	delete(ages, "Charlie")
	fmt.Println("after delete Charlie:", ages)

	// Checking length works the same as slices/arrays/strings: len().
	fmt.Println("number of people:", len(ages))

	// Iterating a map with for-range gives (key, value) pairs.
	// Remember: order is NOT guaranteed — run this program multiple
	// times and the print order may change.
	fmt.Println("-- iterating map --")
	for name, personAge := range ages {
		fmt.Printf("%s is %d years old\n", name, personAge)
	}

	// make() also works for maps, useful when starting empty and filling
	// it dynamically (you cannot assign into a nil map — see below).
	inventory := make(map[string]int)
	inventory["apples"] = 50
	inventory["bananas"] = 30
	fmt.Println("inventory:", inventory)

	// A DECLARED-BUT-UNINITIALIZED map (`var m map[K]V`) is `nil`.
	// Reading from a nil map is safe and returns zero values, but WRITING
	// to one panics at runtime — a common beginner bug.
	var nilMap map[string]int
	fmt.Println("read from nil map (safe):", nilMap["anything"])
	fmt.Println("is nilMap nil?", nilMap == nil)
	// nilMap["x"] = 1 // PANIC: assignment to entry in nil map — always
	// initialize maps with `make()` or a literal before writing to them.

	// Maps as a simple "set" (Go has no built-in Set type): use
	// map[T]bool or map[T]struct{} and check for key presence.
	uniqueTags := map[string]bool{}
	tags := []string{"go", "backend", "go", "learning", "backend"}
	for _, tag := range tags {
		uniqueTags[tag] = true
	}
	fmt.Println("unique tags count:", len(uniqueTags))
	for tag := range uniqueTags {
		fmt.Println("tag:", tag)
	}
}
