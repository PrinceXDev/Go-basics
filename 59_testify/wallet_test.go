package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// ===========================================================================
// PART 1 -- assert vs require
// ===========================================================================

func TestAssertVsRequire(t *testing.T) {
	// assert.*  -> marks the test failed, execution CONTINUES.
	// require.* -> marks the test failed, execution STOPS right here.
	//
	// Rule of thumb:
	//   require for anything the rest of the test depends on
	//   assert  for independent expectations you want reported together
	acc := &Account{ID: "acc-1", Balance: 5_000}

	require.NotNil(t, acc)                   // if this fails, the lines below would panic
	assert.Equal(t, "acc-1", acc.ID)         // both of these run even if one fails,
	assert.EqualValues(t, 5000, acc.Balance) // so you see every problem at once

	// Equal uses ObjectsAreEqual -> reflect.DeepEqual for non-bytes.
	// It is TYPE STRICT: int64(5000) != int(5000).
	assert.NotEqual(t, 5000, acc.Balance)     // int vs int64 -> not equal!
	assert.Equal(t, int64(5000), acc.Balance) // this is the correct form
	// EqualValues converts types before comparing -- handy, but it hides
	// genuine type mistakes, so prefer Equal with an explicit conversion.
}

// ===========================================================================
// PART 2 -- error assertions (the ones interviews ask about)
// ===========================================================================

func TestErrorAssertions(t *testing.T) {
	svc := NewWalletService(newFakeLedger(&Account{ID: "a", Balance: 100}), noopNotifier{})

	_, err := svc.Withdraw("a", 500)

	require.Error(t, err)
	// ErrorIs unwraps the %w chain -- this is what you assert on, NOT the string.
	assert.ErrorIs(t, err, ErrInsufficientFunds)
	// Contains on the message is a weaker, brittle check; use it only for
	// context you deliberately added.
	assert.Contains(t, err.Error(), "withdraw 500 from a")

	// ErrorAs for typed errors:
	var target *MyValidationError
	assert.False(t, errors.As(err, &target)) // not that kind of error

	// Happy path:
	acc, err := svc.Withdraw("a", 40)
	require.NoError(t, err) // stop if this failed -- acc would be nil
	assert.Equal(t, int64(60), acc.Balance)
}

type MyValidationError struct{ Field string }

func (e *MyValidationError) Error() string { return "invalid " + e.Field }

// ===========================================================================
// PART 3 -- testify/mock: expectations, argument matchers, call counts
// ===========================================================================

// MockLedger embeds mock.Mock. Each method records the call and replays
// whatever the test told it to return.
type MockLedger struct{ mock.Mock }

func (m *MockLedger) Get(id string) (*Account, error) {
	args := m.Called(id) // records the call, looks up the matching expectation
	// args.Get(0) is `any`; guard the nil case before type-asserting.
	acc, _ := args.Get(0).(*Account)
	return acc, args.Error(1)
}

func (m *MockLedger) Save(a *Account) error {
	args := m.Called(a)
	return args.Error(0)
}

type MockNotifier struct{ mock.Mock }

func (m *MockNotifier) Notify(accountID, message string) error {
	args := m.Called(accountID, message)
	return args.Error(0)
}

func TestWithdraw_WithMocks(t *testing.T) {
	ledger := new(MockLedger)
	notifier := new(MockNotifier)

	stored := &Account{ID: "acc-9", Balance: 1_000}

	// Expectation: Get("acc-9") returns stored, nil -- exactly once.
	ledger.On("Get", "acc-9").Return(stored, nil).Once()

	// mock.MatchedBy runs a predicate on the argument -- use it to assert the
	// service saved the CORRECT new balance, not just "something".
	ledger.On("Save", mock.MatchedBy(func(a *Account) bool {
		return a.ID == "acc-9" && a.Balance == 700
	})).Return(nil).Once()

	// mock.Anything when you genuinely do not care about an argument.
	notifier.On("Notify", "acc-9", mock.Anything).Return(nil)

	svc := NewWalletService(ledger, notifier)
	acc, err := svc.Withdraw("acc-9", 300)

	require.NoError(t, err)
	assert.Equal(t, int64(700), acc.Balance)

	// AssertExpectations fails the test if any .On(...) was never called,
	// or a .Once()/.Times(n) count was not met. Always call it.
	ledger.AssertExpectations(t)
	notifier.AssertExpectations(t)

	// Extra call-level assertions:
	ledger.AssertNumberOfCalls(t, "Get", 1)
	notifier.AssertCalled(t, "Notify", "acc-9", "withdrew 300 cents")
}

func TestWithdraw_NotifierFailureDoesNotFailWithdrawal(t *testing.T) {
	ledger := new(MockLedger)
	notifier := new(MockNotifier)

	ledger.On("Get", "x").Return(&Account{ID: "x", Balance: 50}, nil)
	ledger.On("Save", mock.Anything).Return(nil)
	notifier.On("Notify", mock.Anything, mock.Anything).Return(errors.New("smtp down"))

	_, err := NewWalletService(ledger, notifier).Withdraw("x", 10)

	assert.NoError(t, err, "a broken notifier must not break the withdrawal")
	notifier.AssertExpectations(t)
}

