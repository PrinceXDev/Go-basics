package main

import "fmt"

/* I don't care what you are. If you have M(), you're acceptable. */
type I interface {
	M()
}

/* T has M(). */
type T struct {
	S string
}

func (t T) M() {
	fmt.Println(t.S)
}

/* Does T satisfy I? */
func main() {
	var i I = T{"Hello"}
	i.M()

	/* interface{}
	        ↓
	"Give me ANYTHING"
	*/

	// empty interface
	var a interface{}
	a = 42

	// a = "Hello"
	// a = false
	fmt.Println(a)

}

/*
      INTERFACE I
   ┌───────────────┐
   │   M() required│
   └───────┬───────┘
           │
           │ T has M()
           ▼
      STRUCT T
   ┌───────────────┐
   │ S = "Hello"   │
   │ M()           │
   └───────┬───────┘
           │
           │
   var i I = T{"Hello"}
           │
           ▼
         i.M()
           │
           ▼
        "Hello"

*/

/*

Animal interface

Requirements:
├── Speak()
└── Move()

Dog
├── Speak() ✅
└── Move()  ✅

Therefore:
Dog satisfies Animal
*/

/*

**********************************Most important distinction**********************************

Keep these two concepts separate in your head:

type Speaker interface {
    Speak()
}

means:

"You MUST have Speak()."

Whereas:

var x any

means:

"You can put ANY value here."

So:

=========================================================
interface with methods
        ↓
"I need something with these capabilities"

empty interface / any
        ↓
"I don't require any capabilities"

=========================================================
*/
