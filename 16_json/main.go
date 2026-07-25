package main

// ============================================================================
// CONCEPT: Working with JSON — the `encoding/json` package.
//
// WHY THIS MATTERS
// JS has JSON built into the language (`JSON.stringify` / `JSON.parse`)
// because JS objects and JSON are structurally almost the same thing. Go
// has no such shortcut — Go structs are STATICALLY TYPED, so converting
// to/from JSON needs an explicit package: `encoding/json`.
//
// TWO DIRECTIONS:
//   Marshal   = Go value  -> JSON bytes   (like JSON.stringify)
//   Unmarshal = JSON bytes -> Go value    (like JSON.parse)
//
// STRUCT TAGS: the `json:"..."` text after each field is a STRUCT TAG —
// metadata attached to a field that the json package reads via reflection
// to know what JSON key to use. WITHOUT tags, Go uses the exact Go field
// name (which usually looks wrong in JSON since Go fields must start
// uppercase to be exported, but JSON convention is lowercase/camelCase).
// ============================================================================

import (
	"encoding/json"
	"fmt"
)

// Struct tags map Go fields to JSON keys, and control extra behavior:
//   `json:"name"`            -> use "name" as the JSON key
//   `json:"email,omitempty"` -> omit this key from output if it's the
//                               zero value (empty string, 0, nil, etc.)
//   `json:"-"`               -> never include this field in JSON at all
type User struct {
	Name     string `json:"name"`
	Age      int    `json:"age"`
	Email    string `json:"email,omitempty"`
	Password string `json:"-"` // never serialized — e.g. sensitive data
}

// Nested structs serialize as nested JSON objects automatically.
type Address struct {
	City    string `json:"city"`
	Country string `json:"country"`
}

type Profile struct {
	User    User    `json:"user"`
	Address Address `json:"address"`
	Tags    []string `json:"tags"`
}

func main() {
	// ---------- MARSHAL: Go struct -> JSON ----------
	u := User{Name: "Alice", Age: 30, Email: "alice@example.com", Password: "secret123"}

	jsonBytes, err := json.Marshal(u)
	if err != nil {
		fmt.Println("marshal error:", err)
		return
	}
	// json.Marshal returns []byte, not string — convert for printing.
	// Notice: no "password" key at all (json:"-"), and the field order
	// matches the JSON tags, not necessarily struct declaration order
	// intuition from JS.
	fmt.Println("Marshaled JSON:", string(jsonBytes))

	// MarshalIndent produces pretty-printed, human-readable JSON — useful
	// for logs/debugging, equivalent to JSON.stringify(obj, null, 2) in JS.
	prettyBytes, _ := json.MarshalIndent(u, "", "  ")
	fmt.Println("Pretty JSON:\n" + string(prettyBytes))

	// omitempty in action: an empty Email disappears from the output.
	u2 := User{Name: "Bob", Age: 25}
	jsonBytes2, _ := json.Marshal(u2)
	fmt.Println("Bob's JSON (no email key):", string(jsonBytes2))

	// ---------- UNMARSHAL: JSON -> Go struct ----------
	// Typically this JSON comes from an API response, a file, etc. Here
	// it's a raw string literal for demonstration.
	rawJSON := []byte(`{"name": "Charlie", "age": 40, "email": "charlie@example.com"}`)

	var decoded User
	// Unmarshal needs a POINTER (&decoded) so it can WRITE into your
	// variable — this ties directly back to lesson 13's pointer lesson.
	err = json.Unmarshal(rawJSON, &decoded)
	if err != nil {
		fmt.Println("unmarshal error:", err)
		return
	}
	fmt.Printf("Decoded: %+v\n", decoded) // %+v shows field names too

	// ---------- Nested structs and slices ----------
	profile := Profile{
		User:    User{Name: "Dana", Age: 28},
		Address: Address{City: "Berlin", Country: "Germany"},
		Tags:    []string{"admin", "beta-tester"},
	}
	profileJSON, _ := json.MarshalIndent(profile, "", "  ")
	fmt.Println("Profile JSON:\n" + string(profileJSON))

	// ---------- Unmarshaling into a map when you don't know the shape ----------
	// If you don't have (or don't want) a struct, decode into
	// map[string]any — the JSON equivalent of "just give me an object".
	unknownJSON := []byte(`{"status": "ok", "count": 5, "active": true}`)
	var result map[string]any
	json.Unmarshal(unknownJSON, &result)
	fmt.Println("Decoded into map:", result)
	// Values come back as `any` — you'd need a type assertion (lesson 14)
	// to use them as a specific type, e.g. result["count"].(float64)
	// (JSON numbers always decode as float64 in Go by default).
	if count, ok := result["count"].(float64); ok {
		fmt.Println("count as float64:", count)
	}
}
