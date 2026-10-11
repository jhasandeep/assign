// Command worker pushes pending vouchers to the accounting system.
// Run as many copies as you like; they coordinate through Postgres.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"sutra/internal/config"
	"sutra/internal/store"
	"sutra/internal/worker"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("worker stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	config.LoadDotEnv()
	url, err := config.DatabaseURL()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	s, err := store.New(ctx, url)
	if err != nil {
		return err
	}
	defer s.Close()

	cfg := worker.DefaultConfig()
	cfg.Concurrency = config.Int("WORKER_CONCURRENCY", cfg.Concurrency)
	cfg.MaxAttempts = config.Int("WORKER_MAX_ATTEMPTS", cfg.MaxAttempts)
	cfg.Poll = config.Duration("WORKER_POLL", cfg.Poll)
	cfg.Lease = config.Duration("WORKER_LEASE", cfg.Lease)
	cfg.BackoffBase = config.Duration("WORKER_BACKOFF_BASE", cfg.BackoffBase)
	cfg.BackoffMax = config.Duration("WORKER_BACKOFF_MAX", cfg.BackoffMax)
	httpTimeout := config.Duration("ACCOUNTING_TIMEOUT", 3*time.Second)
	if cfg.Lease <= httpTimeout {
		log.Warn("WORKER_LEASE should exceed ACCOUNTING_TIMEOUT or workers may double-claim", "lease", cfg.Lease, "timeout", httpTimeout)
	}

	acct := worker.NewAccounting(config.String("ACCOUNTING_URL", "http://localhost:9090"), httpTimeout)
	log.Info("worker started", "concurrency", cfg.Concurrency, "http_timeout", httpTimeout, "max_attempts", cfg.MaxAttempts)
	worker.New(s, acct, cfg, log).Run(ctx)
	log.Info("worker stopped")
	return nil
}
