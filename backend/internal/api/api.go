// Package api is the HTTP layer: parse, call the store, map errors to statuses.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"sutra/internal/store"
)

type API struct {
	store *store.Store
	log   *slog.Logger
}

func New(s *store.Store, log *slog.Logger) http.Handler {
	a := &API{store: s, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("POST /jobwork/receipts", a.createReceipt)
	mux.HandleFunc("GET /issues/{id}", a.getIssue)
	mux.HandleFunc("GET /vouchers", a.listVouchers)
	mux.HandleFunc("POST /vouchers/{id}/retry", a.retryVoucher)
	return mux
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ping(r.Context()); err != nil {
		a.fail(w, r, http.StatusServiceUnavailable, "database unavailable", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail writes a client-visible message; the underlying error (if any) is logged,
// not leaked.
func (a *API) fail(w http.ResponseWriter, r *http.Request, status int, msg string, cause error) {
	if cause != nil {
		a.log.ErrorContext(r.Context(), msg, "method", r.Method, "path", r.URL.Path, "err", cause)
	}
	writeJSON(w, status, map[string]any{"error": msg})
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("id must be a positive integer")
	}
	return id, nil
}
