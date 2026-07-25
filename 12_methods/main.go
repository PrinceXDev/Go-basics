package main

// ============================================================================
// CONCEPT: Methods — functions attached to a type via a RECEIVER.
//
// Go has no classes, so "methods" are just regular functions with one extra
// piece of syntax: a receiver, written between `func` and the method name.
// This is how Go attaches behavior to the data structs defined in lesson 11.
//
// JS/TS comparison: this replaces `class Person { greet() {...} }`. Instead
// of methods living INSIDE the type definition, they're declared
// separately, and can even be added in a different file.
//
// THE BIG DECISION: VALUE receiver vs POINTER receiver
//   func (p Person) Method()   -> value receiver: gets a COPY of the struct
//   func (p *Person) Method()  -> pointer receiver: gets the REAL struct,
//                                  can modify it, avoids copying
// Rule of thumb: if the method needs to modify the struct, or the struct is
// large (copying it would be wasteful), use a pointer receiver. Read-only,
// small structs can use value receivers. (Pointers get their own deep-dive
// in lesson 13 — you just need enough here to understand receivers.)
// ============================================================================

import "fmt"

type Rectangle struct {
	Width  float64
	Height float64
}

// VALUE RECEIVER: `r` here is a COPY of whatever Rectangle called this
// method. Reading fields is fine; this method cannot change the original.
func (r Rectangle) Area() float64 {
	return r.Width * r.Height
}

func (r Rectangle) Perimeter() float64 {
	return 2 * (r.Width + r.Height)
}

// POINTER RECEIVER: `r` here is a POINTER to the original Rectangle (the
// `*` means "pointer to"). Changes made inside this method persist on the
// original struct after the call — because it's not a copy.
func (r *Rectangle) Scale(factor float64) {
	r.Width *= factor
	r.Height *= factor
}

type Counter struct {
	value int
}

// Another pointer receiver example — incrementing only makes sense if it
// mutates the real Counter, so a value receiver here would silently do
// nothing useful (it would increment a copy that's immediately discarded).
func (c *Counter) Increment() {
	c.value++
}

func (c Counter) Value() int {
	return c.value
}

func main() {
	rect := Rectangle{Width: 4, Height: 3}

	fmt.Println("Area:", rect.Area())
	fmt.Println("Perimeter:", rect.Perimeter())

	// Calling a pointer-receiver method on a plain variable — Go
	// automatically takes the address for you (equivalent to (&rect).Scale(2)).
	// This automatic conversion is a Go convenience; it only works because
	// `rect` is an addressable variable.
	rect.Scale(2)
	fmt.Println("After Scale(2), width:", rect.Width, "height:", rect.Height)
	fmt.Println("New area:", rect.Area())

	counter := Counter{}
	counter.Increment()
	counter.Increment()
	counter.Increment()
	fmt.Println("Counter value:", counter.Value())

	// Proving the value-receiver "copy" behavior: calling Area() (value
	// receiver) never changes rect, only Scale() (pointer receiver) does.
	before := rect.Width
	rect.Area() // does nothing to rect itself
	fmt.Println("Width unchanged by Area():", rect.Width == before)
}
