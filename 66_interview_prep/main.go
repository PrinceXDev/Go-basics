package main

// ============================================================================
// GO INTERVIEW GOTCHAS -- fourteen "what does this print?" questions, each one
// actually executed so you can see the answer rather than trust a blog post.
//
// These are the questions that separate "I have written Go" from "I understand
// Go's semantics". Read each block, PREDICT the output, then run it.
//
// Run: go run ./66_interview_prep
// The Q&A bank (architecture, concurrency, production) is in README.md.
// ============================================================================

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
)

func main() {
	q1LoopVariable()
	q2NilInterface()
	q3SliceAliasing()
	q4AppendCapacity()
	q5MapIterationOrder()
	q6DeferEvaluation()
	q7DeferNamedReturn()
	q8DeferInLoop()
	q9StringsAreBytes()
	q10ValueVsPointerReceiver()
	q11InterfaceSatisfaction()
	q12ChannelSemantics()
	q13WaitGroupBug()
	q14TimeAfterLeak()
}

// ===========================================================================
// Q1. Does this print 0,1,2 or 2,2,2?
// ===========================================================================

func q1LoopVariable() {
	fmt.Println("Q1. Loop variable capture")

	var wg sync.WaitGroup
	out := make([]int, 0, 3)
	var mu sync.Mutex

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			out = append(out, i) // captures i -- but WHICH i?
			mu.Unlock()
		}()
	}
	wg.Wait()
	slices.Sort(out)

	fmt.Printf("   got %v\n", out)
	fmt.Println(`   ANSWER: 0,1,2 on Go 1.22+ -- and 3,3,3 (or 2,2,2, or anything)
   on Go 1.21 and earlier.

   Go 1.22 changed the spec: the loop variable is now declared PER ITERATION
   instead of once per loop. Before that, all three goroutines closed over
   the SAME variable and read whatever it held when they happened to run.

   This is the single most common Go interview question, and the answer
   changed in 2024. Say both halves: you know the modern behaviour AND the
   old trap, and that the fix used to be i := i or passing i as an argument.
   The old behaviour still matters when you read pre-1.22 code.`)
}

// ===========================================================================
// Q2. This function returns a nil *MyError. Is the returned error == nil?
// ===========================================================================

type MyError struct{ msg string }

func (e *MyError) Error() string { return e.msg }

func mightFail(fail bool) *MyError { // <-- the bug is in this signature
	if fail {
		return &MyError{"boom"}
	}
	return nil
}

func q2NilInterface() {
	fmt.Println("\nQ2. The typed-nil interface trap")

	var err error = mightFail(false) // *MyError(nil) stored in an error

	fmt.Printf("   err == nil            -> %v\n", err == nil)
	fmt.Printf("   err's dynamic type    -> %v\n", reflect.TypeOf(err))
	fmt.Printf("   value inside is nil   -> %v\n", reflect.ValueOf(err).IsNil())

	fmt.Println(`   ANSWER: err != nil, even though the pointer inside it IS nil.

   An interface value is a PAIR: (type, value). It is nil only when BOTH
   halves are nil. Assigning a nil *MyError gives you (*MyError, nil) --
   a non-nil interface holding a nil pointer. Your "if err != nil" then
   fires on success, and calling a method on it may panic.

   THE FIX: never return a concrete error type. Declare the function as
   returning "error" and return a literal nil. This is why linters flag
   any function returning a concrete pointer error type.`)
}

// ===========================================================================
// Q3. What does modifying a sub-slice do to the original?
// ===========================================================================

