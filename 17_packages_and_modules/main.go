package main

// ============================================================================
// CONCEPT: Packages and modules — organizing code across multiple files
// and folders, and a deeper look at go.mod.
//
// RECAP FROM LESSON 01: a folder = a package. Every .go file in a folder
// must declare the SAME package name at the top.
//
// WHAT'S NEW HERE: importing a package that lives INSIDE YOUR OWN MODULE,
// not the standard library. Look at the import path below:
//   "example.com/go-basics/17_packages_and_modules/mathutils"
// This is built from:
//   1. the module name declared in go.mod ("example.com/go-basics")
//   2. the folder path from the module root to the package
//      ("17_packages_and_modules/mathutils")
// Go resolves this ENTIRELY from folder structure — there's no separate
// "package registry" step for your own code, unlike JS where even local
// files need relative import paths (`./mathutils`).
//
// MODULE vs PACKAGE — the terms are easy to conflate:
//   - A MODULE is the whole project / repository: one go.mod file, one
//     module name, potentially MANY packages inside it.
//   - A PACKAGE is a single folder of related .go files within a module.
// JS/TS comparison: a Go module is roughly like an npm package (the unit
// you'd `go get` or publish), while a Go package is more like one JS
// file/folder you'd `import` from within that same project.
// ============================================================================

import (
	"fmt"

	"example.com/go-basics/17_packages_and_modules/mathutils"
)

func main() {
	// Using the imported package: prefix every exported name with the
	// package name, same convention as fmt.Println, strings.ToUpper, etc.
	// from lesson 02 — those are ALSO just packages, from the standard
	// library instead of your own module.
	fmt.Println("Add:", mathutils.Add(3, 4))
	fmt.Println("Multiply:", mathutils.Multiply(3, 4))
	fmt.Println("Square:", mathutils.Square(5))

	// mathutils.square(5) // COMPILE ERROR: square is unexported
	// (lowercase) — invisible outside the mathutils package, even though
	// you imported it. This is the SAME visibility rule as lesson 01,
	// just now enforced across a package boundary instead of within one
	// file.
}
