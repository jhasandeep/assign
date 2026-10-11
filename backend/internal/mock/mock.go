// Package mock is a deliberately unreliable stand-in for the accounting API.
package mock

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

type Config struct {
	FailRate float64       // fraction of calls answered with HTTP 500 (nothing recorded)
	HangRate float64       // fraction of calls that sleep HangFor, then proceed normally
	HangFor  time.Duration // 5s in the brief
}

func DefaultConfig() Config {
	return Config{FailRate: 0.20, HangRate: 0.10, HangFor: 5 * time.Second}
}

type Server struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	byRef   map[string]string // client_ref -> external_ref
	counter int
}

func New(cfg Config, log *slog.Logger) *Server {
	return &Server{cfg: cfg, log: log, byRef: map[string]string{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/vouchers", s.postVoucher)
	return mux
}

type voucherRequest struct {
	ClientRef   string `json:"client_ref"`
	Type        string `json:"type"`
	SourceID    int64  `json:"source_id"`
	AmountPaise int64  `json:"amount_paise"`
}

func (s *Server) postVoucher(w http.ResponseWriter, r *http.Request) {
	roll := rand.Float64()
	switch {
	case roll < s.cfg.FailRate:
		s.log.Info("injected 500")
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "accounting system unavailable"})
		return
	case roll < s.cfg.FailRate+s.cfg.HangRate:
		s.log.Info("injected hang", "for", s.cfg.HangFor)
		// Deliberately ignores r.Context(): if the client gives up, the request
		// still lands afterwards, which is the dangerous real-world case.
		time.Sleep(s.cfg.HangFor)
	}

	var req voucherRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.ClientRef == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "client_ref is required"})
		return
	}

	// Check-and-insert under one lock so concurrent duplicates cannot both win.
	s.mu.Lock()
	ext, dup := s.byRef[req.ClientRef]
	if !dup {
		s.counter++
		ext = fmt.Sprintf("ACC-%06d", s.counter)
		s.byRef[req.ClientRef] = ext
	}
	s.mu.Unlock()

	if dup {
		s.log.Info("duplicate client_ref", "client_ref", req.ClientRef, "external_ref", ext)
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "duplicate client_ref", "external_ref": ext,
		})
		return
	}
	s.log.Info("voucher accepted", "client_ref", req.ClientRef, "external_ref", ext, "amount_paise", req.AmountPaise)
	writeJSON(w, http.StatusCreated, map[string]any{"external_ref": ext})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