func q3SliceAliasing() {
	fmt.Println("\nQ3. Slice aliasing")

	original := []int{1, 2, 3, 4, 5}
	sub := original[1:3] // len 2, cap 4 -- it SHARES the backing array

	fmt.Printf("   original=%v sub=%v (len %d, cap %d)\n", original, sub, len(sub), cap(sub))

	sub[0] = 99
	fmt.Printf("   after sub[0]=99 -> original=%v   <-- mutated!\n", original)

	sub = append(sub, 777) // cap allows it -> writes into original[3]
	fmt.Printf("   after append    -> original=%v   <-- mutated again!\n", original)

	// The fix: a FULL SLICE EXPRESSION a[low:high:max] caps the capacity, so
	// append is forced to allocate a fresh array instead of stomping.
	safe := original[1:3:3] // len 2, cap 2
	safe = append(safe, 555)
	fmt.Printf("   with s[1:3:3]   -> original=%v safe=%v   <-- untouched\n", original, safe)

	fmt.Println(`   ANSWER: a slice is a VIEW (pointer, len, cap) over an array.
   Slicing shares memory; append writes in place whenever capacity allows.

   This is the #1 source of "impossible" data corruption in Go. Any function
   that keeps or modifies a slice it was handed should either document that
   it takes ownership, or copy: dst := slices.Clone(src).`)
}

// ===========================================================================
// Q4. Does append always return a slice that shares memory with its input?
// ===========================================================================

func q4AppendCapacity() {
	fmt.Println("\nQ4. append and reallocation")

	a := make([]int, 3, 4) // len 3, cap 4
	b := append(a, 10)     // fits -> SAME array
	b[0] = 999
	fmt.Printf("   cap was 4: a=%v b=%v -> a[0] changed: %v\n", a, b, a[0] == 999)

	c := make([]int, 3, 3) // len 3, cap 3
	d := append(c, 10)     // does NOT fit -> new array, values copied
	d[0] = 999
	fmt.Printf("   cap was 3: c=%v d=%v -> c[0] changed: %v\n", c, d, c[0] == 999)

	fmt.Println(`   ANSWER: it depends on capacity, which is exactly why this is
   dangerous. Same backing array when it fits, a fresh copy when it does not.
   So "does modifying the result affect the input?" is "sometimes" -- the
   worst possible answer for a program's correctness.

   Always use the returned slice (s = append(s, x)) and never assume the
   input is or is not aliased. Pre-allocate with make([]T, 0, n) when you
   know n: it avoids the log(n) reallocation-and-copy cycles entirely.`)
}

// ===========================================================================
// Q5. Is map iteration order stable?
// ===========================================================================

