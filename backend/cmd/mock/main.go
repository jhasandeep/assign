// Command mock runs the unreliable mock accounting server.
//
//	MOCK_ADDR       listen address (default :9090)
//	MOCK_FAIL_RATE  fraction answered with HTTP 500 (default 0.2)
//	MOCK_HANG_RATE  fraction that hang MOCK_HANG_FOR then succeed (default 0.1)
//	MOCK_HANG_FOR   hang duration (default 5s)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"sutra/internal/config"
	"sutra/internal/mock"
)

func main() {
	config.LoadDotEnv()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg := mock.DefaultConfig()
	cfg.FailRate = config.Float("MOCK_FAIL_RATE", cfg.FailRate)
	cfg.HangRate = config.Float("MOCK_HANG_RATE", cfg.HangRate)
	cfg.HangFor = config.Duration("MOCK_HANG_FOR", cfg.HangFor)

	srv := &http.Server{
		Addr:              config.String("MOCK_ADDR", ":9090"),
		Handler:           mock.New(cfg, log).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("mock accounting listening", "addr", srv.Addr,
		"fail_rate", cfg.FailRate, "hang_rate", cfg.HangRate, "hang_for", cfg.HangFor)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("mock stopped", "err", err)
		os.Exit(1)
	}
}
