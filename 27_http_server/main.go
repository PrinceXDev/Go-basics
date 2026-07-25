package main

// ============================================================================
// CONCEPT: A basic HTTP server using `net/http` — no framework needed.
//
// WHY THIS MATTERS
// Node needs Express (or similar) for anything beyond the most trivial
// server. Go's standard library `net/http` is already a fully capable,
// production-grade HTTP server and router — frameworks like Gin or Echo
// exist for convenience, not because raw Go can't do the job.
//
// CORE CONCEPT: a HANDLER is just a function with the signature
//   func(w http.ResponseWriter, r *http.Request)
// `w` is how you WRITE the response; `r` holds everything about the
// incoming request (method, path, headers, body, query params).
//
// JS/TS comparison:
//   Express: app.get("/hello", (req, res) => res.send("hi"))
//   Go:      mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {...})
// ============================================================================

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

type Todo struct {
	ID   int    `json:"id"`
	Task string `json:"task"`
	Done bool   `json:"done"`
}

// in-memory "database" for this demo — a real app would use an actual DB.
var todos = []Todo{
	{ID: 1, Task: "Learn Go basics", Done: true},
	{ID: 2, Task: "Learn goroutines", Done: true},
	{ID: 3, Task: "Build an HTTP server", Done: false},
}

func helloHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Hello from Go's built-in HTTP server!")
}

// Handlers can inspect the request method to behave differently per verb,
// mirroring how Express route handlers differ per app.get/app.post/etc.
func todosHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Encode the slice directly to the ResponseWriter as JSON —
		// ResponseWriter satisfies io.Writer, and json.NewEncoder can
		// write straight to any io.Writer (no intermediate []byte needed,
		// unlike json.Marshal from lesson 16).
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(todos)

	case http.MethodPost:
		var newTodo Todo
		// json.NewDecoder reads and unmarshals directly from the request
		// body (an io.Reader), the mirror image of NewEncoder above.
		if err := json.NewDecoder(r.Body).Decode(&newTodo); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		newTodo.ID = len(todos) + 1
		todos = append(todos, newTodo)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated) // 201
		json.NewEncoder(w).Encode(newTodo)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Reading a URL query parameter, e.g. /greet?name=Prince
func greetHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "stranger"
	}
	fmt.Fprintf(w, "Hello, %s!\n", name)
}

func main() {
	// ServeMux is Go's built-in router: maps URL paths to handlers.
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", helloHandler)
	mux.HandleFunc("/todos", todosHandler)
	mux.HandleFunc("/greet", greetHandler)

	fmt.Println("Server starting on http://localhost:8080")
	// ListenAndServe blocks forever, serving requests, until the process
	// is killed or an error occurs (e.g. port already in use) — that
	// error is worth checking, hence log.Fatal here.
	log.Fatal(http.ListenAndServe(":8080", mux))
}
