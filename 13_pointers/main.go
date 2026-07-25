package main

// ============================================================================
// CONCEPT: Pointers — variables that store a MEMORY ADDRESS instead of a
// value directly.
//
// WHY THIS MATTERS
// JS/TS has no pointers at all — objects are references under the hood,
// but you never see or manipulate an "address". Go exposes this directly,
// but in a SAFE, limited way: no pointer arithmetic (you can't do
// `ptr + 1` like in C), so most pointer bugs from C simply can't happen.
//
// TWO OPERATORS TO MEMORIZE:
//   &x   -> "address of x"   -> gives you a pointer TO x
//   *ptr -> "dereference"    -> gives you the VALUE stored at that address
//
// WHY USE POINTERS AT ALL?
//   1. To let a function modify the CALLER's variable (lesson 12's pointer
//      receivers use exactly this).
//   2. To avoid copying large structs on every function call.
//   3. To represent "no value" for a struct (nil), similar to null.
// ============================================================================

import "fmt"

type Point struct{ X, Y int }

func main() {
	x := 10
	p := &x // p is now a pointer to x — it holds x's memory address

	fmt.Println("x:", x)
	fmt.Println("p (address):", p)
	fmt.Println("*p (dereferenced -> value at that address):", *p)

	// Changing the value THROUGH the pointer changes the original x.
	*p = 20
	fmt.Println("x after *p = 20:", x)

	// This is the core reason pointers exist: passing by value vs by
	// pointer to a function.
	fmt.Println("-- pass by value (no effect on caller) --")
	value := 5
	incrementByValue(value)
	fmt.Println("value after incrementByValue:", value) // unchanged: 5

	fmt.Println("-- pass by pointer (modifies caller's variable) --")
	incrementByPointer(&value)
	fmt.Println("value after incrementByPointer:", value) // changed: 6

	// A struct example, tying back to lesson 11's copy behavior.
	pt := Point{X: 1, Y: 2}

	movePointValue(pt) // gets a COPY — original pt is untouched
	fmt.Println("pt after movePointValue (unchanged):", pt)

	movePointPointer(&pt) // gets the REAL pt via pointer — it changes
	fmt.Println("pt after movePointPointer (changed):", pt)

	// `new()` allocates memory for a type and returns a pointer to its
	// zero value — an alternative to `&SomeStruct{}`.
	newPoint := new(Point)
	fmt.Println("newPoint (zero value via new()):", *newPoint)
	newPoint.X = 100 // Go auto-dereferences for field access; no need for (*newPoint).X
	fmt.Println("newPoint.X after edit:", newPoint.X)

	// A nil pointer means "points to nothing". Dereferencing a nil
	// pointer PANICS at runtime — the Go equivalent of JS's
	// "Cannot read properties of null".
	var nilPtr *Point
	fmt.Println("nilPtr == nil:", nilPtr == nil)
	// fmt.Println(*nilPtr) // PANIC: runtime error: invalid memory address
}

func incrementByValue(n int) {
	n++ // only changes the local copy
}

func incrementByPointer(n *int) {
	*n++ // dereferences and changes the ORIGINAL variable
}

func movePointValue(p Point) {
	p.X += 10
	p.Y += 10
}

func movePointPointer(p *Point) {
	p.X += 10
	p.Y += 10
}
