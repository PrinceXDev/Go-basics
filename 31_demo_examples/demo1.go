package main

import (
	"fmt"
	"sync"
	"time"
)

/*
	Add  → How many workers?
	Done → One worker finished
	Wait → Wait for everyone

The order of workers is unpredictable because goroutines execute concurrently.

But:

All workers completed will happen only after all three workers call Done().

==================
Mental model
==================
             WaitGroup
          ┌─────────────┐
          │ Counter = 3 │
          └──────┬──────┘
                 │
       ┌─────────┼─────────┐
       ↓         ↓         ↓
    Worker 1  Worker 2  Worker 3
       │         │         │
      Done      Done      Done
       │         │         │
       ↓         ↓         ↓
       2         1         0
                           │
                           ↓
                      WAIT RELEASED
						   ↓
                      All completed

===> WaitGroup = a finish counter.
*/

// Why pass *sync.WaitGroup? => Because all goroutines need to work with the same WaitGroup.
/*  SAME WaitGroup
             │
   ┌─────────┼─────────┐
   ↓         ↓         ↓
Worker 1  Worker 2  Worker 3
   │         │         │
  Done      Done      Done
*/
func worker(id int, wg *sync.WaitGroup) {
	defer wg.Done() // Why? => Because we want Done() to happen no matter how the function exits normally.

	// Doing work
	fmt.Println("Worker", id, "started")

	/* if something {
	    return
	} */

	// if fn early returns, wg.Done() still executes.
	// wg.Done() prevents the Wait() from potentially waiting forever.

	time.Sleep(time.Second)

	// More work
	fmt.Println("Worker", id, "finished")
}

func main() {
	var wg sync.WaitGroup

	wg.Add(3)

	go worker(1, &wg)
	go worker(2, &wg)
	go worker(3, &wg)

	wg.Wait()

	fmt.Println("All workers completed")
}

/* A very common pattern

var wg sync.WaitGroup

for i := 0; i < 5; i++ {
    wg.Add(1)

    go func(id int) {
        defer wg.Done()

        fmt.Println("Worker", id)
    }(i)
}

wg.Wait()
===
Loop
 │
 ├── Add(1) → Worker 0
 ├── Add(1) → Worker 1
 ├── Add(1) → Worker 2
 ├── Add(1) → Worker 3
 └── Add(1) → Worker 4

                 ↓

             Wait()

                 ↓

       Wait for all 5 workers

**** Working Pattern ****
Main                 Worker

Add(1)
   |
go worker() ───────→ Done()
   |
Wait()
*************************

*/
