# Sutra Mini

A reliable accounting push for job-work receipts: Go + Postgres (Neon) + one Next.js page.
Receiving fabric back from a job-worker records a receipt **and** an accounting voucher in one
transaction; a worker then pushes vouchers to a flaky accounting API without ever posting one twice.

```
backend/   Go: API, sync worker, mock accounting server, migrations, tests
web/       Next.js: the /sync page
```

## Run it (clean machine)

**Prerequisites:** Go 1.25+, Node 20+, a free [Neon](https://neon.tech) project.

1. **Database URL.** In Neon, open *Connect*, switch **Connection pooling off**, copy the string.
   ```bash
   cp .env.example .env        # then paste the string into DATABASE_URL
   ```
2. **Create tables and seed data** (5 SKUs, 2 job-workers, 4 open issues):
   ```bash
   cd backend
   go run ./cmd/migrate
   ```
3. **Start the three Go processes**, each in its own terminal, all from `backend/`:
   ```bash
   go run ./cmd/mock      # unreliable accounting server  :9090
   go run ./cmd/api       # HTTP API                      :8080
   go run ./cmd/worker    # voucher sync worker
   ```
4. **Start the page**:
   ```bash
   cd web && npm install && npm run dev      # http://localhost:3000/sync
   ```

Every command reads `.env` from the repo root (real environment variables win).

## Try it

```bash
curl localhost:8080/issues/1        # issued / received / outstanding   (issue 1 = 40 pcs)

curl -i -X POST localhost:8080/jobwork/receipts \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-1' \
  -d '{"issue_id":1,"qty":5,"received_on":"2026-10-10"}'          # 201
# same command again -> 200 + "Idempotent-Replayed: true", same body, nothing new created

curl 'localhost:8080/vouchers?status=pending'
curl -X POST localhost:8080/vouchers/1/retry
```

Status codes for `POST /jobwork/receipts`: `201` created, `200` replay of a known key,
`400` bad input, `404` unknown issue, `409` would exceed the issued quantity (body has `outstanding`),
`422` the key was already used with a *different* request.

**Overshoot under concurrency** (issue 1 has 40 pieces; 10 x 5 = 50 is too many):
```bash
for i in $(seq 1 10); do
  curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/jobwork/receipts \
    -H "Idempotency-Key: k-$i" -H 'Content-Type: application/json' \
    -d '{"issue_id":1,"qty":5,"received_on":"2026-10-10"}' &
done | sort | uniq -c; wait         # 8 x 201, 2 x 409
```

**See a voucher fail, then retry it** - restart the mock so every call fails, create a receipt, wait
~15 s until it shows under *Failed* on `/sync`, restart the mock normally, press **Retry**:
```bash
MOCK_FAIL_RATE=1 go run ./cmd/mock        # PowerShell: $env:MOCK_FAIL_RATE=1; go run ./cmd/mock
```

**Crash safety:** create a batch of receipts, kill the worker while the mock is mid-hang (the mock logs
`injected hang`), restart it - or start two. Every voucher ends `pushed` exactly once; the mock logs
`duplicate client_ref` for any call that had already landed, and the worker records that as success.

## Tests

```bash
cd backend && go test ./... -count=1
```
They need `DATABASE_URL` (the guarantees live in Postgres, so a fake would prove nothing). Each test
creates and drops its own schema, so your data is untouched.

| Test | Proves |
|---|---|
| `TestConcurrentReceiptsCannotOvershoot` | 20 simultaneous 1-piece receipts against 10 issued -> exactly 10 accepted; received never exceeds issued; one voucher per accepted receipt |
| `TestIdempotencyKeyReplayCreatesNothing` | replays (sequential and 12 concurrent) return the same body and add no receipt or voucher; a reused key with a different body is rejected |
| `TestClaimIsExclusiveAcrossWorkers` | 6 workers claiming 60 vouchers never get the same one twice |
| `TestTimeoutThenRetryIsReconciledThroughDuplicate` | timeout after the call landed -> retry gets 409 -> pushed, one voucher created upstream |
| `TestRepeatedFailureEndsFailedAndCanBeRetried` | persistent 500s end `failed` with a reason; Retry requeues it |

I checked the first two are not vacuous by breaking the migration (dropping the `CHECK`, then the
`UNIQUE` key): both tests fail.

## How the guarantees are enforced

| Requirement | Mechanism (all in Postgres) |
|---|---|
| No overshoot | Trigger on `jobwork_receipts` adds `qty` to `jobwork_issues.received_qty`; `CHECK (received_qty <= qty)` rejects the overshooting write. The `UPDATE` row-locks the issue, so concurrent receipts queue. |
| Receipt + voucher atomic | One transaction in `store.CreateReceipt`. |
| Idempotency | `idempotency_key UNIQUE` + `INSERT ... ON CONFLICT DO NOTHING`. A concurrent duplicate waits on the unique index, then replays the committed result. `request_hash` catches key reuse with a different payload. |
| Never post twice | Each voucher has a `client_ref` fixed at creation and sent on every attempt. The accounting system answers a repeat with `409` + original `external_ref`; the worker treats that as success. |
| Two workers / crash | `UPDATE ... WHERE id IN (SELECT ... FOR UPDATE SKIP LOCKED)` claims rows and sets a lease (`locked_until`). A dead worker's lease expires and another picks the voucher up. |
| Slow endpoint doesn't block | One goroutine per in-flight voucher; HTTP client timeout (3 s) is shorter than the lease (15 s). |
| Backoff | `base * 2^(attempt-1)`, capped at 30 s, +/-20% jitter; 5 attempts then `failed` with `last_error`. 4xx other than 409/408/429 fail immediately. |
| Money | `bigint` paise everywhere; `amount_paise = qty * rate_paise`. Only the UI formats rupees. |

## Design notes and known limits

- I added `received_qty` and `rate_paise` to `jobwork_issues` and `request_hash`, `next_attempt_at`,
  `locked_until` columns elsewhere; `rate_paise` exists because a voucher needs an amount. See
  `backend/migrations/`.
- Retry resets `attempts` to 0 (a fresh budget); it only acts on `failed` vouchers and refuses `pushed`.
- The mock keeps its state in memory. Restarting it forgets previously posted `client_ref`s - fine for
  the demo, but a real accounting system would persist them.
- The stretch `POST /ask` endpoint was **not** built.
