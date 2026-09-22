package main

// ============================================================================
// CONCEPT: testify -- the assertion/mocking library almost every Go team uses.
//
// Go's stdlib `testing` has no assertions on purpose: you write
//
//	if got != want { t.Errorf("got %v want %v", got, want) }
//
// That is fine, but it gets noisy fast. testify gives you three things:
//
//	assert   -- checks that REPORT a failure and keep going
//	require  -- checks that REPORT a failure and STOP the test (t.FailNow)
//	mock     -- record/replay fake implementations of your interfaces
//	suite    -- xUnit-style setup/teardown grouping (optional, use sparingly)
//
// The one rule people get wrong: use `require` for preconditions (err must be
// nil, pointer must be non-nil) and `assert` for the actual expectations.
// If you `assert.NoError` and then dereference the result, a failing test
// panics with a nil pointer instead of telling you what actually broke.
//
// Run: go test -v ./59_testify/
// ============================================================================

import (
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------
// The code under test: a tiny wallet service with an injected dependency.
// The dependency is an INTERFACE -- that is what makes it mockable.
// ---------------------------------------------------------------------------

var (
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrAccountNotFound   = errors.New("account not found")
)

// Account is the value we move money between.
type Account struct {
	ID      string
	Balance int64 // cents -- never use float64 for money
}

// Ledger is everything the service needs from storage. Small interface,
// defined by the CONSUMER (the service), not by the database package --
// that is idiomatic Go and it is also what makes mocking painless.
type Ledger interface {
	Get(id string) (*Account, error)
	Save(a *Account) error
}

// Notifier is a second dependency, used to show mock expectations on
// something whose result we do not care about (fire-and-forget).
type Notifier interface {
	Notify(accountID, message string) error
}

type WalletService struct {
	ledger   Ledger
	notifier Notifier
}

func NewWalletService(l Ledger, n Notifier) *WalletService {
	return &WalletService{ledger: l, notifier: n}
}

// Withdraw is the behaviour we will test from every angle.
func (s *WalletService) Withdraw(id string, amount int64) (*Account, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("withdraw %d: %w", amount, errors.New("amount must be positive"))
	}

	acc, err := s.ledger.Get(id)
	if err != nil {
		return nil, fmt.Errorf("withdraw %s: %w", id, err)
	}

	if acc.Balance < amount {
		return nil, fmt.Errorf("withdraw %d from %s: %w", amount, id, ErrInsufficientFunds)
	}

	acc.Balance -= amount
	if err := s.ledger.Save(acc); err != nil {
		return nil, fmt.Errorf("withdraw %s: %w", id, err)
	}

	// Fire-and-forget: a failed notification must not fail the withdrawal.
	_ = s.notifier.Notify(id, fmt.Sprintf("withdrew %d cents", amount))

	return acc, nil
}

func main() {
	fmt.Println("this lesson lives in wallet_test.go -- run: go test -v ./59_testify/")
}
