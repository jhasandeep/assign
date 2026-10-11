package store

import "context"

type Issue struct {
	ID          int64  `json:"id"`
	PartyID     int64  `json:"party_id"`
	Party       string `json:"party"`
	SKUID       int64  `json:"sku_id"`
	SKUCode     string `json:"sku_code"`
	IssuedOn    string `json:"issued_on"`
	Issued      int    `json:"issued"`
	Received    int    `json:"received"`
	Outstanding int    `json:"outstanding"`
}

// GetIssue derives "received" from the receipts themselves (not the running
// counter), so the number a caller sees is the ground truth.
func (s *Store) GetIssue(ctx context.Context, id int64) (Issue, error) {
	var i Issue
	err := s.pool.QueryRow(ctx, `
		SELECT i.id, i.party_id, p.name, i.sku_id, s.code, i.issued_on::text, i.qty,
		       COALESCE((SELECT SUM(r.qty) FROM jobwork_receipts r WHERE r.issue_id = i.id), 0)::int
		  FROM jobwork_issues i
		  JOIN parties p ON p.id = i.party_id
		  JOIN skus s    ON s.id = i.sku_id
		 WHERE i.id = $1`, id,
	).Scan(&i.ID, &i.PartyID, &i.Party, &i.SKUID, &i.SKUCode, &i.IssuedOn, &i.Issued, &i.Received)
	if err != nil {
		return Issue{}, notFoundOr(err)
	}
	i.Outstanding = i.Issued - i.Received
	return i, nil
}
