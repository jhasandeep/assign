package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	StatusPending = "pending"
	StatusPushed  = "pushed"
	StatusFailed  = "failed"
)

func ValidStatus(s string) bool {
	return s == StatusPending || s == StatusPushed || s == StatusFailed
}

type Voucher struct {
	ID          int64     `json:"id"`
	Type        string    `json:"type"`
	SourceID    int64     `json:"source_id"`
	AmountPaise int64     `json:"amount_paise"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	LastError   *string   `json:"last_error"`
	ClientRef   string    `json:"client_ref"`
	ExternalRef *string   `json:"external_ref"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const voucherCols = `id, type, source_id, amount_paise, status, attempts, last_error,
	client_ref, external_ref, created_at, updated_at`

func scanVoucher(row pgx.Row) (Voucher, error) {
	var v Voucher
	err := row.Scan(&v.ID, &v.Type, &v.SourceID, &v.AmountPaise, &v.Status, &v.Attempts,
		&v.LastError, &v.ClientRef, &v.ExternalRef, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// ListVouchers returns vouchers, newest first. An empty status means all.
func (s *Store) ListVouchers(ctx context.Context, status string) ([]Voucher, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+voucherCols+` FROM vouchers
		 WHERE $1 = '' OR status = $1
		 ORDER BY id DESC LIMIT 500`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Voucher{}
	for rows.Next() {
		v, err := scanVoucher(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetVoucher(ctx context.Context, id int64) (Voucher, error) {
	v, err := scanVoucher(s.pool.QueryRow(ctx, `SELECT `+voucherCols+` FROM vouchers WHERE id = $1`, id))
	return v, notFoundOr(err)
}

var ErrAlreadyPushed = errors.New("voucher is already pushed")

// RetryVoucher puts a failed voucher back in the queue with a fresh attempt
// budget. A pending voucher is returned as-is (retrying twice is harmless);
// a pushed one is refused, since pushing it again could double-post.
func (s *Store) RetryVoucher(ctx context.Context, id int64) (Voucher, error) {
	v, err := scanVoucher(s.pool.QueryRow(ctx, `
		UPDATE vouchers
		   SET status = 'pending', attempts = 0, next_attempt_at = now(),
		       locked_until = NULL, updated_at = now()
		 WHERE id = $1 AND status = 'failed'
		RETURNING `+voucherCols, id))
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Voucher{}, err
	}
	cur, err := s.GetVoucher(ctx, id)
	if err != nil {
		return Voucher{}, err
	}
	if cur.Status == StatusPushed {
		return cur, ErrAlreadyPushed
	}
	return cur, nil
}

// ClaimedVoucher is what a worker needs to make the HTTP call.
type ClaimedVoucher struct {
	ID          int64
	ClientRef   string
	Type        string
	SourceID    int64
	AmountPaise int64
	Attempts    int // including this one
}

// ClaimVouchers atomically leases up to limit due vouchers and counts the
// attempt. FOR UPDATE SKIP LOCKED means concurrent workers get disjoint rows;
// the lease (locked_until) keeps a voucher claimed after this statement
// commits, and expires on its own if the worker dies.
func (s *Store) ClaimVouchers(ctx context.Context, limit int, lease time.Duration) ([]ClaimedVoucher, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE vouchers
		   SET attempts = attempts + 1,
		       locked_until = now() + make_interval(secs => $2),
		       updated_at = now()
		 WHERE id IN (
		       SELECT id FROM vouchers
		        WHERE status = 'pending'
		          AND next_attempt_at <= now()
		          AND (locked_until IS NULL OR locked_until <= now())
		        ORDER BY next_attempt_at, id
		        LIMIT $1
		        FOR UPDATE SKIP LOCKED)
		RETURNING id, client_ref, type, source_id, amount_paise, attempts`,
		limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ClaimedVoucher
	for rows.Next() {
		var c ClaimedVoucher
		if err := rows.Scan(&c.ID, &c.ClientRef, &c.Type, &c.SourceID, &c.AmountPaise, &c.Attempts); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkPushed records success. Guarded by status = 'pending' so a stale worker
// cannot overwrite a later state.
func (s *Store) MarkPushed(ctx context.Context, id int64, externalRef string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE vouchers
		   SET status = 'pushed', external_ref = $2, last_error = NULL,
		       locked_until = NULL, updated_at = now()
		 WHERE id = $1 AND status = 'pending'`, id, externalRef)
	return err
}

// MarkAttemptFailed records a failed attempt. If final, the voucher becomes
// failed (needs a human Retry); otherwise it stays pending and is retried
// after backoff.
func (s *Store) MarkAttemptFailed(ctx context.Context, id int64, reason string, final bool, backoff time.Duration) error {
	status := StatusPending
	if final {
		status = StatusFailed
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE vouchers
		   SET status = $2, last_error = $3,
		       next_attempt_at = now() + make_interval(secs => $4),
		       locked_until = NULL, updated_at = now()
		 WHERE id = $1 AND status = 'pending'`, id, status, reason, backoff.Seconds())
	if err != nil {
		return fmt.Errorf("mark attempt failed: %w", err)
	}
	return nil
}
