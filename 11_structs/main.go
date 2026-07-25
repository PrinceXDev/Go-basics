package main

// ============================================================================
// CONCEPT: Structs — Go's way of grouping related data into a custom type.
//
// JS/TS comparison: a Go struct is closest to a TypeScript `interface` or
// `type` used for object shapes, e.g.:
//   TS:  type Person = { name: string; age: number }
//   Go:  type Person struct { Name string; Age int }
// The big difference: Go has NO classes. Structs hold DATA. Behavior
// (methods) is attached SEPARATELY (lesson 12) — Go favors composition of
// small pieces over class hierarchies and inheritance.
//
// CAPITALIZATION MATTERS AGAIN: a field starting with a capital letter
// (Name, Age) is exported (visible outside this package); lowercase would
// be private to the package. Same rule as function names from lesson 01.
// ============================================================================

import "fmt"

// Defining a struct type.
type Person struct {
	Name string
	Age  int
	City string
}

// A struct can contain another struct — composition, not inheritance.
type Address struct {
	Street string
	City   string
	Zip    string
}

type Employee struct {
	Name    string
	Salary  float64
	Address Address // nested struct
}

func main() {
	// Creating a struct value — struct literal with named fields (the
	// idiomatic way; field order doesn't matter when you name them).
	p1 := Person{
		Name: "Alice",
		Age:  30,
		City: "London",
	}
	fmt.Println("p1:", p1)
	fmt.Println("p1.Name:", p1.Name)

	// Positional struct literal — values in DECLARATION order, no field
	// names. Works but is fragile (reordering fields breaks it silently)
	// so named fields are generally preferred, especially with 3+ fields.
	p2 := Person{"Bob", 25, "Paris"}
	fmt.Println("p2:", p2)

	// Zero-value struct: declaring with `var` gives every field its own
	// zero value (0, "", false, etc.) — same idea as lesson 02/04.
	var p3 Person
	fmt.Println("p3 (zero value):", p3)

	// Modifying a field after creation
	p3.Name = "Charlie"
	p3.Age = 40
	fmt.Println("p3 after edits:", p3)

	// Struct EQUALITY: two structs are equal if all their fields are
	// equal — this works out of the box with ==, unlike JS objects
	// (where {} === {} is always false because JS compares references).
	pA := Person{Name: "Dan", Age: 20, City: "NYC"}
	pB := Person{Name: "Dan", Age: 20, City: "NYC"}
	fmt.Println("pA == pB:", pA == pB) // true — compares by VALUE

	// Structs are VALUE TYPES: assigning or passing a struct COPIES it.
	// This is a major difference from JS, where objects are always
	// reference types.
	pCopy := pA
	pCopy.Name = "Changed"
	fmt.Println("pA.Name (unaffected):", pA.Name)
	fmt.Println("pCopy.Name:", pCopy.Name)

	// Nested struct usage
	emp := Employee{
		Name:   "Eve",
		Salary: 75000,
		Address: Address{
			Street: "123 Main St",
			City:   "Berlin",
			Zip:    "10115",
		},
	}
	fmt.Println("employee city:", emp.Address.City)

	// Anonymous struct: a one-off struct type with no name, useful for
	// throwaway groupings (e.g. quick test data) without declaring a
	// named type first.
	config := struct {
		Debug   bool
		Version string
	}{
		Debug:   true,
		Version: "1.0.0",
	}
	fmt.Println("config:", config)
}
