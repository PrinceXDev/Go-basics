package main

import "fmt"

/* T can be any type that satisfies Go's comparable constraint.

[T comparable]
 │      │
 │      └── constraint
 │
 └── type parameter

- T represents some type that will be decided later.

** any vs comparable
=======================

any (T can be basically any Go type.)
func Print[T any](value T)

example:-
Print(10)
Print("Hello")
Print(true)
Print([]int{1, 2, 3})

But you can't necessarily do operations on T.

For example:

func Add[T any](a T, b T) T {
    return a + b // ❌
}

Go doesn't know whether T supports +.

*/

func Index[T comparable](s []T, x T) int {
	for i, v := range s {
		if v == x {
			return i
		}
	}
	return -1
}

func main() {
	s := []int{10, 20, 30, 40, 50}
	fmt.Println(Index(s, 50))

	t := []string{"Banana", "apple", "mango"}
	fmt.Println(Index(t, "Banana"))
}
