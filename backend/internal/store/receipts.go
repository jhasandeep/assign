package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type ReceiptInput struct {
	IssueID        int64
	Qty            int
	ReceivedOn     time.Time
	IdempotencyKey string
}

type Receipt struct {
	ID         int64  `json:"id"`
	IssueID    int64  `json:"issue_id"`
	Qty        int    `json:"qty"`
	ReceivedOn string `json:"received_on"`
}

// VoucherRef is the immutable part of a voucher, so a replayed request returns
// byte-for-byte the same body as the original (status etc. live on GET /vouchers).
type VoucherRef struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	AmountPaise int64  `json:"amount_paise"`
	ClientRef   string `json:"client_ref"`
}

type ReceiptResult struct {
	Receipt  Receipt    `json:"receipt"`
	Voucher  VoucherRef `json:"voucher"`
	Replayed bool       `json:"-"`
}

var (
	ErrIssueNotFound = errors.New("issue not found")
	// ErrKeyReuse: the Idempotency-Key was seen before with a different payload.
	ErrKeyReuse = errors.New("idempotency key was used with a different request")
)

// OvershootError means the receipt would push received above issued.
type OvershootError struct {
	Outstanding int
}

func (e *OvershootError) Error() string {
	return fmt.Sprintf("receipt exceeds outstanding quantity (%d outstanding)", e.Outstanding)
}

func requestHash(in ReceiptInput) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%d|%s", in.IssueID, in.Qty, in.ReceivedOn.Format(time.DateOnly))))
	return hex.EncodeToString(sum[:])
}

// CreateReceipt records a receipt and its voucher in one transaction.
//
//   - Overshoot: the receipts trigger bumps jobwork_issues.received_qty; the
//     received_within_issued CHECK rejects the write that would exceed qty. The
//     UPDATE row-locks the issue, so concurrent receipts queue up behind it.
//   - Idempotency: the receipt insert claims idempotency_key via the UNIQUE
//     index. A concurrent duplicate blocks on that index until the first
//     transaction commits, then takes the ON CONFLICT path and replays it.
func (s *Store) CreateReceipt(ctx context.Context, in ReceiptInput) (ReceiptResult, error) {
	hash := requestHash(in)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReceiptResult{}, err
	}
	defer tx.Rollback(ctx)

	var res ReceiptResult
	err = tx.QueryRow(ctx, `
		INSERT INTO jobwork_receipts (issue_id, qty, received_on, idempotency_key, request_hash)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id, issue_id, qty, received_on::text`,
		in.IssueID, in.Qty, in.ReceivedOn, in.IdempotencyKey, hash,
	).Scan(&res.Receipt.ID, &res.Receipt.IssueID, &res.Receipt.Qty, &res.Receipt.ReceivedOn)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Key already used: the winning transaction has committed by now.
		_ = tx.Rollback(ctx)
		return s.replay(ctx, in.IdempotencyKey, hash)
	case err != nil:
		return ReceiptResult{}, s.mapReceiptErr(ctx, in.IssueID, err)
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO vouchers (type, source_id, amount_paise)
		SELECT 'jobwork_receipt', $1, $2::bigint * rate_paise
		  FROM jobwork_issues WHERE id = $3
		RETURNING id, type, amount_paise, client_ref`,
		res.Receipt.ID, in.Qty, in.IssueID,
	).Scan(&res.Voucher.ID, &res.Voucher.Type, &res.Voucher.AmountPaise, &res.Voucher.ClientRef)
	if err != nil {
		return ReceiptResult{}, fmt.Errorf("create voucher: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ReceiptResult{}, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

func (s *Store) replay(ctx context.Context, key, hash string) (ReceiptResult, error) {
	var res ReceiptResult
	var storedHash string
	err := s.pool.QueryRow(ctx, `
		SELECT r.id, r.issue_id, r.qty, r.received_on::text, r.request_hash,
		       v.id, v.type, v.amount_paise, v.client_ref
		  FROM jobwork_receipts r
		  JOIN vouchers v ON v.type = 'jobwork_receipt' AND v.source_id = r.id
		 WHERE r.idempotency_key = $1`, key,
	).Scan(&res.Receipt.ID, &res.Receipt.IssueID, &res.Receipt.Qty, &res.Receipt.ReceivedOn, &storedHash,
		&res.Voucher.ID, &res.Voucher.Type, &res.Voucher.AmountPaise, &res.Voucher.ClientRef)
	if err != nil {
		return ReceiptResult{}, fmt.Errorf("load replayed receipt: %w", err)
	}
	if storedHash != hash {
		return ReceiptResult{}, ErrKeyReuse
	}
	res.Replayed = true
	return res, nil
}

func (s *Store) mapReceiptErr(ctx context.Context, issueID int64, err error) error {
	pgErr := pgError(err)
	if pgErr == nil {
		return fmt.Errorf("insert receipt: %w", err)
	}
	switch {
	case pgErr.Code == pgForeignKeyViolation:
		return ErrIssueNotFound
	case pgErr.Code == pgCheckViolation && pgErr.ConstraintName == "received_within_issued":
		var outstanding int
		// The failed transaction is gone; read the committed state for the message.
		if qerr := s.pool.QueryRow(ctx,
			`SELECT qty - received_qty FROM jobwork_issues WHERE id = $1`, issueID).Scan(&outstanding); qerr != nil {
			return &OvershootError{Outstanding: -1}
		}
		return &OvershootError{Outstanding: outstanding}
	}
	return fmt.Errorf("insert receipt: %w", err)
}
