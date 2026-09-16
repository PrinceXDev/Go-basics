package main

// ============================================================================
// CONCEPT: Struct tags + reflection + input validation.
//
// WHY THIS MATTERS
// In lesson 16 you wrote `json:"name"` on a struct field and it "just
// worked". This lesson explains WHY: struct tags are string metadata
// attached to fields, readable at runtime via the `reflect` package. Any
// library can define its own tag namespace — `json`, `db`, `validate`,
// `env` — and that is how most of Go's ecosystem does declarative config.
//
// Then we use the most common third-party consumer of tags:
// go-playground/validator, which validates a struct against rules written
// in `validate:"..."` tags.
//
// JS/TS comparison: TypeScript types vanish at runtime, so you reach for
// zod/class-validator with decorators. Go's struct tags are the same idea,
// except they're plain strings baked into the binary and read with
// reflection instead of decorators.
// ============================================================================

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

// A struct tag is the backtick string AFTER the field type. Format is
// space-separated `key:"value"` pairs. The compiler does NOT check them —
// a typo like `jsonn:"id"` compiles fine and silently does nothing. This is
// the #1 struct-tag bug.
type User struct {
	ID    int    `json:"id"        db:"user_id"  validate:"required,gt=0"`
	Name  string `json:"name"      db:"name"     validate:"required,min=2,max=50"`
	Email string `json:"email"     db:"email"    validate:"required,email"`
	Age   int    `json:"age"       db:"age"      validate:"gte=0,lte=130"`
	Role  string `json:"role"      db:"role"     validate:"oneof=admin user guest"`
	Notes string `json:"-"         db:"-"        validate:"-"` // "-" = skip
}

func main() {
	// ---------- Part 1: reading tags yourself with reflect ----------
	// reflect lets you inspect types at runtime. This is exactly what
	// encoding/json does internally when it marshals your struct.
	fmt.Println("== reading struct tags via reflection ==")
	t := reflect.TypeOf(User{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		// .Tag.Get("json") returns "" if that key isn't present.
		fmt.Printf("field=%-6s type=%-6s json=%-7q db=%-9q validate=%q\n",
			f.Name, f.Type, f.Tag.Get("json"), f.Tag.Get("db"), f.Tag.Get("validate"))
	}

	// A tag value can carry options after a comma, e.g. `json:"age,omitempty"`.
	// Consumers split on "," themselves — the tag format only guarantees
	// key:"value", the inner structure is each library's own convention.
	fmt.Println()
	fmt.Println("== building a column list from db tags (mini-ORM in 6 lines) ==")
	var cols []string
	for i := 0; i < t.NumField(); i++ {
		if tag := t.Field(i).Tag.Get("db"); tag != "" && tag != "-" {
			cols = append(cols, tag)
		}
	}
	fmt.Printf("SELECT %s FROM users;\n", strings.Join(cols, ", "))

	// ---------- Part 2: validator/v10 ----------
	// One validator instance is safe for concurrent use and caches its
	// reflection work — create it ONCE at startup, not per request.
	v := validator.New(validator.WithRequiredStructEnabled())

	good := User{ID: 1, Name: "Ada", Email: "ada@example.com", Age: 36, Role: "admin"}
	bad := User{ID: 0, Name: "A", Email: "not-an-email", Age: 200, Role: "wizard"}

	fmt.Println()
	fmt.Println("== validating a good struct ==")
	report(v.Struct(good))

	fmt.Println()
	fmt.Println("== validating a bad struct ==")
	report(v.Struct(bad))
}

// report turns validator's error into something a human (or an API client)
// can actually use.
func report(err error) {
	if err == nil {
		fmt.Println("valid ✓")
		return
	}

	// validator returns ValidationErrors — a slice of per-field failures.
	// Use errors.As / a type assertion to get at the detail; printing the
	// raw error gives you one long unhelpful line.
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		fmt.Println("unexpected error:", err)
		return
	}

	for _, fe := range verrs {
		// fe.Field()=field name, fe.Tag()=which rule failed,
		// fe.Param()=the rule's argument, fe.Value()=what was supplied.
		fmt.Printf("  %-6s failed rule %-9q (param=%-18q got=%v)\n",
			fe.Field(), fe.Tag(), fe.Param(), fe.Value())
	}
}

// ----------------------------------------------------------------------------
// COMMON TAGS YOU WILL ACTUALLY USE
//   required            must be non-zero
//   omitempty (json)    drop the field from output when it's the zero value
//   -                   skip this field entirely
//   min/max             length for strings/slices, value for numbers
//   gt/gte/lt/lte       numeric comparisons
//   email, url, uuid    format checks
//   oneof=a b c         enum
//   dive                apply following rules to each element of a slice/map
//
// GOTCHAS
//   1. Tags are only read from EXPORTED fields. A lowercase field is
//      invisible to json and validator alike.
//   2. `required` means "not the zero value", so `required` on an int can
//      never accept 0, and on a bool can never accept false. Use a pointer
//      (*int, *bool) when you must distinguish "absent" from "zero".
//   3. Typos in tag keys are silent. `go vet` catches malformed tag SYNTAX
//      but not a wrong key name.
// ----------------------------------------------------------------------------