func TestWithdraw_LedgerFailure(t *testing.T) {
	ledger := new(MockLedger)
	ledger.On("Get", "ghost").Return(nil, ErrAccountNotFound)

	_, err := NewWalletService(ledger, new(MockNotifier)).Withdraw("ghost", 10)

	assert.ErrorIs(t, err, ErrAccountNotFound)
	// Save must never have been reached.
	ledger.AssertNotCalled(t, "Save", mock.Anything)
}

// ===========================================================================
// PART 4 -- table-driven tests, still the default in Go. testify just makes
// the assertion line shorter; the SHAPE stays the same.
// ===========================================================================

func TestWithdraw_Table(t *testing.T) {
	tests := []struct {
		name     string
		start    int64
		amount   int64
		wantErr  error
		wantLeft int64
	}{
		{name: "exact balance", start: 100, amount: 100, wantLeft: 0},
		{name: "partial", start: 100, amount: 30, wantLeft: 70},
		{name: "too much", start: 100, amount: 101, wantErr: ErrInsufficientFunds},
		{name: "zero amount", start: 100, amount: 0, wantErr: errors.New("amount must be positive")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // safe: each subtest builds its own service

			svc := NewWalletService(newFakeLedger(&Account{ID: "a", Balance: tc.start}), noopNotifier{})
			acc, err := svc.Withdraw("a", tc.amount)

			if tc.wantErr != nil {
				require.Error(t, err)
				// sentinel errors -> ErrorIs; ad-hoc ones -> message contains
				if errors.Is(tc.wantErr, ErrInsufficientFunds) {
					assert.ErrorIs(t, err, ErrInsufficientFunds)
				} else {
					assert.Contains(t, err.Error(), tc.wantErr.Error())
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantLeft, acc.Balance)
		})
	}
}

// ===========================================================================
// PART 5 -- suite.Suite: shared setup/teardown.
// Use it when several tests need the same expensive fixture (a DB, a server).
// Do NOT use it just to group tests -- subtests already do that, and suites
// hide state between tests, which is how flaky tests are born.
// ===========================================================================

type WalletSuite struct {
	suite.Suite
	svc    *WalletService
	ledger *fakeLedger
}

// SetupSuite runs once before all tests in the suite.
func (s *WalletSuite) SetupSuite() {
	s.T().Log("suite: open expensive resources here (DB, server)")
}

// SetupTest runs before EVERY test -- this is where you reset state.
func (s *WalletSuite) SetupTest() {
	s.ledger = newFakeLedger(&Account{ID: "s", Balance: 1_000})
	s.svc = NewWalletService(s.ledger, noopNotifier{})
}

// TearDownSuite runs once after all tests.
func (s *WalletSuite) TearDownSuite() { s.T().Log("suite: close resources here") }

func (s *WalletSuite) TestWithdrawSucceeds() {
	acc, err := s.svc.Withdraw("s", 250)
	s.Require().NoError(err)         // s.Require() == require bound to s.T()
	s.Equal(int64(750), acc.Balance) // s.Equal == assert.Equal bound to s.T()
}

func (s *WalletSuite) TestStateIsResetBetweenTests() {
	// Proves SetupTest ran again: balance is back to 1000, not 750.
	acc, err := s.svc.Withdraw("s", 1_000)
	s.Require().NoError(err)
	s.Zero(acc.Balance)
}

// This single Go test function is what actually runs the suite.
func TestWalletSuite(t *testing.T) { suite.Run(t, new(WalletSuite)) }

// ===========================================================================
// Hand-written fakes -- the alternative to testify/mock.
//
// When the dependency is small and you care about STATE (what ended up
// stored), a fake like this is clearer than a mock. Reach for testify/mock
// when you care about INTERACTIONS (was it called, with what, how often).
// ===========================================================================

type fakeLedger struct {
	accounts map[string]*Account
	saves    int
}

func newFakeLedger(seed ...*Account) *fakeLedger {
	f := &fakeLedger{accounts: map[string]*Account{}}
	for _, a := range seed {
		f.accounts[a.ID] = a
	}
	return f
}

func (f *fakeLedger) Get(id string) (*Account, error) {
	a, ok := f.accounts[id]
	if !ok {
		return nil, ErrAccountNotFound
	}
	// Return a copy so the service cannot mutate our stored state by accident.
	cp := *a
	return &cp, nil
}

func (f *fakeLedger) Save(a *Account) error {
	f.accounts[a.ID] = a
	f.saves++
	return nil
}

type noopNotifier struct{}

func (noopNotifier) Notify(string, string) error { return nil }

// Compile-time proof the fakes satisfy the interfaces. This one-liner is a
// very common Go idiom -- it fails at BUILD time, not at test time.
var (
	_ Ledger   = (*fakeLedger)(nil)
	_ Ledger   = (*MockLedger)(nil)
	_ Notifier = noopNotifier{}
	_ Notifier = (*MockNotifier)(nil)
)
