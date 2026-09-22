package main

// Go imports are NOT comma-separated. A parenthesised import block puts one
// path per line; `import ("fmt", "math/rand")` is a syntax error.
import (
	"fmt"
	"math"
	"math/rand"
)

//  main() is the entry point
// run a go file -> go looks for package main and func main

func main() {
	fmt.Println("Hello go", rand.Intn(10))
	fmt.Println(math.Pow(2, 3))
}
