package sample

import "fmt"

func main() {
	var i interface{} = "hello"

	// where the variable i is of type string
	s := i.(string)
	fmt.Println(s)

	s, ok := i.(string)
	fmt.Println(s, ok)

	s2, ok2 := i.(float64)
	fmt.Println(s2, ok2)

	f, ok3 := i.(float64) // no panic, ok3 will be false
	fmt.Println(f, ok3)
}
