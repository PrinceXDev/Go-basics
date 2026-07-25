// Package store holds this project's core business logic for managing
// todos. It lives under internal/ because it is a private implementation
// detail of THIS module — the Go compiler physically prevents any project
// outside this module from importing it, even if they tried.
package store

import (
	"errors"
	"sync"
)

type Todo struct {
	ID   int
	Task string
	Done bool
}

// Store is a thread-safe in-memory todo store — note the sync.Mutex from
// lesson 24, since a real API server handles concurrent requests as
// goroutines and must protect shared state accordingly.
type Store struct {
	mu     sync.Mutex
	todos  []Todo
	nextID int
}

func New() *Store {
	return &Store{nextID: 1}
}

func (s *Store) Add(task string) Todo {
	s.mu.Lock()
	defer s.mu.Unlock()

	todo := Todo{ID: s.nextID, Task: task}
	s.todos = append(s.todos, todo)
	s.nextID++
	return todo
}

func (s *Store) All() []Todo {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Return a copy so callers can't mutate our internal slice directly
	// (ties back to lesson 09's shared-underlying-array gotcha).
	result := make([]Todo, len(s.todos))
	copy(result, s.todos)
	return result
}

var ErrNotFound = errors.New("todo not found")

func (s *Store) Complete(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.todos {
		if s.todos[i].ID == id {
			s.todos[i].Done = true
			return nil
		}
	}
	return ErrNotFound
}
