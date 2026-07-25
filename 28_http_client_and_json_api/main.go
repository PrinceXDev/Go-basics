package main

// ============================================================================
// CONCEPT: Acting as an HTTP CLIENT — calling a JSON API and decoding the
// response, using `net/http` + `encoding/json` together.
//
// JS/TS comparison: this replaces `fetch()` / `axios`.
//   JS:  const res = await fetch(url); const data = await res.json();
//   Go:  resp, err := http.Get(url); json.NewDecoder(resp.Body).Decode(&data)
// The shape is similar, but Go makes every failure point explicit via
// (result, error) returns instead of a single awaited promise that might
// reject.
//
// To keep this lesson self-contained (no real internet dependency needed),
// this file starts its OWN tiny local server in a goroutine (lesson 22),
// then immediately acts as a client calling it — demonstrating both roles
// in one runnable program.
// ============================================================================

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Weather struct {
	City        string  `json:"city"`
	TempCelsius float64 `json:"temp_celsius"`
}

func startLocalServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/weather", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Weather{City: "Berlin", TempCelsius: 18.5})
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		// Echoes back whatever JSON body was POSTed, to demonstrate a
		// client SENDING JSON, not just receiving it.
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		json.NewEncoder(w).Encode(body)
	})
	go http.ListenAndServe(":8081", mux)
}

func main() {
	startLocalServer()
	time.Sleep(100 * time.Millisecond) // give the goroutine a moment to start listening

	// ---------- Simple GET request ----------
	resp, err := http.Get("http://localhost:8081/weather")
	if err != nil {
		fmt.Println("request error:", err)
		return
	}
	// A response body is a stream (io.ReadCloser) — it MUST be closed
	// once you're done reading it, or the connection is never released
	// back to the pool. `defer` is the idiomatic place for this.
	defer resp.Body.Close()

	fmt.Println("Status code:", resp.StatusCode)

	var weather Weather
	if err := json.NewDecoder(resp.Body).Decode(&weather); err != nil {
		fmt.Println("decode error:", err)
		return
	}
	fmt.Printf("Weather in %s: %.1f°C\n", weather.City, weather.TempCelsius)

	// ---------- POST request with a JSON body ----------
	payload := map[string]any{"message": "hello server", "count": 3}
	jsonBytes, _ := json.Marshal(payload) // lesson 16's Marshal, reused here

	// http.Post needs an io.Reader for the body — bytes.NewReader wraps
	// our []byte to satisfy that interface.
	postResp, err := http.Post(
		"http://localhost:8081/echo",
		"application/json",
		bytes.NewReader(jsonBytes),
	)
	if err != nil {
		fmt.Println("post error:", err)
		return
	}
	defer postResp.Body.Close()

	var echoed map[string]any
	json.NewDecoder(postResp.Body).Decode(&echoed)
	fmt.Println("Echoed back:", echoed)

	// ---------- Using a custom http.Client with a timeout ----------
	// http.Get/Post use a shared DEFAULT client with no timeout — for
	// real applications you almost always want your own client with an
	// explicit timeout, so a hung server can't block your program forever.
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "http://localhost:8081/weather", nil)
	if err != nil {
		fmt.Println("request build error:", err)
		return
	}
	req.Header.Set("X-Custom-Header", "go-learning")

	customResp, err := client.Do(req)
	if err != nil {
		fmt.Println("custom client error:", err)
		return
	}
	defer customResp.Body.Close()
	fmt.Println("Custom client status:", customResp.StatusCode)
}
