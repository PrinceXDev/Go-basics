# Concurrent Balance Updates: Lost Update Problem

Go + PostgreSQL demo of what goes wrong when two people change the same balance
at the same time, and four production-grade ways to fix it.

## 1. The problem (wallet example)

A wallet account has **₹40**. At almost the same moment:

- **User A** tops up **+₹20** (should give 60)
- **User B** pays **₹20** (should give 40 again)

Correct final balance: `40 + 20 - 20 = ₹40`.

The usual code does *read, calculate in the app, write*:

```sql
SELECT balance FROM accounts WHERE id = 1;          -- app computes new = old +/- 20
UPDATE accounts SET balance = <new> WHERE id = 1;   -- writes an absolute number
```

### Timeline (default isolation level: READ COMMITTED)

```
time   User A (+20)                    User B (-20)                  DB balance
----   ---------------------------     --------------------------    ----------
t1     BEGIN                           BEGIN                              40
t2     SELECT balance  -> 40                                              40
t3                                     SELECT balance  -> 40              40
t4     new = 40 + 20 = 60                                                 40
t5                                     new = 40 - 20 = 20                 40
t6     UPDATE balance = 60                                                60
t7     COMMIT                                                             60
t8                                     UPDATE balance = 20  (waits for    20
                                       A's row lock, then overwrites)
t9                                     COMMIT                             20  <-- WRONG
```

User A's ₹20 deposit **vanished**. If B had written first the result would be 60:
the money is wrong either way, and which one you get depends on timing.
This is a **race condition**, specifically the **lost update** anomaly.

**Why it happens:** B decided its number from a value (40) that was already stale
by t6. Row locks did not help because B's `SELECT` took no lock, and B's `UPDATE`
just wrote the stale-based number once A released the row.

A second symptom, **overspending**: balance 50, ten parallel ₹20 withdrawals all
read 50, all pass the "enough money?" check, and ₹200 leaves a ₹50 account.

## 2. Solutions

### A. Atomic SQL update

Let the database do the math and the check in one statement. No gap between
read and write.

```sql
UPDATE accounts SET balance = balance + $1
WHERE id = $2 AND balance + $1 >= 0;
-- 0 rows affected  => account missing or insufficient funds
```

### B. Pessimistic locking (`SELECT ... FOR UPDATE`)

Lock the row when you read it. Everyone else waits until you commit.

```sql
BEGIN;
SELECT balance FROM accounts WHERE id = 1 FOR UPDATE;  -- B blocks here until A commits
UPDATE accounts SET balance = 60 WHERE id = 1;
COMMIT;
```

B now reads **60** (not 40) once it gets the lock, so it writes 40. Correct.

### C. Optimistic locking (version column)

No lock. The write only succeeds if nobody changed the row since you read it.

```sql
SELECT balance, version FROM accounts WHERE id = 1;     -- 40, v0
UPDATE accounts SET balance = 60, version = version + 1
WHERE id = 1 AND version = 0;                           -- 1 row: A wins
-- B runs the same with v0: 0 rows affected -> conflict -> re-read (60, v1) and retry
```

### D. Transaction isolation levels

Keep the read-modify-write code, but run it at a stricter level. PostgreSQL then
**aborts** the loser with `SQLSTATE 40001` (`could not serialize access due to
concurrent update`) and the app **must retry the whole transaction**.

```sql
BEGIN ISOLATION LEVEL REPEATABLE READ;   -- or SERIALIZABLE
SELECT balance FROM accounts WHERE id = 1;
UPDATE accounts SET balance = 60 WHERE id = 1;   -- loser gets 40001 here
COMMIT;
```

| Level | Stops lost update? | Notes |
|---|---|---|
| READ COMMITTED (default) | No | Each statement sees the latest committed data |
| REPEATABLE READ | Yes (40001 on same-row conflict) | Snapshot taken at first statement |
| SERIALIZABLE | Yes | Also stops write skew across *different* rows; may abort more, even at commit |

## 3. Comparison

| | Atomic SQL | `FOR UPDATE` | Optimistic version | Isolation level + retry |
|---|---|---|---|---|
| Correct under contention | Yes | Yes | Yes (with retry) | Yes (with retry) |
| Holds locks across app logic | No | **Yes** | No | Row lock only at write |
| Retry code needed | No | No | **Yes** | **Yes** |
| Schema change | No | No | `version` column | No |
| Complex business rules in app | Hard (SQL only) | Easy | Easy | Easy |
| Throughput on a hot row | Best | Serialised, lock waits | Many retries when hot | Many aborts when hot |
| Deadlock risk | Low | Yes if many rows locked in different order | None | Possible (40P01) |
| Best for | Counters, balances, stock | High contention, must not fail, short transactions | Low contention, long think time, web forms ("someone edited this") | Many-row invariants (SERIALIZABLE), safety net |

