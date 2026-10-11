// Package worker pushes pending vouchers to the accounting system.
//
// Safety model, in one paragraph: a voucher is claimed with a time-limited
// lease (FOR UPDATE SKIP LOCKED), so two workers never take the same row at
// once. Every attempt sends the same client_ref, and the accounting system
// answers a repeat with 409 + the original external_ref, which we record as
// success. So "worker died after the HTTP call but before the DB update", or
// "lease expired while we were slow", just causes one more call that is
// reconciled through the 409 path. Nothing is ever posted twice.
package worker

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"sutra/internal/store"
)

type Config struct {
	Concurrency int           // vouchers in flight at once
	Poll        time.Duration // sleep when nothing is due
	Lease       time.Duration // must exceed the HTTP timeout
	MaxAttempts int           // attempts before a voucher ends 'failed'
	BackoffBase time.Duration
	BackoffMax  time.Duration
}

func DefaultConfig() Config {
	return Config{
		Concurrency: 8,
		Poll:        500 * time.Millisecond,
		Lease:       15 * time.Second,
		MaxAttempts: 5,
		BackoffBase: time.Second,
		BackoffMax:  30 * time.Second,
	}
}

type Worker struct {
	store *store.Store
	acct  *Accounting
	cfg   Config
	log   *slog.Logger
}

func New(s *store.Store, acct *Accounting, cfg Config, log *slog.Logger) *Worker {
	return &Worker{store: s, acct: acct, cfg: cfg, log: log}
}

// Run claims and pushes vouchers until ctx is cancelled. Each voucher runs in
// its own goroutine, so one slow call never blocks the others.
func (w *Worker) Run(ctx context.Context) {
	slots := make(chan struct{}, w.cfg.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()

	for ctx.Err() == nil {
		free := cap(slots) - len(slots)
		var claimed []store.ClaimedVoucher
		if free > 0 {
			var err error
			claimed, err = w.store.ClaimVouchers(ctx, free, w.cfg.Lease)
			if err != nil {
				if ctx.Err() == nil {
					w.log.Error("claim vouchers", "err", err)
				}
				claimed = nil
			}
		}
		for _, v := range claimed {
			slots <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				w.process(ctx, v)
			}()
		}
		if len(claimed) == 0 {
			select {
			case <-ctx.Done():
			case <-time.After(w.cfg.Poll):
			}
		}
	}
}

// ProcessOnce claims up to limit due vouchers and processes them to
// completion. Used by tests; Run is the production loop.
func (w *Worker) ProcessOnce(ctx context.Context, limit int) (int, error) {
	claimed, err := w.store.ClaimVouchers(ctx, limit, w.cfg.Lease)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, v := range claimed {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.process(ctx, v)
		}()
	}
	wg.Wait()
	return len(claimed), nil
}

func (w *Worker) process(ctx context.Context, v store.ClaimedVoucher) {
	log := w.log.With("voucher_id", v.ID, "client_ref", v.ClientRef, "attempt", v.Attempts)
	res := w.acct.Push(ctx, v)

	// If we are shutting down, do not record a bogus failure: leave the lease to
	// expire and let the next worker pick the voucher up.
	if ctx.Err() != nil {
		log.Warn("shutting down mid-push; lease will expire")
		return
	}

	// Result writes get their own short deadline, independent of the request.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	switch res.Outcome {
	case Pushed:
		if err := w.store.MarkPushed(wctx, v.ID, res.ExternalRef); err != nil {
			// The call landed but we could not record it. Safe: the lease expires,
			// the next attempt gets 409 + external_ref and records it then.
			log.Error("mark pushed", "err", err)
			return
		}
		log.Info("pushed", "external_ref", res.ExternalRef)
	default:
		final := res.Outcome == Permanent || v.Attempts >= w.cfg.MaxAttempts
		backoff := w.backoff(v.Attempts)
		if err := w.store.MarkAttemptFailed(wctx, v.ID, res.Reason, final, backoff); err != nil {
			log.Error("record failure", "err", err)
			return
		}
		if final {
			log.Warn("voucher failed", "reason", res.Reason)
		} else {
			log.Info("attempt failed, will retry", "reason", res.Reason, "in", backoff.Round(time.Millisecond))
		}
	}
}

// backoff is exponential (base * 2^(attempt-1)), capped, with +/-20% jitter so
// vouchers that failed together do not retry in lockstep.
func (w *Worker) backoff(attempt int) time.Duration {
	d := w.cfg.BackoffBase
	for i := 1; i < attempt && d < w.cfg.BackoffMax; i++ {
		d *= 2
	}
	if d > w.cfg.BackoffMax {
		d = w.cfg.BackoffMax
	}
	jitter := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(d) * jitter)
}
