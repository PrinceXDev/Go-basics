package main

import "fmt"

func main() {
	var city string
	city = "London"

	var channel = "Sangam" // inferred to string

	// := (declare and assign with type inference)
	fmt.Println(city, channel)

	subscribers := 1000

	subscribers = subscribers + 100

	fmt.Println(subscribers)
}
