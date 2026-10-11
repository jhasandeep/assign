package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"sutra/internal/store"
	"sutra/internal/testutil"
	"sutra/internal/worker"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func makeVouchers(t *testing.T, s *store.Store, n int) {
	t.Helper()
	issue := testutil.NewIssue(t, s, n, 1000)
	for i := 0; i < n; i++ {
		_, err := s.CreateReceipt(context.Background(), store.ReceiptInput{
			IssueID: issue, Qty: 1, ReceivedOn: time.Now(), IdempotencyKey: fmt.Sprintf("k-%d-%d", issue, i),
		})
		if err != nil {
			t.Fatalf("create receipt: %v", err)
		}
	}
}

// Two workers polling the same table must never hold the same voucher.
func TestClaimIsExclusiveAcrossWorkers(t *testing.T) {
	s := testutil.NewStore(t)
	const total = 60
	makeVouchers(t, s, total)

	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				got, err := s.ClaimVouchers(context.Background(), 3, time.Minute)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if len(got) == 0 {
					return
				}
				mu.Lock()
				for _, v := range got {
					seen[v.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != total {
		t.Errorf("claimed %d distinct vouchers, want %d", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("voucher %d claimed %d times", id, n)
		}
	}
}

// The dangerous case: the first call reaches accounting but the worker gives up
// waiting (timeout). The retry must be answered 409 + original external_ref and
// recorded as pushed, with exactly one voucher created on the accounting side.
// The same path covers "worker crashed after the HTTP call".
func TestTimeoutThenRetryIsReconciledThroughDuplicate(t *testing.T) {
	s := testutil.NewStore(t)
	makeVouchers(t, s, 1)

	var mu sync.Mutex
	created := map[string]string{}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ClientRef string `json:"client_ref"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			time.Sleep(600 * time.Millisecond) // longer than the worker's timeout
		}

		mu.Lock()
		ext, dup := created[req.ClientRef]
		if !dup {
			ext = "ACC-1"
			created[req.ClientRef] = ext
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if dup {
			w.WriteHeader(http.StatusConflict)
		} else {
			w.WriteHeader(http.StatusCreated)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"external_ref": ext})
	}))
	defer srv.Close()

	cfg := worker.DefaultConfig()
	cfg.BackoffBase = 10 * time.Millisecond
	cfg.BackoffMax = 20 * time.Millisecond
	w := worker.New(s, worker.NewAccounting(srv.URL, 200*time.Millisecond), cfg, quietLog())
	ctx := context.Background()

	if n, err := w.ProcessOnce(ctx, 5); err != nil || n != 1 {
		t.Fatalf("first pass: n=%d err=%v", n, err)
	}
	v := onlyVoucher(t, s)
	if v.Status != store.StatusPending || v.LastError == nil {
		t.Fatalf("after timeout: status=%s last_error=%v, want pending with an error", v.Status, v.LastError)
	}

	time.Sleep(50 * time.Millisecond) // let the backoff elapse
	if n, err := w.ProcessOnce(ctx, 5); err != nil || n != 1 {
		t.Fatalf("second pass: n=%d err=%v", n, err)
	}
	v = onlyVoucher(t, s)
	if v.Status != store.StatusPushed || v.ExternalRef == nil || *v.ExternalRef != "ACC-1" {
		t.Fatalf("after retry: status=%s external_ref=%v, want pushed ACC-1", v.Status, v.ExternalRef)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 1 {
		t.Errorf("accounting created %d vouchers, want exactly 1", len(created))
	}
}

// A voucher that keeps failing ends 'failed' with the reason, then a manual
// retry puts it back in the queue.
func TestRepeatedFailureEndsFailedAndCanBeRetried(t *testing.T) {
	s := testutil.NewStore(t)
	makeVouchers(t, s, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := worker.DefaultConfig()
	cfg.MaxAttempts = 3
	cfg.BackoffBase = time.Millisecond
	cfg.BackoffMax = 2 * time.Millisecond
	w := worker.New(s, worker.NewAccounting(srv.URL, time.Second), cfg, quietLog())

	for i := 0; i < 3; i++ {
		time.Sleep(10 * time.Millisecond)
		if _, err := w.ProcessOnce(context.Background(), 5); err != nil {
			t.Fatal(err)
		}
	}
	v := onlyVoucher(t, s)
	if v.Status != store.StatusFailed || v.Attempts != 3 || v.LastError == nil {
		t.Fatalf("status=%s attempts=%d last_error=%v, want failed after 3 attempts with a reason", v.Status, v.Attempts, v.LastError)
	}

	retried, err := s.RetryVoucher(context.Background(), v.ID)
	if err != nil || retried.Status != store.StatusPending || retried.Attempts != 0 {
		t.Fatalf("retry: %+v err=%v, want pending with attempts reset", retried, err)
	}
}

func onlyVoucher(t *testing.T, s *store.Store) store.Voucher {
	t.Helper()
	vs, err := s.ListVouchers(context.Background(), "")
	if err != nil || len(vs) != 1 {
		t.Fatalf("list vouchers: %d rows, err=%v", len(vs), err)
	}
	return vs[0]
}
