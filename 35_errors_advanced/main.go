package main

// ============================================================================
// CONCEPT: error handling, part 2 — sentinel errors, custom error types,
// `errors.Is`, `errors.As`, wrapping chains, and `errors.Join`.
//
// WHY THIS MATTERS
// Lesson 08 taught you `if err != nil` and `fmt.Errorf("...: %w", err)`.
// That's enough to PROPAGATE an error. This lesson is about the other
// half: letting the CALLER inspect an error and react differently — 404 vs
// 500, retry vs give up, show the user vs log and swallow.
//
// The rule of thumb: the layer that CREATES the error decides what kind it
// is; every layer in between just wraps it with context; the top layer
// (HTTP handler, CLI main) decides what to do about it.
//
// JS/TS comparison: JS uses `instanceof CustomError` and `err.code`. Go's
// errors.As is `instanceof`, and errors.Is is the sentinel/`err.code`
// check. The big difference: Go's wrapping is EXPLICIT (`%w`), so you can
// always see in the source whether the chain is preserved.
// ============================================================================

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// ---------------------------------------------------------------------------
// PATTERN 1: SENTINEL ERRORS
// A package-level error value that callers compare against. Name them
// Err<Thing>. Export them only if callers genuinely need to branch on them —
// once exported, they're part of your public API forever.
// ---------------------------------------------------------------------------

var (
	ErrNotFound     = errors.New("not found")
	ErrUnauthorized = errors.New("unauthorized")
	ErrRateLimited  = errors.New("rate limited")
)

// ---------------------------------------------------------------------------
// PATTERN 2: CUSTOM ERROR TYPES
// Use these when the caller needs DATA out of the error, not just identity
// ("which field was invalid?", "how long until I can retry?").
//
// A type is an error if it has `Error() string`. Add `Unwrap() error` to
// make it part of a wrap chain.
// ---------------------------------------------------------------------------

type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed on %q: %s", e.Field, e.Reason)
}

// QueryError wraps a lower-level error while adding its own context.
type QueryError struct {
	Query string
	Err   error // the wrapped cause
}

func (e *QueryError) Error() string {
	return fmt.Sprintf("query %q: %v", e.Query, e.Err)
}

// Unwrap is what makes errors.Is/errors.As able to see THROUGH this error
// to e.Err. Without it, the chain stops here.
func (e *QueryError) Unwrap() error { return e.Err }

// ---------------------------------------------------------------------------
// A fake three-layer app: store -> service -> handler.
// ---------------------------------------------------------------------------

// Layer 1 (store): creates the raw error.
func findUser(id int) (string, error) {
	switch id {
	case 1:
		return "Ada", nil
	case 2:
		// Wrap a sentinel inside a custom type. The chain is now:
		//   *QueryError -> ErrNotFound
		return "", &QueryError{Query: "SELECT * FROM users WHERE id=2", Err: ErrNotFound}
	default:
		return "", &QueryError{Query: "SELECT ...", Err: ErrUnauthorized}
	}
}

// Layer 2 (service): adds context with %w. NEVER use %v here — %v flattens
// the error to a string and destroys the chain, so the handler above can no
// longer tell a 404 from a 401.
func loadProfile(id int) (string, error) {
	name, err := findUser(id)
	if err != nil {
		return "", fmt.Errorf("loadProfile(%d): %w", id, err)
	}
	return "profile of " + name, nil
}

// Layer 3 (handler): decides what to DO. This is the only layer that
// inspects the error.
func handle(id int) {
	profile, err := loadProfile(id)
	if err == nil {
		fmt.Printf("  200 OK  %s\n", profile)
		return
	}

	// errors.Is walks the whole chain comparing by IDENTITY (==) against a
	// sentinel. Use it for "is this THAT error?".
	switch {
	case errors.Is(err, ErrNotFound):
		fmt.Printf("  404     %v\n", err)
	case errors.Is(err, ErrUnauthorized):
		fmt.Printf("  401     %v\n", err)
	default:
		fmt.Printf("  500     %v\n", err)
	}

	// errors.As walks the chain looking for an error of a given TYPE and,
	// if found, assigns it to your pointer so you can read its fields.
	// Use it for "give me the typed error so I can use its data".
	var qe *QueryError
	if errors.As(err, &qe) {
		fmt.Printf("          (failing query was: %s)\n", qe.Query)
	}
}

