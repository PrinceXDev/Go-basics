// tiny-crud: the smallest possible CRUD API with a real database and one
// middleware, meant to be read top to bottom in a few minutes.
//
// Run it with:
//
//	go run ./tiny-crud
//
// Then try:
//
//	curl -X POST localhost:8080/users -d '{"name":"Ada","email":"ada@example.com"}'
//	curl localhost:8080/users
//	curl localhost:8080/users/1
//	curl -X PUT localhost:8080/users/1 -d '{"name":"Ada Lovelace","email":"ada@example.com"}'
//	curl -X DELETE localhost:8080/users/1
package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

// ---------- model ----------

type User struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ---------- database ----------

// A single global *sql.DB is fine for a tiny demo like this one; it's a
// connection pool, not a single connection, so handlers can share it safely.
var db *sql.DB

// dbFilePath pins the SQLite file next to this source file, so the database
// always lives inside tiny-crud/ no matter which directory `go run` is
// invoked from.
func dbFilePath() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "tiny-crud.db")
}

func initDB() {
	var err error
	db, err = sql.Open("sqlite", dbFilePath())
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id    INTEGER PRIMARY KEY AUTOINCREMENT,
			name  TEXT NOT NULL,
			email TEXT NOT NULL
		)`)
	if err != nil {
		log.Fatal(err)
	}
}

// ---------- middleware ----------

// loggingMiddleware wraps a handler and logs every request's method, path
// and how long it took. This is the shape almost all Go middleware takes:
// a func that takes a handler and returns a new handler wrapping it.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start))
	})
}

// ---------- handlers ----------

func getUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`SELECT id, name, email FROM users`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Email); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		users = append(users, u)
	}

	writeJSON(w, http.StatusOK, users)
}

func getUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var u User
	err = db.QueryRow(`SELECT id, name, email FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Name, &u.Email)
	if err == sql.ErrNoRows {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, u)
}

func createUser(w http.ResponseWriter, r *http.Request) {
	var u User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if u.Name == "" || u.Email == "" {
		http.Error(w, "name and email are required", http.StatusBadRequest)
		return
	}

	result, err := db.Exec(`INSERT INTO users (name, email) VALUES (?, ?)`, u.Name, u.Email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u.ID, _ = result.LastInsertId()

	writeJSON(w, http.StatusCreated, u)
}

func updateUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var u User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	result, err := db.Exec(`UPDATE users SET name = ?, email = ? WHERE id = ?`, u.Name, u.Email, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	u.ID = id
	writeJSON(w, http.StatusOK, u)
}

func deleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	result, err := db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ---------- wiring ----------

func main() {
	initDB()
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /users", getUsers)
	mux.HandleFunc("GET /users/{id}", getUser)
	mux.HandleFunc("POST /users", createUser)
	mux.HandleFunc("PUT /users/{id}", updateUser)
	mux.HandleFunc("DELETE /users/{id}", deleteUser)

	server := &http.Server{
		Addr:    ":8080",
		Handler: loggingMiddleware(mux),
	}

	log.Println("Server running on :8080 (data stored in tiny-crud.db)")
	log.Fatal(server.ListenAndServe())
}
