package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"sutra/internal/store"
)

// Outcome classifies one push attempt.
type Outcome int

const (
	// Pushed: the accounting system has the voucher (created now, or already
	// there and reported via 409). ExternalRef is set.
	Pushed Outcome = iota
	// Retryable: timeout, network error, 5xx, 429. Try again after backoff.
	Retryable
	// Permanent: the accounting system rejected the voucher (other 4xx).
	Permanent
)

type Result struct {
	Outcome     Outcome
	ExternalRef string
	Reason      string
}

// Accounting talks to the accounting API. The http.Client timeout bounds every
// call, so a hanging endpoint can hold up at most one goroutine for that long.
type Accounting struct {
	baseURL string
	client  *http.Client
}

func NewAccounting(baseURL string, timeout time.Duration) *Accounting {
	return &Accounting{baseURL: baseURL, client: &http.Client{Timeout: timeout}}
}

type pushBody struct {
	ClientRef   string `json:"client_ref"`
	Type        string `json:"type"`
	SourceID    int64  `json:"source_id"`
	AmountPaise int64  `json:"amount_paise"`
}

type pushReply struct {
	ExternalRef string `json:"external_ref"`
}

// Push posts the voucher with its stable client_ref. Because client_ref never
// changes across attempts, re-sending after a timeout or a crash is safe: if
// the first call did land, the accounting system answers 409 with the original
// external_ref and we treat that as success.
func (a *Accounting) Push(ctx context.Context, v store.ClaimedVoucher) Result {
	payload, err := json.Marshal(pushBody{
		ClientRef: v.ClientRef, Type: v.Type, SourceID: v.SourceID, AmountPaise: v.AmountPaise,
	})
	if err != nil {
		return Result{Outcome: Permanent, Reason: "encode request: " + err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/api/vouchers", bytes.NewReader(payload))
	if err != nil {
		return Result{Outcome: Permanent, Reason: "build request: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return Result{Outcome: Retryable, Reason: "request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusConflict:
		var reply pushReply
		if err := json.Unmarshal(body, &reply); err != nil || reply.ExternalRef == "" {
			// Accepted but we cannot learn the reference: retry; the 409 path
			// will hand it to us next time.
			return Result{Outcome: Retryable, Reason: fmt.Sprintf("HTTP %d without external_ref", resp.StatusCode)}
		}
		return Result{Outcome: Pushed, ExternalRef: reply.ExternalRef}
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout:
		return Result{Outcome: Retryable, Reason: fmt.Sprintf("accounting returned HTTP %d", resp.StatusCode)}
	default:
		return Result{Outcome: Permanent, Reason: fmt.Sprintf("accounting rejected voucher: HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
