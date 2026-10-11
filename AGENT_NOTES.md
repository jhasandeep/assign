# AGENT_NOTES

> DRAFT - written by the coding agent from what actually happened in the session. Edit the
> "Your own notes" section at the bottom in your own words before submitting.

## Tools used

- **Claude Code (Claude Sonnet 5.5)** in VS Code wrote the code, migrations, tests and docs from the
  case-study brief, and ran everything (build, tests, end-to-end checks).
- Go 1.27 (installed via winget during the session; `go.mod` says 1.25), Postgres 17 (throwaway local
  cluster for verification), Node 24 / Next.js 16.

## What was verified, and against what

- `go vet`, `go test ./...` (5 tests) and a manual end-to-end run (real mock + API + worker + Next.js proxy)
  all against a **local Postgres 17**, not Neon. Nothing here touched Neon yet; the code uses plain
  Postgres features only (trigger, CHECK, `ON CONFLICT`, `FOR UPDATE SKIP LOCKED`), but the first real
  run on Neon is still to be done.
- End-to-end observations: 10 concurrent receipts of 5 against 40 issued -> 8 x 201, 2 x 409; replay ->
  200 with `Idempotent-Replayed: true`; worker killed mid-flight then two workers started -> 38 vouchers,
  38 distinct posts at the mock, 0 double-posts, 1 duplicate answered 409 and reconciled; mock at 100%
  failure -> voucher ended `failed` after 5 attempts, restart mock + Retry -> `pushed`; Retry on a
  pushed voucher -> 409.
- **Not done:** the 3-minute recording, a run on a genuinely clean machine, the stretch `POST /ask`.

## What the agent got wrong or did badly

1. **PDF reading failed first.** The Read tool needs `pdftoppm`, which isn't installed; it had to fall
   back to `pdftotext`. No impact on the code.
2. **A weak mutation check.** To prove the idempotency test is meaningful I removed `UNIQUE` from
   `idempotency_key`. The test did fail - but because `ON CONFLICT (idempotency_key)` then errors
   ("no unique or exclusion constraint"), not because duplicate rows were created. So that check proves
   the test is sensitive to the constraint, less strongly than "duplicates get through". The overshoot
   mutation (dropping the `CHECK`) is the convincing one: it shows `received 30 > issued 10`.
3. **Sloppy first draft of a test helper.** Idempotency keys in `worker_test.go` were first generated
   with a `string(rune('a'+i%26))` hack; replaced with `fmt.Sprintf`.
4. **`go get` silently bumped `go.mod`** to `go 1.25.0` (it follows the newest dependency). README states
   Go 1.25+ accordingly.

## Design decisions worth challenging

- **Overshoot guard is a trigger + CHECK, not application code.** Chosen so that any writer, not just
  this API, is bound by it. Trade-off: receipts are append-only by convention only; an UPDATE/DELETE on
  `jobwork_receipts` would not adjust `received_qty`. A production version should forbid those.
- **Test isolation:** a first instinct was to run tests against shared tables. Parallel test packages
  would then see each other's pending vouchers (the claim test would steal them). Each test now gets its
  own schema (`search_path`), which is why tests need a direct, non-pooled connection.
- **Timeout vs. lease:** the lease (15 s) must exceed the HTTP timeout (3 s), otherwise a second worker
  could claim a voucher still in flight. Even then the 409 path makes the overlap harmless; the worker
  logs a warning if the config violates this.
- **The mock lets a hung request land after the client gave up.** That is the case the 409 path exists for.

## Your own notes

<!-- Replace this section: which parts you reviewed or changed, one thing YOU caught in the agent's
output, and anything you would do differently. The brief asks for a candid account of "one thing you
caught"; the agent cannot honestly write that for you. -->

- Tools I used besides the above:
- What I checked and what I changed:
- One thing I caught:
