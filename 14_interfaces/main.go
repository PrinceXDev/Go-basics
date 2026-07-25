package main

// ============================================================================
// CONCEPT: Interfaces — Go's structural typing ("duck typing" with a
// compile-time safety net).
//
// WHY THIS MATTERS
// A Go interface defines a SET OF METHODS. Any type that implements those
// methods automatically satisfies the interface — there is NO `implements`
// keyword, no explicit declaration required. If it has the right methods,
// it qualifies. This is called STRUCTURAL typing.
//
// JS/TS comparison: this is remarkably close to a TypeScript structural
// interface — TS doesn't care what you NAME a type as long as its shape
// matches. Go takes that same idea and applies it to BEHAVIOR (methods)
// instead of just data shape.
//
// WHY USE INTERFACES AT ALL?
// They let you write functions that accept "anything that can do X"
// instead of "only this exact struct type" — enabling polymorphism without
// class inheritance.
// ============================================================================

import "fmt"

// An interface: any type with an Area() float64 method AND a
// Perimeter() float64 method satisfies this interface automatically.
type Shape interface {
	Area() float64
	Perimeter() float64
}

type Rectangle struct {
	Width, Height float64
}

func (r Rectangle) Area() float64      { return r.Width * r.Height }
func (r Rectangle) Perimeter() float64 { return 2 * (r.Width + r.Height) }

type Circle struct {
	Radius float64
}

func (c Circle) Area() float64      { return 3.14159 * c.Radius * c.Radius }
func (c Circle) Perimeter() float64 { return 2 * 3.14159 * c.Radius }

// This function accepts ANY Shape — Rectangle, Circle, or any future type
// that implements Area() and Perimeter(). Neither Rectangle nor Circle
// mentions "Shape" anywhere in their own definition — that's the
// structural typing at work.
func describeShape(s Shape) {
	fmt.Printf("Area: %.2f, Perimeter: %.2f\n", s.Area(), s.Perimeter())
}

// The EMPTY INTERFACE `any` (alias for `interface{}`) has NO required
// methods, so literally every type satisfies it. It's Go's escape hatch
// for "I don't know/care about the type" — closest JS/TS equivalent is
// the `any` type in TypeScript, or a plain untyped value in JS.
// Use sparingly: you lose compile-time type safety when you reach for it.
func printAnything(v any) {
	fmt.Println("value:", v)
}

func main() {
	rect := Rectangle{Width: 4, Height: 3}
	circle := Circle{Radius: 5}

	// Both satisfy Shape despite being unrelated types with no shared
	// parent — this is polymorphism via structural typing, not inheritance.
	describeShape(rect)
	describeShape(circle)

	// A slice of the INTERFACE type can hold a mix of concrete types.
	shapes := []Shape{rect, circle, Rectangle{Width: 2, Height: 2}}
	fmt.Println("-- iterating mixed shapes --")
	for _, s := range shapes {
		describeShape(s)
	}

	// `any` accepts literally anything.
	printAnything(42)
	printAnything("hello")
	printAnything(rect)
	printAnything(true)

	// TYPE ASSERTION: recovering the concrete type from an interface
	// value, when you need to. The "comma-ok" idiom (same pattern as
	// maps in lesson 10) avoids a panic if the assertion is wrong.
	var s Shape = circle
	if c, ok := s.(Circle); ok {
		fmt.Println("s is a Circle with radius:", c.Radius)
	}
	if _, ok := s.(Rectangle); !ok {
		fmt.Println("s is definitely not a Rectangle")
	}

	// TYPE SWITCH: like a regular switch (lesson 06), but switches on the
	// DYNAMIC TYPE stored inside an interface value.
	fmt.Println("-- type switch --")
	describeAny(rect)
	describeAny(circle)
	describeAny(123)
}

func describeAny(v any) {
	switch val := v.(type) {
	case Rectangle:
		fmt.Println("It's a Rectangle with width", val.Width)
	case Circle:
		fmt.Println("It's a Circle with radius", val.Radius)
	default:
		fmt.Println("Unknown type:", val)
	}
}
