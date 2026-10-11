// Package testutil gives tests a real, isolated Postgres. The guarantees under
// test live in the database, so a fake would prove nothing.
package testutil

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"sutra/internal/config"
	"sutra/internal/store"
	"sutra/migrations"
)

var schemaSeq atomic.Int64

// NewStore creates a private schema on DATABASE_URL, migrates it, and returns a
// store whose connections are pinned to that schema. The schema is dropped when
// the test ends. Use a direct (non-pooled) Postgres URL.
func NewStore(t *testing.T) *store.Store {
	t.Helper()
	config.LoadDotEnv()
	base, err := config.DatabaseURL()
	if err != nil {
		t.Fatalf("tests need a real Postgres: %v", err)
	}
	ctx := context.Background()

	schema := fmt.Sprintf("test_%d_%d", time.Now().UnixNano(), schemaSeq.Add(1))

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to DATABASE_URL: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})

	s, err := store.New(ctx, withSearchPath(t, base, schema))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close)

	if _, err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func withSearchPath(t *testing.T, raw, schema string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	q.Set("pool_max_conns", "24") // enough connections for real concurrency
	u.RawQuery = q.Encode()
	return u.String()
}

// NewIssue inserts a party, SKU and an open issue of qty pieces at ratePaise
// per piece, and returns the issue id.
func NewIssue(t *testing.T, s *store.Store, qty int, ratePaise int64) int64 {
	t.Helper()
	var id int64
	err := s.Pool().QueryRow(context.Background(), `
		WITH p AS (INSERT INTO parties (name, phone) VALUES ('Test Party', '000') RETURNING id),
		     k AS (INSERT INTO skus (code, name, image_url)
		           VALUES ('T-' || gen_random_uuid()::text, 'Test SKU', 'http://example.test/x.png') RETURNING id)
		INSERT INTO jobwork_issues (party_id, sku_id, qty, rate_paise, issued_on)
		SELECT p.id, k.id, $1, $2, CURRENT_DATE FROM p, k
		RETURNING id`, qty, ratePaise).Scan(&id)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	return id
}

// Count runs a "SELECT count(*) ..." style query and returns the number.
func Count(t *testing.T, s *store.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Pool().QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", strings.Fields(query)[0], err)
	}
	return n
}
