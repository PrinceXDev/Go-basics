package main

import (
	"fmt"
	"math"
	"strings"
)

// function scope
func greet() {
	message := "Hii there"
	fmt.Println(message)
}

// package scope
var appName = "Demo App"

func showAppName() {
	fmt.Println(appName)
}

func main() {
	x := 10 // scoped to main()

	if x > 5 {
		y := "hello"      // scoped to this if-block only
		fmt.Println(x, y) // works fine here
	}

	// fmt.Println(y) // ERROR: y is undefined here

	greet()

	fmt.Println(appName)

	showAppName()

	fmt.Println(math.Sqrt(25))

	// Example of string pkg
	firstName := "prince"
	fmt.Println(strings.ToUpper(firstName))

	views1 := 1000
	views2 := 2000

	totalViews := views1 + views2

	fmt.Println(totalViews)

	likes := 100000
	likes++
	likes++

	avgViews := totalViews / 2
	fmt.Println(totalViews, likes, avgViews)

	rating := 4.1
	fmt.Println(rating)
}

/* // Data Types

var age int = 30           // whole numbers
var price float64 = 9.99   // decimals
var name string = "Gopher" // text
var isValid bool = true    // true/false
var letter byte = 'A'      // alias for uint8, holds a character

// Type inference

person := "Alice"      // inferred as string
price := 19.99       // inferred as float64
count := 10          // inferred as int
isActive := true     // inferred as bool

// Zero values

var age int       // 0
var price float64 // 0.0
var name string   // "" (empty string)
var isActive bool // false */
