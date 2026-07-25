package calculator

// ============================================================================
// Test file conventions:
//   - filename ends in `_test.go`
//   - test functions start with `Test`, take `t *testing.T`, return nothing
//   - `go test` finds and runs every such function in the package
//
// Assertions: Go's testing package has NO built-in assert/expect functions
// (unlike Jest's `expect(x).toBe(y)`). You write plain if-statements and
// call t.Errorf/t.Fatalf yourself. This is intentional — Go favors
// explicitness over a DSL, though third-party libraries like testify add
// assertion helpers if you want them later.
// ============================================================================

import "testing"

func TestAdd(t *testing.T) {
	result := Add(2, 3)
	expected := 5
	if result != expected {
		// t.Errorf reports a failure but lets the test function continue
		// running (useful when checking multiple things in one test).
		t.Errorf("Add(2, 3) = %d; expected %d", result, expected)
	}
}

func TestDivide(t *testing.T) {
	result, err := Divide(10, 2)
	if err != nil {
		// t.Fatalf reports a failure AND stops this test function
		// immediately — use it when continuing would be meaningless
		// (e.g. an unexpected error means `result` can't be trusted).
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 5 {
		t.Errorf("Divide(10, 2) = %d; expected 5", result)
	}
}

func TestDivideByZero(t *testing.T) {
	_, err := Divide(10, 0)
	if err == nil {
		t.Error("expected an error when dividing by zero, got nil")
	}
}

// TABLE-DRIVEN TESTS — the idiomatic Go pattern for testing many
// input/output pairs without repeating the same test body over and over.
// JS/TS comparison: similar in spirit to Jest's `test.each([...])`.
func TestIsEven(t *testing.T) {
	// An anonymous slice of structs (lesson 11) defining each test case.
	cases := []struct {
		name     string
		input    int
		expected bool
	}{
		{"zero is even", 0, true},
		{"two is even", 2, true},
		{"three is odd", 3, false},
		{"negative even", -4, true},
		{"negative odd", -7, false},
	}

	for _, c := range cases {
		// t.Run creates a named SUBTEST — shows up individually in test
		// output, and lets you run just one with `go test -run TestIsEven/two_is_even`.
		t.Run(c.name, func(t *testing.T) {
			result := IsEven(c.input)
			if result != c.expected {
				t.Errorf("IsEven(%d) = %t; expected %t", c.input, result, c.expected)
			}
		})
	}
}
