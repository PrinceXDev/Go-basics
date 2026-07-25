package main

// ============================================================================
// CONCEPT: Third-party modules — using code someone else published.
//
// HOW IT WORKS (compare to `npm install`)
//   1. `go get github.com/google/uuid` downloads the package, adds it to
//      go.mod as a dependency (with its exact version), and records a
//      cryptographic hash of it in go.sum (auto-created) for integrity
//      verification — similar in spirit to npm's package-lock.json.
//   2. Import it using its full path, exactly like your own module's
//      packages in lesson 17 — `import "github.com/google/uuid"`.
//   3. Go caches downloaded modules globally on your machine (not in a
//      per-project node_modules-style folder), so re-using the same
///     dependency across projects doesn't re-download it.
//
// SEMANTIC VERSIONING: Go dependencies are pinned to a specific version in
// go.mod, e.g. `github.com/google/uuid v1.6.0`, following semver
// (MAJOR.MINOR.PATCH) — same convention as npm packages.
//
// This example was already fetched with:
//   go get github.com/google/uuid
// which updated go.mod and created go.sum in the project root.
// ============================================================================

import (
	"fmt"

	"github.com/google/uuid"
)

func main() {
	// uuid.New() is an EXPORTED function from the third-party package —
	// used exactly like a standard library or local package call.
	id := uuid.New()
	fmt.Println("Generated UUID:", id)
	fmt.Println("As string:", id.String())

	// Parsing an existing UUID string back into the type — demonstrates
	// the package returning (value, error), the same idiom from lesson 08.
	parsed, err := uuid.Parse("123e4567-e89b-12d3-a456-426614174000")
	if err != nil {
		fmt.Println("parse error:", err)
		return
	}
	fmt.Println("Parsed UUID:", parsed)

	// Invalid input demonstrates the error path.
	_, err = uuid.Parse("not-a-valid-uuid")
	if err != nil {
		fmt.Println("Expected parse error:", err)
	}
}