func q5MapIterationOrder() {
	fmt.Println("\nQ5. Map iteration order")

	m := map[int]int{}
	for i := 0; i < 12; i++ {
		m[i] = i
	}

	for i := 0; i < 3; i++ {
		keys := make([]int, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		fmt.Printf("   loop %d: %v\n", i+1, keys)
	}

	sorted := slices.Sorted(maps.Keys(m)) // Go 1.23+: iterator -> sorted slice
	fmt.Printf("   deterministic: %v\n", sorted)
	fmt.Println("   (with a SMALL map you often see the same cyclic order from a")
	fmt.Println("    different starting point -- still random, just less obviously")
	fmt.Println("    so. Never rely on either shape.)")

	fmt.Println(`   ANSWER: deliberately RANDOMISED, on every run and every loop.

   The runtime starts each range at a random bucket ON PURPOSE, so that code
   cannot accidentally depend on an order the implementation never promised.
   (Go's team learned this from languages where iteration order was an
   accident that became a de-facto API.)

   Need order? Collect the keys and sort them. Note the one exception:
   fmt printing of a map IS sorted by key, which lulls people into thinking
   maps are ordered. They are not.`)
}

// ===========================================================================
// Q6. When are deferred arguments evaluated?
// ===========================================================================

func q6DeferEvaluation() {
	fmt.Println("\nQ6. defer argument evaluation")

	x := 1
	defer fmt.Printf("   deferred with argument: x=%d  <-- captured at DEFER time\n", x)
	defer func() {
		fmt.Printf("   deferred in a closure:  x=%d  <-- read at RUN time\n", x)
	}()
	x = 42
	fmt.Println("   x is now 42; two defers are pending (LIFO)")

	// Output order: closure first (LIFO), then the argument form.
	// ANSWER: arguments to a deferred call are evaluated IMMEDIATELY, at the
	// point of the defer statement. The CALL happens later. A closure has no
	// arguments, so it reads the variable when it finally runs.
	//
	// This is why `defer file.Close()` is fine but
	// `defer fmt.Println(time.Since(start))` always prints ~0.
}

// ===========================================================================
// Q7. Can a defer change an already-returned value?
// ===========================================================================

func namedReturn() (result int) {
	defer func() { result *= 2 }() // modifies the NAMED return variable
	return 5
}

func anonReturn() int {
	result := 5
	defer func() { result *= 2 }() // modifies a local; return value is copied
	return result
}

func recoverToError() (err error) {
	defer func() {
		if r := recover(); r != nil {
			// The ONLY way to turn a panic into an error: recover in a defer
			// and assign to a NAMED return value.
			err = fmt.Errorf("recovered: %v", r)
		}
	}()
	panic("kaboom")
}

func q7DeferNamedReturn() {
	fmt.Println("\nQ7. defer and named return values")
	fmt.Printf("   named return  -> %d\n", namedReturn())
	fmt.Printf("   anon  return  -> %d\n", anonReturn())
	fmt.Printf("   panic -> error -> %v\n", recoverToError())

	fmt.Println(`   ANSWER: 10, 5, and a real error.

   "return 5" is two steps: assign 5 to the return slot, THEN run defers,
   THEN actually return. With a NAMED result the defer can still see and
   modify that slot. With an unnamed result the value was already copied out.

   This mechanism is what makes panic-to-error conversion possible, and it
   is the standard way to write a recovering middleware.`)
}

// ===========================================================================
// Q8. What is wrong with defer inside a loop?
// ===========================================================================

func q8DeferInLoop() {
	fmt.Println("\nQ8. defer inside a loop")

	closed := 0
	openResource := func() func() { return func() { closed++ } }

	// BROKEN: every defer stacks up and runs only when the FUNCTION returns.
	// Loop over 10,000 files and you hold 10,000 handles at once.
	func() {
		for i := 0; i < 3; i++ {
			cleanup := openResource()
			defer cleanup() // all 3 run at the very end
		}
		fmt.Printf("   broken: after the loop, closed=%d (still holding them)\n", closed)
	}()
	fmt.Printf("   broken: after the function returns, closed=%d\n", closed)

	// FIXED: give each iteration its own function scope.
	closed = 0
	for i := 0; i < 3; i++ {
		func() {
			cleanup := openResource()
			defer cleanup() // runs at the end of THIS iteration
		}()
	}
	fmt.Printf("   fixed:  closed=%d after each iteration\n", closed)

	fmt.Println(`   ANSWER: defer is FUNCTION-scoped, not block-scoped. In a loop
   it accumulates. Wrap the body in an immediately-invoked func, or extract
   a helper function -- which is usually cleaner anyway.`)
}

// ===========================================================================
// Q9. What is len("héllo")? What does range give you?
// ===========================================================================

func q9StringsAreBytes() {
	fmt.Println("\nQ9. Strings, bytes and runes")

	s := "héllo"
	fmt.Printf("   len(%q)              = %d   <-- BYTES, not characters\n", s, len(s))
	fmt.Printf("   len([]rune(s))       = %d   <-- code points\n", len([]rune(s)))
	fmt.Printf("   s[1]                 = %d  (a byte, half of 'é')\n", s[1])

	fmt.Print("   range gives (byteIndex, rune): ")
	for i, r := range s {
		fmt.Printf("(%d,%c) ", i, r)
	}
	fmt.Println("   <-- indexes SKIP")

	fmt.Println(`   ANSWER: len is bytes; range decodes UTF-8 and yields runes with
   their BYTE offsets. Indexing s[i] gives a byte and will happily slice a
   multi-byte character in half.

   Practical rules: range for iterating, []rune only when you genuinely need
   character positions (it allocates), and never s[:n] to truncate
   user-supplied text -- you will produce invalid UTF-8. Note also that a
   "character" a user sees may still be several runes (é can be e + U+0301),
   which is why golang.org/x/text exists.`)
}

// ===========================================================================
// Q10. Value receiver or pointer receiver?
// ===========================================================================

type Counter struct{ n int }

func (c Counter) IncValue()    { c.n++ } // operates on a COPY
func (c *Counter) IncPointer() { c.n++ } // operates on the original

func q10ValueVsPointerReceiver() {
	fmt.Println("\nQ10. Value vs pointer receivers")

	c := Counter{}
	c.IncValue()
	c.IncValue()
	fmt.Printf("   after 2x IncValue():   n=%d\n", c.n)

	c.IncPointer()
	c.IncPointer()
	fmt.Printf("   after 2x IncPointer(): n=%d\n", c.n)

	fmt.Println(`   ANSWER: 0 then 2. A value receiver gets a COPY of the struct;
   mutations die with it. Go auto-takes the address for c.IncPointer(), which
   is why both look identical at the call site -- a real trap for readers.

   THE RULES: use a pointer receiver if the method mutates, if the struct is
   large, or if it contains a sync.Mutex (copying a lock is a bug that
   go vet catches). And be CONSISTENT: mixing both on one type is the
   classic code-review finding, because it makes the method set ambiguous
   for interface satisfaction -- see Q11.`)
}

// ===========================================================================
// Q11. Which types satisfy this interface?
// ===========================================================================

type Speaker interface{ Speak() string }

type Dog struct{}

func (d Dog) Speak() string { return "woof" } // VALUE receiver

type Cat struct{}

func (c *Cat) Speak() string { return "meow" } // POINTER receiver

func q11InterfaceSatisfaction() {
	fmt.Println("\nQ11. Method sets and interface satisfaction")

	var s Speaker

	s = Dog{}  // ok: value receiver -> both Dog and *Dog satisfy Speaker
	s = &Dog{} // ok
	s = &Cat{} // ok: pointer receiver -> only *Cat satisfies Speaker
	// s = Cat{}   // COMPILE ERROR: Cat does not implement Speaker
	_ = s

	fmt.Printf("   Dog{}  implements Speaker: %v\n", implements[Dog]())
	fmt.Printf("   &Dog{} implements Speaker: %v\n", implements[*Dog]())
	fmt.Printf("   Cat{}  implements Speaker: %v   <-- compile error if you try\n", implements[Cat]())
	fmt.Printf("   &Cat{} implements Speaker: %v\n", implements[*Cat]())

	fmt.Println(`   ANSWER: a VALUE-receiver method is in the method set of both T
   and *T. A POINTER-receiver method is in the method set of *T ONLY.

   WHY: Go can always take the address of an addressable value, but an
   interface holds a copy -- there is no address to take, so it cannot
   promote T to *T. Hence "cannot use Cat literal as Speaker".

   The practical version: if ANY method needs a pointer receiver, give the
   type pointer receivers everywhere and always pass &T.`)
}

func implements[T any]() bool {
	return reflect.TypeFor[T]().Implements(reflect.TypeFor[Speaker]())
}

// ===========================================================================
// Q12. What happens when you read from / close / nil a channel?
// ===========================================================================

func q12ChannelSemantics() {
	fmt.Println("\nQ12. Channel semantics")

	ch := make(chan int, 2)
	ch <- 1
	close(ch)

	v, ok := <-ch
	fmt.Printf("   buffered value after close: v=%d ok=%v\n", v, ok)
	v, ok = <-ch
	fmt.Printf("   drained, then read again:   v=%d ok=%v   <-- zero value, not a block\n", v, ok)

	// A nil channel blocks forever -- which is USEFUL: setting a case's
	// channel to nil disables that case in a select.
	var nilCh chan int
	select {
	case <-nilCh:
		fmt.Println("   unreachable")
	default:
		fmt.Println("   receive on a nil channel: blocks forever (select took default)")
	}

	fmt.Println(`   ANSWER:
     send on closed   -> PANIC
     close of closed  -> PANIC
     close of nil     -> PANIC
     receive on closed-> drains buffered values, then zero value + ok=false
     send/recv on nil -> blocks FOREVER (deadlock if nothing else runs)

   THE OWNERSHIP RULE that prevents all three panics: the goroutine that
   SENDS is the one that CLOSES, and there is exactly one of it. A receiver
   must never close. Multiple senders? Use a sync.WaitGroup and have a
   separate goroutine close after Wait().

   And: closing is a BROADCAST. It is how you signal N goroutines at once,
   which is exactly how context cancellation works under the hood.`)
}

// ===========================================================================
// Q13. Spot the bug.
// ===========================================================================

func q13WaitGroupBug() {
	fmt.Println("\nQ13. The WaitGroup race")

	// BROKEN (do not copy this):
	//   for i := 0; i < 3; i++ {
	//       go func() {
	//           wg.Add(1)          // <-- Add INSIDE the goroutine
	//           defer wg.Done()
	//           work()
	//       }()
	//   }
	//   wg.Wait()                  // may run before any Add -> returns instantly
	//
	// CORRECT: Add before launching.
	var wg sync.WaitGroup
	done := make([]bool, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1) // <-- outside, before the goroutine starts
		go func() {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond)
			done[i] = true // distinct index per goroutine -> no race
		}()
	}
	wg.Wait()
	fmt.Printf("   all finished: %v\n", done)

	fmt.Println(`   ANSWER: wg.Add must happen BEFORE the goroutine is launched,
   in the parent. Inside the goroutine it races with Wait(), which can see a
   counter of 0 and return before any work starts -- so your program exits
   mid-flight, usually only on a loaded CI machine.

   Related: never copy a WaitGroup (pass *sync.WaitGroup), and Go 1.25+
   offers wg.Go(f) which does the Add/Done pairing for you.`)
}

