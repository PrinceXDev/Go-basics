package main

// ============================================================================
// CONCEPT: Practicing variable declarations, basic types, and zero values.
//
// WHY THIS FOLDER EXISTS
// Lessons 02 and 03 introduced `var`, `:=`, and Go's basic types (int,
// float64, string, bool) along with "zero values" (the default value a
// variable gets if you declare it without assigning anything). This lesson
// is pure practice: using all three declaration styles side by side and
// deliberately observing a zero value instead of just reading about it.
// ============================================================================

import "fmt"

func main() {
	// 1. Explicit `var` with a type, useful when you want the declaration
	//    and the assignment separated, or want the type to be unmistakable.
	var name string
	name = "Prince"

	// 2. Short declaration `:=` — the idiomatic default inside functions.
	//    Go infers the type from the literal on the right (1000 -> int).
	age := 25

	// 3. Another `:=`, this time inferring float64 from a decimal literal.
	heightMeters := 1.78

	// 4. `var` with NO assignment. This does not error and is not
	//    "undefined" like in JS — Go gives it the zero value for bool,
	//    which is `false`. Every Go type has a defined zero value:
	//      int     -> 0
	//      float64 -> 0.0
	//      string  -> "" (empty string, not null)
	//      bool    -> false
	var isLearningGo bool

	fmt.Println("Name:", name)
	fmt.Println("Age:", age)
	fmt.Println("Height (m):", heightMeters)
	fmt.Println("isLearningGo (zero value, unset):", isLearningGo)

	// 5. Bonus: age * 12 = age in months.
	// Both `age` and `12` are int, so this is safe int multiplication —
	// no mixing of int and float64 is happening here.
	ageInMonths := age * 12
	fmt.Println("Age in months:", ageInMonths)

	// Now that we've SEEN the zero value, actually set it, to show the
	// variable is fully usable afterward — it was never "broken", just
	// initialized to a sensible default.
	isLearningGo = true
	fmt.Println("isLearningGo (after assignment):", isLearningGo)

	cityName := "Mumbai"
	fmt.Println(cityName)

	carPrice := 500000
	fmt.Println(carPrice)
}
