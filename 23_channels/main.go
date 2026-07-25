package main

// ============================================================================
// CONCEPT: Channels — typed pipes for goroutines to send/receive values.
//
// WHY THIS MATTERS
// WaitGroups (lesson 22) only tell you WHEN goroutines are done — they
// don't let goroutines exchange DATA safely. Channels solve that. Go's own
// philosophy, often quoted, is:
//   "Do not communicate by sharing memory; instead, share memory by
//    communicating."
// Instead of multiple goroutines fighting over the same variable (needing
// locks), you pass values THROUGH a channel, one direction at a time.
//
// JS/TS comparison: there's no direct equivalent in JS. The closest
// mental model is an async queue — think of a channel as a strictly
// typed, blocking Promise-based queue that goroutines push to and pull
// from.
// ============================================================================

import (
	"fmt"
	"time"
)

func main() {
	// ---------- BASIC UNBUFFERED CHANNEL ----------
	// make(chan T) creates a channel carrying values of type T.
	// UNBUFFERED means: a send blocks until another goroutine is ready to
	// receive, and vice versa — the two sides synchronize/rendezvous.
	messages := make(chan string)

	go func() {
		messages <- "hello from goroutine" // send a value into the channel
	}()

	msg := <-messages // receive a value from the channel (blocks until one arrives)
	fmt.Println("Received:", msg)

	// ---------- USING A CHANNEL TO RETURN WORK RESULTS ----------
	// A very common pattern: goroutine computes something, sends the
	// result back over a channel instead of returning it directly (since
	// a goroutine cannot "return" a value the normal way — its caller has
	// already moved on).
	results := make(chan int)
	go func() {
		sum := 0
		for i := 1; i <= 100; i++ {
			sum += i
		}
		results <- sum
	}()
	fmt.Println("Sum of 1..100:", <-results)

	// ---------- BUFFERED CHANNELS ----------
	// make(chan T, n) creates a BUFFERED channel with capacity n. Sends
	// only block once the buffer is full — up to n values can be sent
	// without a receiver being ready yet. Useful when producer and
	// consumer don't need to run in lockstep.
	buffered := make(chan int, 3)
	buffered <- 1
	buffered <- 2
	buffered <- 3
	// buffered <- 4 // would BLOCK here — buffer is full, no receiver yet
	close(buffered) // signals "no more values will be sent"

	// for-range over a channel receives values until it's closed —
	// closing is how a sender tells receivers "I'm done".
	fmt.Println("-- draining buffered channel --")
	for v := range buffered {
		fmt.Println("buffered value:", v)
	}

	// ---------- FAN-OUT: multiple goroutines feeding one channel ----------
	jobs := make(chan int, 5)
	done := make(chan bool)

	// One goroutine processes whatever comes through `jobs`.
	go func() {
		for job := range jobs {
			fmt.Println("processing job:", job)
			time.Sleep(10 * time.Millisecond)
		}
		done <- true // signal completion
	}()

	for i := 1; i <= 5; i++ {
		jobs <- i
	}
	close(jobs) // no more jobs coming — lets the range loop above finish
	<-done       // wait for the worker goroutine to signal it's done

	fmt.Println("All jobs processed")

	// ---------- READING FROM A CLOSED CHANNEL ----------
	// Receiving from a closed channel never blocks — it immediately
	// returns the zero value. The comma-ok form (same idiom as maps in
	// lesson 10, type assertions in lesson 14) tells you whether the
	// value is real or the channel is simply closed/empty.
	ch := make(chan int, 1)
	ch <- 42
	close(ch)
	v1, ok1 := <-ch
	fmt.Println("first receive:", v1, "ok:", ok1) // 42, true
	v2, ok2 := <-ch
	fmt.Println("second receive:", v2, "ok:", ok2) // 0, false (channel empty+closed)
}
