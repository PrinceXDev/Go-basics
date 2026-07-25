package main

// ============================================================================
// CONCEPT: Struct embedding — Go's answer to inheritance.
//
// GO HAS NO CLASSES AND NO `extends`. Instead, you EMBED one struct inside
// another (declare a field with just a TYPE name, no field name), and the
// outer struct gains direct access to the embedded struct's fields and
// methods, as if they were its own. This is called "composition" and is
// Go's deliberate alternative to classical inheritance.
//
// JS/TS comparison: this is NOT the same as `class Dog extends Animal`.
// There's no "is-a" polymorphism through a shared base type — Dog does not
// become an Animal you can pass where an Animal interface is expected,
// UNLESS Dog independently implements the same methods (which embedding
// conveniently does for you, method-promotion style). Think of it more
// like automatic delegation/mixins than classical inheritance.
// ============================================================================

import "fmt"

type Animal struct {
	Name string
	Age  int
}

func (a Animal) Describe() string {
	return fmt.Sprintf("%s is %d years old", a.Name, a.Age)
}

func (a Animal) Eat() {
	fmt.Println(a.Name, "is eating")
}

// Dog EMBEDS Animal: notice there's no field name, just the type `Animal`.
// This is the syntax that triggers embedding (as opposed to a normal named
// field like `Owner Animal`, which would NOT promote methods/fields).
type Dog struct {
	Animal        // embedded — Dog "has-a" Animal, and its fields/methods
	Breed  string // are PROMOTED to Dog's own level
}

// Dog can define its OWN method too, alongside the promoted ones.
func (d Dog) Bark() {
	fmt.Println(d.Name, "says Woof!") // d.Name works directly - promoted field
}

// A struct can even OVERRIDE a promoted method by redefining it with the
// same name — Dog's own Describe() takes priority over Animal's.
func (d Dog) Describe() string {
	return fmt.Sprintf("%s the %s (%d yrs)", d.Name, d.Breed, d.Age)
}

func main() {
	d := Dog{
		Animal: Animal{Name: "Rex", Age: 3},
		Breed:  "Labrador",
	}

	// Promoted fields — accessed as if Dog HAD Name/Age directly, even
	// though they actually live on the embedded Animal.
	fmt.Println("d.Name:", d.Name)
	fmt.Println("d.Age:", d.Age)
	fmt.Println("d.Breed:", d.Breed)

	// Promoted method — Eat() is defined on Animal, but callable directly
	// on Dog with no extra syntax.
	d.Eat()

	// Dog's own method
	d.Bark()

	// Dog's OVERRIDDEN Describe() wins over Animal's version.
	fmt.Println(d.Describe())

	// You can still reach the embedded struct explicitly by its type
	// name, e.g. to call the ORIGINAL Animal.Describe() despite the
	// override.
	fmt.Println("Original Animal.Describe():", d.Animal.Describe())

	// Embedding also works with interfaces (lesson 14) — a struct can
	// embed an interface to partially implement it and only override
	// specific methods, a common pattern in larger Go codebases. Shown
	// here conceptually via multiple struct embedding:
	type LoudDog struct {
		Dog
		Volume int
	}

	ld := LoudDog{
		Dog:    d,
		Volume: 11,
	}
	// Fields/methods promote through MULTIPLE levels of embedding.
	fmt.Println("ld.Name (promoted through Dog -> Animal):", ld.Name)
	ld.Bark()
	fmt.Println("ld.Volume:", ld.Volume)
}
