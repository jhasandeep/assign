package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"sutra/internal/store"
)

type receiptRequest struct {
	IssueID    int64  `json:"issue_id"`
	Qty        int    `json:"qty"`
	ReceivedOn string `json:"received_on"` // YYYY-MM-DD
}

const maxIdempotencyKeyLen = 200

func (a *API) createReceipt(w http.ResponseWriter, r *http.Request) {
	var req receiptRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		a.fail(w, r, http.StatusBadRequest, "invalid JSON body: "+err.Error(), nil)
		return
	}
	if req.IssueID <= 0 {
		a.fail(w, r, http.StatusBadRequest, "issue_id must be a positive integer", nil)
		return
	}
	if req.Qty <= 0 {
		a.fail(w, r, http.StatusBadRequest, "qty must be greater than zero", nil)
		return
	}
	receivedOn, err := time.Parse(time.DateOnly, req.ReceivedOn)
	if err != nil {
		a.fail(w, r, http.StatusBadRequest, "received_on must be a date like 2026-10-12", nil)
		return
	}

	// Without a key the request still works, it just can't be safely replayed.
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > maxIdempotencyKeyLen {
		a.fail(w, r, http.StatusBadRequest, "Idempotency-Key is too long", nil)
		return
	}
	if key == "" {
		key = "auto-" + uuid.NewString()
	}

	res, err := a.store.CreateReceipt(r.Context(), store.ReceiptInput{
		IssueID: req.IssueID, Qty: req.Qty, ReceivedOn: receivedOn, IdempotencyKey: key,
	})

	var over *store.OvershootError
	switch {
	case errors.As(err, &over):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": over.Error(), "outstanding": over.Outstanding,
		})
	case errors.Is(err, store.ErrIssueNotFound):
		a.fail(w, r, http.StatusNotFound, "issue not found", nil)
	case errors.Is(err, store.ErrKeyReuse):
		a.fail(w, r, http.StatusUnprocessableEntity, err.Error(), nil)
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError, "could not record receipt", err)
	case res.Replayed:
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, res)
	default:
		writeJSON(w, http.StatusCreated, res)
	}
}
