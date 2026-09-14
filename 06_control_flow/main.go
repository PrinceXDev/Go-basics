package main

/* ============================================================================
CONCEPT: Control flow — if/else, for (Go's ONLY loop), and switch

PART A — if / else
Mechanically similar to JS, but two Go-specific rules matter:
  1. No parentheses required around the condition: `if x > 5 {` not
     `if (x > 5) {`. Braces `{}` are MANDATORY even for one-line bodies
     (unlike JS, which allows `if (x) doThing();` with no braces).
  2. Go allows an "init statement" before the condition, scoped only to
     the if/else chain — see the example below. This is a common Go
     idiom, especially paired with functions that return (value, error).

PART B — for
Go has exactly ONE looping keyword: `for`. There is no `while`, no
`do-while`, no `foreach` keyword. `for` covers all of them by varying
what you put in the parentheses-less header.

PART C — switch
Similar purpose to JS switch, but with two big differences:
  1. No `break` needed — Go does NOT fall through to the next case by
     default (JS does, which is why JS needs `break` everywhere).
  2. A `switch` with no condition at all acts like a clean if/else-if
     chain — very idiomatic Go.
============================================================================ */

import "fmt"

func main() {
	// ---------- PART A: if / else ----------
	age := 20

	if age >= 18 {
		fmt.Println("Adult")
	} else if age >= 13 {
		fmt.Println("Teenager")
	} else {
		fmt.Println("Child")
	}

	if money := 100000; money > 1500000 {
		fmt.Println("Rich")
	} else if money < 500000 {
		fmt.Println("Poor")
	} else {
		fmt.Println("leave in life of death")
	}

	// Go-specific: an init statement before the condition, using `;` to
	// separate it from the actual condition. `score` only exists inside
	// this if/else block — it disappears right after, just like the
	// block-scope rule from lesson 02.
	if score := 85; score >= 90 {
		fmt.Println("Grade: A")
	} else if score >= 75 {
		fmt.Println("Grade: B")
	} else {
		fmt.Println("Grade: C")
	}
	// fmt.Println(score) // ERROR: score is undefined here

	// ---------- PART B: for (the only loop) ----------

	// 1. Classic three-part for loop (init; condition; post) — same shape
	//    as JS `for (let i = 0; i < 5; i++)`, minus the parentheses.
	fmt.Println("-- classic for --")
	for i := 0; i < 5; i++ {
		fmt.Println("i =", i)
	}

	// 2. "while loop" — just drop the init and post parts. This IS Go's
	//    while loop; there is no separate `while` keyword.
	fmt.Println("-- while-style for --")
	count := 0
	for count < 3 {
		fmt.Println("count =", count)
		count++
	}

	// 3. Infinite loop — drop everything. Must `break` explicitly to
	//    escape it, or the program runs forever.
	fmt.Println("-- infinite for with break --")
	n := 0
	for {
		if n >= 2 {
			break // exits the loop immediately
		}
		fmt.Println("n =", n)
		n++
	}

	// 4. `for range` — Go's foreach, for iterating over strings, slices,
	//    arrays, and maps. Range gives you (index, value) pairs.
	fmt.Println("-- for range over a string --")
	word := "Go"
	for index, letter := range word {
		// %c formats a rune (character) as its printable character
		fmt.Printf("index=%d letter=%c\n", index, letter)
	}

	// If you don't need the index, Go requires you to explicitly ignore
	// it with `_` (the "blank identifier"). You cannot just omit it —
	// Go treats an unused declared variable as a COMPILE ERROR, so `_`
	// exists specifically to say "I know this exists, I'm ignoring it."
	fmt.Println("-- for range, ignoring index --")
	for _, letter := range word {
		fmt.Printf("letter=%c\n", letter)
	}

	// ---------- PART C: switch ----------
	day := 3

	fmt.Println("-- switch with a value --")
	switch day {
	case 1:
		fmt.Println("Monday")
	case 2:
		fmt.Println("Tuesday")
	case 3:
		fmt.Println("Wednesday")
	default:
		fmt.Println("Some other day")
		// No `break` anywhere above: Go automatically stops after the
		// matching case. In JS, forgetting `break` would fall through
		// into the next case — a classic JS footgun Go avoids by design.
	}

	// switch with NO condition = clean if/else-if chain.
	// Idiomatic Go often prefers this over a long if/else-if ladder.
	temp := 30
	fmt.Println("-- switch with no condition --")
	switch {
	case temp > 35:
		fmt.Println("Hot")
	case temp > 20:
		fmt.Println("Warm")
	default:
		fmt.Println("Cold")
	}

	/* (Javascript)
	let scores = [];
	*/

	// make() used to creating a slice
	// []int the type: a slice of integers, 0 is length and 5 is capacity
	scores := make([]int, 0, 5)
	fmt.Println(scores, len(scores), cap(scores))

	scores = append(scores, 100)
	scores = append(scores, 100, 500, 700, 800, 900, 1000)

	fmt.Println("updated scores =>", scores)

	// new experiment

	shares := make([]int, 0, 2) // cap is 2
	shares = append(shares, 2)  // full
	fmt.Println("till not double", cap(shares))

	shares = append(shares, 5, 10) // double size and copy previous element into here

	fmt.Println("shares", shares)
	fmt.Println("length of shares", len(shares))
	fmt.Println("Capacity of shares", cap(shares))

	todos := []string{"learn go", "learn aws", "learn rag"}
	more := []string{"learn python", "learn js"}

	todos = append(todos, more...)
	fmt.Println("Todos", todos)

	total := 0
	views := make([]int, 0, 5)
	views = append(views, 10, 20, 30, 40, 50)

	for i, v := range views {
		fmt.Println(i, v)
		total = total + v
	}

	fmt.Println("sum of the items", total)
}