func main() {
	fmt.Println("== three-layer error propagation ==")
	for _, id := range []int{1, 2, 99} {
		fmt.Printf("GET /users/%d\n", id)
		handle(id)
	}

	// ---------- errors.Is vs == ----------
	// Once an error has been wrapped, `==` fails but errors.Is still works.
	fmt.Println()
	fmt.Println("== why == is not enough ==")
	wrapped := fmt.Errorf("outer: %w", fmt.Errorf("middle: %w", ErrRateLimited))
	fmt.Println("err == ErrRateLimited      :", wrapped == ErrRateLimited)          //nolint // false
	fmt.Println("errors.Is(err, ErrRateLim) :", errors.Is(wrapped, ErrRateLimited)) // true
	fmt.Println("errors.Unwrap once         :", errors.Unwrap(wrapped))

	// ---------- %w vs %v ----------
	fmt.Println()
	fmt.Println("== percent-w preserves the chain, percent-v destroys it ==")
	withW := fmt.Errorf("ctx: %w", ErrNotFound)
	withV := fmt.Errorf("ctx: %v", ErrNotFound)
	fmt.Println("both print the same :", withW.Error() == withV.Error())
	fmt.Println("but Is(w-verb)      :", errors.Is(withW, ErrNotFound))
	fmt.Println("    Is(v-verb)      :", errors.Is(withV, ErrNotFound), "<- chain lost!")

	// ---------- errors.As with a custom type ----------
	fmt.Println()
	fmt.Println("== errors.As pulls data out ==")
	err := validateAge("abc")
	var ve *ValidationError
	if errors.As(err, &ve) {
		fmt.Printf("field=%q reason=%q\n", ve.Field, ve.Reason)
	}
	// errors.As also works against stdlib error types, e.g. strconv's:
	var numErr *strconv.NumError
	if errors.As(err, &numErr) {
		fmt.Printf("underlying strconv failure: func=%s input=%q\n", numErr.Func, numErr.Num)
	}

	// ---------- errors.Join (Go 1.20+) ----------
	// Collect MULTIPLE independent failures into one error — the natural
	// fit for form validation, or "close everything and report all errors".
	fmt.Println()
	fmt.Println("== errors.Join: many failures, one error ==")
	joined := errors.Join(
		&ValidationError{Field: "email", Reason: "missing @"},
		&ValidationError{Field: "age", Reason: "must be >= 0"},
		ErrUnauthorized,
	)
	fmt.Println(joined) // one per line
	fmt.Println("Is(joined, ErrUnauthorized):", errors.Is(joined, ErrUnauthorized))

	// errors.Join skips nils, and returns nil if EVERY argument is nil —
	// so you can accumulate unconditionally and check once at the end.
	fmt.Println("Join(nil, nil) == nil      :", errors.Join(nil, nil) == nil)

	// ---------- stdlib sentinels you'll actually meet ----------
	fmt.Println()
	fmt.Println("== stdlib sentinels ==")
	_, ferr := os.Open("definitely-does-not-exist.txt")
	fmt.Println("os.IsNotExist style :", errors.Is(ferr, os.ErrNotExist))
	// Others worth knowing: io.EOF, sql.ErrNoRows, context.Canceled,
	// context.DeadlineExceeded, http.ErrServerClosed.
}

func validateAge(raw string) error {
	n, err := strconv.Atoi(raw)
	if err != nil {
		// Wrap BOTH: a typed error of ours AND the strconv cause. Here we
		// build the chain by hand so errors.As can find either one.
		return fmt.Errorf("%w: %w", &ValidationError{Field: "age", Reason: "not a number"}, err)
	}
	if n < 0 {
		return &ValidationError{Field: "age", Reason: "must be >= 0"}
	}
	return nil
}

// ----------------------------------------------------------------------------
// DECISION TABLE — which pattern do I reach for?
//
//   Caller only needs to know WHICH error       -> sentinel + errors.Is
//   Caller needs DATA from the error            -> custom type + errors.As
//   Adding context while passing an error up    -> fmt.Errorf("...: %w", err)
//   Several independent failures at once        -> errors.Join
//   Nobody will ever inspect it                 -> fmt.Errorf("...: %w", err)
//                                                  and move on
//
// RULES
//   1. Handle an error ONCE. Either return it or log it — doing both
//      produces the same failure logged five times at five layers.
//   2. Always %w, never %v, when passing an error upward.
//   3. Error strings are lowercase and have no trailing punctuation:
//      "open config: permission denied", not "Open config failed!".
//      They get concatenated, so they must read as a sentence fragment.
//   4. Return the custom type as a POINTER (*ValidationError) and check for
//      it the same way. Mixing value and pointer receivers here is a
//      classic source of "errors.As never matches".
//   5. A nil *MyError stored in an `error` interface is NOT nil. Never
//      declare `var e *MyError` and return it directly — return `nil`.
// ----------------------------------------------------------------------------
