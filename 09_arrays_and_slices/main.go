package main

// ============================================================================
// CONCEPT: Arrays (fixed-size) vs Slices (dynamic) — Go's list types.
//
// WHY TWO TYPES?
// JS has one array type that resizes freely. Go splits this into two:
//   - ARRAY: fixed length, baked into its TYPE ([5]int is a different type
//     from [3]int). Rarely used directly in everyday Go code.
//   - SLICE: a flexible, resizable VIEW over an underlying array. This is
//     what you use 95% of the time — it's Go's real equivalent of a JS
//     array.
// ============================================================================

import "fmt"

func main() {
	// ---------- ARRAYS (fixed size, rarely used directly) ----------
	var scores [3]int // array of exactly 3 ints, zero-valued: [0 0 0]
	scores[0] = 90
	scores[1] = 85
	scores[2] = 95
	fmt.Println("array scores:", scores)
	fmt.Println("array length:", len(scores))
	// scores[3] = 100 // COMPILE ERROR: index out of range — size is fixed

	// ---------- SLICES (dynamic, the one you'll actually use) ----------

	// Slice literal — looks like an array literal but WITHOUT a size
	// inside the brackets. This is the key syntactic difference:
	//   [3]int{1, 2, 3}  -> array
	//   []int{1, 2, 3}   -> slice
	fruits := []string{"apple", "banana", "cherry"}
	fmt.Println("slice fruits:", fruits)
	fmt.Println("len(fruits):", len(fruits))

	// append() adds elements and returns a (possibly new) slice — you
	// MUST reassign the result. This surprises JS devs used to
	// `array.push()` mutating in place.
	// JS: fruits.push("date")
	// Go: fruits = append(fruits, "date")
	fruits = append(fruits, "date")
	fmt.Println("after append:", fruits)

	// append() also accepts multiple values, or another slice via `...`
	moreFruits := []string{"elderberry", "fig"}
	fruits = append(fruits, moreFruits...)
	fmt.Println("after appending another slice:", fruits)

	// SLICING syntax: slice[start:end] — end is EXCLUSIVE (up to, not
	// including, that index). Same convention as JS's Array.slice().
	fmt.Println("fruits[1:3]:", fruits[1:3]) // banana, cherry
	fmt.Println("fruits[:2]:", fruits[:2])   // apple, banana (start omitted = 0)
	fmt.Println("fruits[3:]:", fruits[3:])   // from index 3 to the end

	// make() creates a slice with a starting LENGTH and (optionally) a
	// CAPACITY — useful when you know roughly how big it'll get, to avoid
	// repeated re-allocation. len = "how many elements now", cap = "how
	// much room before the underlying array must grow".
	numbers := make([]int, 0, 5) // length 0, capacity 5
	for i := 1; i <= 5; i++ {
		numbers = append(numbers, i*10)
	}
	fmt.Println("numbers:", numbers, "len:", len(numbers), "cap:", cap(numbers))

	// Iterating a slice — same `for range` you learned in lesson 06.
	fmt.Println("-- iterating numbers --")
	for index, value := range numbers {
		fmt.Printf("index=%d value=%d\n", index, value)
	}

	// Removing an element: Go has NO built-in remove/splice function.
	// The idiomatic way is to slice around the unwanted index and
	// re-append. Removing index 2 (value 30):
	indexToRemove := 2
	numbers = append(numbers[:indexToRemove], numbers[indexToRemove+1:]...)
	fmt.Println("after removing index 2:", numbers)

	// A 2D slice (slice of slices) — Go's equivalent of a nested array,
	// e.g. a grid or matrix.
	grid := [][]int{
		{1, 2, 3},
		{4, 5, 6},
	}
	fmt.Println("grid[1][2]:", grid[1][2]) // 6

	// IMPORTANT GOTCHA: slices SHARE the underlying array when sliced.
	// Modifying a sub-slice can affect the original — this trips up
	// nearly everyone coming from JS (where slice() always copies).
	original := []int{1, 2, 3, 4, 5}
	sub := original[1:3] // view into original, NOT a copy
	sub[0] = 999
	fmt.Println("original after modifying sub-slice:", original) // [1 999 3 4 5]

	// To get a real independent copy, use the built-in copy() function.
	safeCopy := make([]int, len(original))
	copy(safeCopy, original)
	safeCopy[0] = -1
	fmt.Println("original untouched:", original)
	fmt.Println("safeCopy:", safeCopy)
}
