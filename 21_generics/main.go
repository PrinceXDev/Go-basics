package main

// ============================================================================
// CONCEPT: Generics — type parameters (added in Go 1.18).
//
// THE PROBLEM GENERICS SOLVE
// Before generics, writing a function that works with multiple types meant
// either duplicating code per type, or using `any` (lesson 14) and losing
// all compile-time type safety. Generics let you write ONE function or
// type that works with many types, while the compiler still checks
// everything at compile time.
//
// JS/TS comparison: this is exactly TypeScript generics.
//   TS: function first<T>(arr: T[]): T { return arr[0] }
//   Go: func First[T any](s []T) T { return s[0] }
// The square-bracket type parameter syntax is nearly identical.
// ============================================================================

import "fmt"

// A generic function: [T any] declares a type parameter T, constrained to
// `any` (meaning: T can be literally any type — no constraint at all).
func First[T any](items []T) T {
	return items[0]
}

// CONSTRAINTS restrict what T is allowed to be, so you can use operators
// like +, <, > inside the function — `any` wouldn't allow that, since Go
// can't know in advance whether + is valid for an arbitrary type.
// `Number` here is a custom constraint: T must be one of these underlying
// numeric types. `~int` means "int, OR any type whose underlying type is
// int" (covers custom named types too).
type Number interface {
	~int | ~float64
}

func Sum[T Number](items []T) T {
	var total T // zero value of whatever T turns out to be (0 or 0.0)
	for _, item := range items {
		total += item
	}
	return total
}

// A generic function with TWO type parameters — a simple map/transform,
// converting a slice of one type into a slice of another.
// JS/TS comparison: this is Go's version of Array.prototype.map().
func Map[T, U any](items []T, transform func(T) U) []U {
	result := make([]U, 0, len(items))
	for _, item := range items {
		result = append(result, transform(item))
	}
	return result
}

// A generic type — Stack[T] works with a stack of ANY single type,
// enforced consistently (a Stack[int] can't accept a string push).
type Stack[T any] struct {
	items []T
}

func (s *Stack[T]) Push(item T) {
	s.items = append(s.items, item)
}

func (s *Stack[T]) Pop() (T, bool) {
	var zero T
	if len(s.items) == 0 {
		return zero, false
	}
	last := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return last, true
}

func main() {
	// Go usually INFERS the type parameter from the arguments — you
	// rarely need to write First[int](...) explicitly.
	ints := []int{10, 20, 30}
	fmt.Println("First(ints):", First(ints))

	words := []string{"go", "is", "fun"}
	fmt.Println("First(words):", First(words))

	fmt.Println("Sum(ints):", Sum(ints))

	floats := []float64{1.5, 2.5, 3.0}
	fmt.Println("Sum(floats):", Sum(floats))

	// Map: transform []int -> []string
	doubled := Map(ints, func(n int) int { return n * 2 })
	fmt.Println("doubled:", doubled)

	labels := Map(ints, func(n int) string { return fmt.Sprintf("num-%d", n) })
	fmt.Println("labels:", labels)

	// Generic struct usage
	intStack := &Stack[int]{}
	intStack.Push(1)
	intStack.Push(2)
	intStack.Push(3)
	val, ok := intStack.Pop()
	fmt.Println("popped:", val, "ok:", ok)

	stringStack := &Stack[string]{}
	stringStack.Push("a")
	stringStack.Push("b")
	sval, _ := stringStack.Pop()
	fmt.Println("popped string:", sval)

	// intStack.Push("oops") // COMPILE ERROR: string doesn't match T=int
	// for this specific Stack instance — this is the type safety generics
	// buy you over using `any` everywhere.
}
