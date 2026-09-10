package main

import ("fmt", "math/rand")

//  main() is the entry point
// run a go file -> go looks for package main and func main

func main() {
	fmt.Println("Hello go", rand.Intn(10)) 
	fmt.Println(math.Pow(2, 3))
}
