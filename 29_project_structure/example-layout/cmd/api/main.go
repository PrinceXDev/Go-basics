// cmd/api/main.go — the executable entry point. Deliberately thin: it just
// wires the internal store and pkg validator together and exposes them
// over HTTP. All the real logic lives in internal/store and pkg/validator.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"example.com/todo-api/internal/store"
	"example.com/todo-api/pkg/validator"
)

func main() {
	s := store.New()
	s.Add("Learn Go project layout")
	s.Add("Wire cmd + internal + pkg together")

	mux := http.NewServeMux()

	mux.HandleFunc("/todos", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s.All())

		case http.MethodPost:
			var body struct {
				Task string `json:"task"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			// Using the pkg/validator helper — this is the point of the
			// whole layout: main.go composes internal + pkg, and neither
			// of those packages needs to know about http at all.
			if !validator.NotEmpty(body.Task) {
				http.Error(w, "task must not be empty", http.StatusBadRequest)
				return
			}
			if !validator.MaxLength(body.Task, 200) {
				http.Error(w, "task too long", http.StatusBadRequest)
				return
			}
			todo := s.Add(body.Task)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(todo)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	fmt.Println("todo-api running on http://localhost:8082")
	http.ListenAndServe(":8082", mux)
}
