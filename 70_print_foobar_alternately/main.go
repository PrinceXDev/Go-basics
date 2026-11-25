// LeetCode 1115 - Print FooBar Alternately
// https://leetcode.com/problems/print-foobar-alternately/
//
// Two goroutines share one FooBar. One calls Foo, the other calls Bar.
// Output must be "foobar" repeated n times, whatever order the scheduler picks.
package main

import (
	"fmt"
	"os"
	"strconv"
	"sync"
)

// FooBar passes a "turn token" back and forth over two channels.
// Only the goroutine holding the token may print, so they must alternate.
type FooBar struct {
	n       int
	fooTurn chan struct{}
	barTurn chan struct{}
}

func NewFooBar(n int) *FooBar {
	fb := &FooBar{
		n:       n,
		fooTurn: make(chan struct{}, 1),
		barTurn: make(chan struct{}, 1),
	}
	// foo goes first. Buffer of 1 lets the final hand-off to foo succeed
	// even though foo has already returned and nobody will ever receive it.
	fb.fooTurn <- struct{}{}
	return fb
}

func (fb *FooBar) Foo(printFoo func()) {
	for i := 0; i < fb.n; i++ {
		<-fb.fooTurn
		printFoo()
		fb.barTurn <- struct{}{}
	}
}

func (fb *FooBar) Bar(printBar func()) {
	for i := 0; i < fb.n; i++ {
		<-fb.barTurn
		printBar()
		fb.fooTurn <- struct{}{}
	}
}

func main() {
	n := 5
	if len(os.Args) > 1 {
		if v, err := strconv.Atoi(os.Args[1]); err == nil && v > 0 {
			n = v
		}
	}

	fb := NewFooBar(n)
	var wg sync.WaitGroup
	wg.Add(2)

	// Start Bar first on purpose: it must still wait for Foo.
	go func() { defer wg.Done(); fb.Bar(func() { fmt.Print("bar") }) }()
	go func() { defer wg.Done(); fb.Foo(func() { fmt.Print("foo") }) }()

	wg.Wait()
	fmt.Println()
}
