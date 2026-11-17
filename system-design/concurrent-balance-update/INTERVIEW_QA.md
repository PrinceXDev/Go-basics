# Interview Q&A: Concurrent Balance Updates

Every claim below was reproduced by this project's demo and tests (PostgreSQL 18).

## 30-second answer

> This is a lost update caused by a read-modify-write race. Under READ COMMITTED, both transactions read 40, compute independently, and the last write overwrites the other, so the balance ends at 20 or 60 instead of 40.
>
> My default fix is an atomic update, `SET balance = balance + delta WHERE balance + delta >= 0`: one statement, no locks held while the app thinks, and it prevents overdraft too. If the logic needs several reads first, I use `SELECT FOR UPDATE` and keep the transaction short. If conflicts are rare or users hold data a long time, I use optimistic locking with a version column and retry. Another option is REPEATABLE READ or SERIALIZABLE with retry on SQLSTATE 40001.
>
> For payments I also add idempotency keys, integer money (paise), and an append-only ledger.

## The problem in one picture

```
A reads 40 ──► computes 60 ───────────► writes 60
B reads 40 ────────► computes 20 ──────────────► writes 20   (A's deposit erased)
```

No error is raised. The data is silently wrong. That is what makes it dangerous.

## Which fix when

| Situation                            | Use                            | Why                                          |
| ------------------------------------ | ------------------------------ | -------------------------------------------- |
| Counter, balance, stock              | Atomic SQL                     | One statement, fastest, no retry code        |
| Several reads/rules before write     | `FOR UPDATE`                   | Simple, correct, waits instead of failing    |
| Rare conflicts, long user think time | Optimistic version             | No locks held, detects "someone edited this" |
| Rule spans many rows                 | SERIALIZABLE + retry           | Database enforces it; also stops write skew  |
| Two accounts (transfer)              | `FOR UPDATE` in fixed id order | Prevents deadlock                            |

## Common follow-up questions

**Why not a Go mutex?**
It only protects one process. With several app instances the database is the only shared point of truth.

**Why not SERIALIZABLE everywhere?**
Under contention it aborts many transactions and every caller needs retry code. Correct but costly on hot rows. Demo: ~900 retries for 60 operations on one row, versus 0 for atomic SQL.

**What is a deadlock and how do you avoid it?**
Transfer A->B locks A then waits for B, while B->A locks B then waits for A. Neither can proceed. PostgreSQL detects it (after `deadlock_timeout`, 1 second by default), aborts one with SQLSTATE 40P01, and the other succeeds. Avoid it by always locking rows in the same order (lowest id first), keeping transactions short, and retrying on 40P01. Demo scenario 4 shows all three outcomes.

**Pessimistic or optimistic?**
Pessimistic: assume conflict, lock first, others wait. Optimistic: assume no conflict, check at write time, retry on failure. High contention favours pessimistic or atomic; low contention favours optimistic.

**What does REPEATABLE READ do here?**
The second writer gets `could not serialize access due to concurrent update` (40001) instead of silently overwriting. The app must rerun the whole transaction.

**Why retry with backoff and jitter?**
Without a random delay the losers retry at the same moment and collide again.

**Is `SELECT` then `UPDATE` ever safe?**
Only if the read takes a lock (`FOR UPDATE`), or the transaction level or version check makes the write fail when the data changed.

**What about the check "enough balance"?**
Put it in the same statement as the write (atomic SQL) or after taking the lock. A check on a stale read lets ten ₹20 withdrawals all pass on ₹50 (demo scenario 3: naive paid out 200 from 50). Add `CHECK (balance >= 0)` as a last safety net.

**How do you make a retried request safe?**
An idempotency key stored with a unique constraint, so the same request applied twice changes the balance once.

**How would a real bank model this?**
An append-only ledger of entries (debit/credit) with the balance derived or cached, so every change is auditable and nothing is overwritten.

## Production checklist

- Money as integer paise or `NUMERIC`, never float
- Atomic or locked updates, never read-then-write blindly
- `CHECK (balance >= 0)` constraint
- Idempotency key per request
- Short transactions; lock multiple rows in a fixed order
- Retry on 40001 and 40P01 with backoff and jitter
- Tests that run operations concurrently and assert the final total

## Evidence from this project

| Claim                                                 | Where proven                                                                                         |
| ----------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Naive loses updates                                   | demo scenarios 1-2, `TestNaiveLosesUpdate`                                                           |
| Naive overspends                                      | demo scenario 3, `TestNaiveOverspends`                                                               |
| Atomic, FOR UPDATE, optimistic, isolation are correct | `TestTwoUsersAddAndDeduct`, `TestManyDepositsAndWithdrawals`, `TestInsufficientFundsUnderContention` |
| Opposite transfers deadlock                           | `TestOppositeTransfersDeadlock`                                                                      |
| Lock ordering fixes it                                | `TestOrderedLocksAvoidDeadlock`                                                                      |
| Retry also recovers                                   | `TestDeadlockRetrySucceeds`                                                                          |
| Money is conserved                                    | `TestManyTransfersConserveMoney`                                                                     |
