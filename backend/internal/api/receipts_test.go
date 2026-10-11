package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"sutra/internal/api"
	"sutra/internal/store"
	"sutra/internal/testutil"
)

type resp struct {
	status   int
	replayed bool
	body     []byte
}

func newServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	s := testutil.NewStore(t)
	srv := httptest.NewServer(api.New(s, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv, s
}

func postReceipt(t *testing.T, srv *httptest.Server, key string, issueID int64, qty int) resp {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"issue_id": issueID, "qty": qty, "received_on": "2026-10-10"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/jobwork/receipts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("request failed: %v", err)
		return resp{}
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{status: r.StatusCode, replayed: r.Header.Get("Idempotent-Replayed") == "true", body: b}
}

// fireAtOnce runs n calls of fn at the same moment (all goroutines are released
// by one close) and collects the results.
func fireAtOnce(n int, fn func(i int) resp) []resp {
	out := make([]resp, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out[i] = fn(i)
		}()
	}
	close(start)
	wg.Wait()
	return out
}

// Guarantee: received can never exceed issued, however many requests race.
// If the trigger/CHECK is removed (or the app starts checking in memory),
// the totals below exceed the issued quantity and this test fails.
func TestConcurrentReceiptsCannotOvershoot(t *testing.T) {
	cases := []struct {
		name         string
		issued, qty  int
		requests     int
		wantAccepted int
	}{
		{"20 x qty 1 against 10 issued", 10, 1, 20, 10},
		{"10 x qty 3 against 10 issued", 10, 3, 10, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, s := newServer(t)
			issue := testutil.NewIssue(t, s, c.issued, 18000)

			results := fireAtOnce(c.requests, func(i int) resp {
				return postReceipt(t, srv, fmt.Sprintf("key-%d", i), issue, c.qty)
			})

			accepted, rejected := 0, 0
			for _, r := range results {
				switch r.status {
				case http.StatusCreated:
					accepted++
				case http.StatusConflict:
					rejected++
				default:
					t.Errorf("unexpected status %d: %s", r.status, r.body)
				}
			}
			if accepted != c.wantAccepted || rejected != c.requests-c.wantAccepted {
				t.Errorf("accepted=%d rejected=%d, want accepted=%d", accepted, rejected, c.wantAccepted)
			}

			// Ground truth from the tables, not from the responses.
			received := testutil.Count(t, s, `SELECT COALESCE(SUM(qty), 0) FROM jobwork_receipts WHERE issue_id = $1`, issue)
			if received > c.issued {
				t.Fatalf("OVERSHOOT: received %d > issued %d", received, c.issued)
			}
			if received != c.wantAccepted*c.qty {
				t.Errorf("received = %d, want %d", received, c.wantAccepted*c.qty)
			}
			if n := testutil.Count(t, s, `SELECT count(*) FROM vouchers`); n != accepted {
				t.Errorf("vouchers = %d, want one per accepted receipt (%d)", n, accepted)
			}
		})
	}
}

// Guarantee: replaying an Idempotency-Key returns the original result and
// creates nothing. Removing the UNIQUE key / ON CONFLICT path makes the row
// counts below go above 1 and this test fail.
func TestIdempotencyKeyReplayCreatesNothing(t *testing.T) {
	srv, s := newServer(t)
	issue := testutil.NewIssue(t, s, 100, 18000)
	counts := func() (receipts, vouchers int) {
		return testutil.Count(t, s, `SELECT count(*) FROM jobwork_receipts`),
			testutil.Count(t, s, `SELECT count(*) FROM vouchers`)
	}

	t.Run("sequential replays", func(t *testing.T) {
		first := postReceipt(t, srv, "k-seq", issue, 5)
		if first.status != http.StatusCreated || first.replayed {
			t.Fatalf("first call: status %d replayed=%v body=%s", first.status, first.replayed, first.body)
		}
		for i := 0; i < 5; i++ {
			again := postReceipt(t, srv, "k-seq", issue, 5)
			if again.status != http.StatusOK || !again.replayed {
				t.Fatalf("replay %d: status %d replayed=%v", i, again.status, again.replayed)
			}
			if !bytes.Equal(first.body, again.body) {
				t.Fatalf("replay body differs:\n first: %s\n again: %s", first.body, again.body)
			}
		}
		if r, v := counts(); r != 1 || v != 1 {
			t.Fatalf("after 6 identical requests: receipts=%d vouchers=%d, want 1 and 1", r, v)
		}
	})

	t.Run("concurrent duplicates", func(t *testing.T) {
		results := fireAtOnce(12, func(int) resp { return postReceipt(t, srv, "k-conc", issue, 7) })
		created := 0
		for _, r := range results {
			switch r.status {
			case http.StatusCreated:
				created++
			case http.StatusOK:
			default:
				t.Errorf("unexpected status %d: %s", r.status, r.body)
			}
			if !bytes.Equal(r.body, results[0].body) {
				t.Errorf("bodies differ across duplicates")
			}
		}
		if created != 1 {
			t.Errorf("%d requests reported 'created', want exactly 1", created)
		}
		// 1 receipt from the sequential case + 1 from this key.
		if r, v := counts(); r != 2 || v != 2 {
			t.Fatalf("receipts=%d vouchers=%d, want 2 and 2", r, v)
		}
	})

	t.Run("same key, different payload is rejected", func(t *testing.T) {
		r := postReceipt(t, srv, "k-seq", issue, 6)
		if r.status != http.StatusUnprocessableEntity {
			t.Fatalf("status %d, want 422: %s", r.status, r.body)
		}
		if rc, v := counts(); rc != 2 || v != 2 {
			t.Fatalf("receipts=%d vouchers=%d, want 2 and 2", rc, v)
		}
	})

	t.Run("replay of an accepted key still works when the issue is now full", func(t *testing.T) {
		small := testutil.NewIssue(t, s, 4, 100)
		first := postReceipt(t, srv, "k-full", small, 4) // fills the issue
		again := postReceipt(t, srv, "k-full", small, 4)
		if first.status != http.StatusCreated || again.status != http.StatusOK {
			t.Fatalf("first=%d replay=%d, want 201 then 200 (%s)", first.status, again.status, again.body)
		}
	})
}
