// Command migrate applies the SQL migrations (schema + seed data) to DATABASE_URL.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"sutra/internal/config"
	"sutra/internal/store"
	"sutra/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
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

	applied, err := s.Migrate(ctx, migrations.FS)
	for _, v := range applied {
		fmt.Println("applied", v)
	}
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("database is up to date")
	}
	return nil
}
