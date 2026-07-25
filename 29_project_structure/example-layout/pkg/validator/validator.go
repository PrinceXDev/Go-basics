// Package validator holds small, reusable validation helpers.
//
// This lives under pkg/ because it has NO dependency on this project's
// business logic — it's generic enough that another, unrelated project
// could import it too, which is exactly the signal for putting code in
// pkg/ rather than internal/.
package validator

import "strings"

// NotEmpty reports whether a trimmed string has any content.
func NotEmpty(s string) bool {
	return strings.TrimSpace(s) != ""
}

// MaxLength reports whether s is no longer than max characters.
func MaxLength(s string, max int) bool {
	return len(s) <= max
}
