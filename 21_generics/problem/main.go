package main

import "fmt"

func IndexInt(s []int, x int) int {
	for i, v := range s {
		if v == x {
			return i
		}
	}
	return -1
}

func IndexString(s []string, x string) int {
	for i, v := range s {
		if v == x {
			return i
		}
	}
	return -1
}

func main() {
	s := []int{10, 20, 30, 40, 50}
	fmt.Println(IndexInt(s, 50))

	t := []string{"Banana", "apple", "mango"}
	fmt.Println(IndexString(t, "Banana"))
}