// ===========================================================================
// Q14. Why is this select a memory leak?
// ===========================================================================

func q14TimeAfterLeak() {
	fmt.Println("\nQ14. time.After in a loop")

	fmt.Println(`   BROKEN:
       for {
           select {
           case msg := <-ch:        handle(msg)
           case <-time.After(1*time.Minute):   return
           }
       }

   Every loop iteration creates a NEW timer, and each one stays alive until
   it fires -- one minute later -- holding its channel and memory. A busy
   loop allocates thousands of pending timers. (Go 1.23 made unreferenced
   timers collectable, which softens this, but the idle-timeout semantics
   are still wrong: the timer restarts on every message, so it can never
   measure "idle for a minute" correctly across iterations.)

   CORRECT:
       timer := time.NewTimer(1 * time.Minute)
       defer timer.Stop()
       for {
           select {
           case msg := <-ch:
               handle(msg)
               if !timer.Stop() { <-timer.C }   // drain before reuse (pre-1.23)
               timer.Reset(1 * time.Minute)
           case <-timer.C:
               return
           }
       }

   Same family of bug: time.Tick() (never collectable -- use NewTicker +
   defer Stop), and context.WithTimeout without calling its cancel func.`)

	// A quick proof that a stopped timer is the cheap shape:
	timer := time.NewTimer(time.Hour)
	stopped := timer.Stop()
	fmt.Printf("   timer.Stop() returned %v -> resources released immediately\n", stopped)

	fmt.Print(`
===========================================================================
HOW TO USE THIS FILE

Cover the ANSWER blocks, read the code, write down what you think it prints,
then run it. Anything you got wrong, open the lesson it belongs to:

  Q1  loop vars ......... 22_goroutines, 30_closure
  Q2  typed nil ......... 14_interfaces, 35_errors_advanced
  Q3  Q4 slices ......... 09_arrays_and_slices
  Q5  maps .............. 10_maps
  Q6  Q7 Q8 defer ....... 08_error_handling, 12_methods
  Q9  strings ........... 34_sorting_and_stdlib
  Q10 Q11 receivers ..... 12_methods, 13_pointers, 14_interfaces
  Q12 channels .......... 23_channels, 24_select_and_sync
  Q13 WaitGroup ......... 22_goroutines, 58_testing_concurrent_code
  Q14 timers ............ 33_time_and_formatting, 25_context

The written Q&A bank -- architecture, concurrency, databases, production,
microservices -- is in 66_interview_prep/README.md.
===========================================================================
`)
}

var _ = errors.Is // keep the import honest if you trim examples
var _ = strings.TrimSpace