Rule of thumb: **default to atomic SQL**. Use `FOR UPDATE` when the logic needs
several reads or rules before writing. Use optimistic when conflicts are rare or
the user may sit on a form for minutes. Use SERIALIZABLE when the rule spans rows
you cannot lock simply. Always keep transactions short.

Production notes: store money in the smallest unit (paise) or `NUMERIC`, add an
idempotency key per request, and when locking several accounts (transfers) lock
them in a fixed order, e.g. by id, to avoid deadlocks.

## 4. Code layout

```
migrations/001_accounts.up.sql     table: id, balance (CHECK >= 0), version (own schema balance_demo)
internal/wallet/wallet.go          Repo, errors, retry helpers
internal/wallet/strategies.go      Naive (broken), Atomic, Pessimistic, Optimistic, Isolated
internal/wallet/runner.go          RunConcurrent: goroutines released together
internal/wallet/wallet_test.go     tests (need PostgreSQL)
cmd/demo/main.go                   runs 3 scenarios against every strategy
```

The `Think` field on each strategy sleeps between read and write to simulate
app work. It makes the race happen every run instead of occasionally.

## 5. Run it

Start PostgreSQL (or point `DATABASE_URL` at any database you own; the demo only
creates the schema `balance_demo`):

```bash
docker compose up -d
```

```bash
export DATABASE_URL="postgres://wallet:change_me@localhost:5433/wallet?sslmode=disable"
```

```bash
go run ./cmd/demo
```

```bash
go test ./... -v -race
```

Tests skip automatically if `DATABASE_URL` is not set.

### Real demo output (captured against PostgreSQL 18)

Elapsed times and retry counts vary run to run. Naive is wrong in every
scenario; the other five are correct.

```
Scenario 1: balance 40, User A deposits +20 and User B withdraws -20 at the same time
  naive (broken)               ok=2   insufficient=0   failed=0   retries=0    final=60    WRONG
  atomic SQL                   ok=2   insufficient=0   failed=0   retries=0    final=40       OK
  pessimistic (FOR UPDATE)     ok=2   insufficient=0   failed=0   retries=0    final=40       OK
  optimistic (version)         ok=2   insufficient=0   failed=0   retries=1    final=40       OK
  isolation: repeatable read   ok=2   insufficient=0   failed=0   retries=1    final=40       OK
  isolation: serializable      ok=2   insufficient=0   failed=0   retries=1    final=40       OK

Scenario 2: balance 1000, 30 x +10 and 30 x -10
  naive (broken)               final=930   WRONG   (lost updates)
  all other strategies         final=1000  OK      (optimistic/isolation: ~900 retries)

Scenario 3: balance 50, ten x -20
  naive (broken)               ok=10  final=30     WRONG   (paid out 200 from 50)
  all other strategies         ok=2 insufficient=8 final=10  OK
```

Naive in scenario 1 can end at 60 or 20 depending on who writes last.
Atomic and `FOR UPDATE` always show `retries=0` because they wait instead of conflicting.

## 6. What the tests cover

| Test | Checks |
|---|---|
| `TestNaiveLosesUpdate` | The bug is real: +20 and -20 on 40 does not give 40 |
| `TestNaiveOverspends` | Naive lets more withdrawals through than the balance allows |
| `TestTwoUsersAddAndDeduct` | The ₹40 / +20 / -20 case ends at 40 for all 5 safe strategies |
| `TestManyDepositsAndWithdrawals` | 60 concurrent mixed operations keep 1000 |
| `TestInsufficientFundsUnderContention` | Exactly 2 of 10 withdrawals succeed, balance 10 |
| `TestOptimisticVersionAndConflicts` | Version bumps once per write, conflicts retried |
| `TestOptimisticGivesUp` | With no retries left you get `ErrConflict` |
| `TestIsolationRetriesOnSerializationFailure` | 40001 is raised and retried to a correct result |
| `TestIsolationWithoutRetryReportsConflict` | Without retry the loser fails; why retry is mandatory |
| `TestUnknownAccount` | Missing account returns `ErrAccountNotFound` |
