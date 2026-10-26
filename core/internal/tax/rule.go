// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tax

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// stateCode is the issuing state or province's shape: two letters.
var stateCode = regexp.MustCompile("^[A-Za-z]{2}$")

// StatusOf names the certificate's standing on day, in the words the
// account screen shows: ended (inactive, kept on file), future (its window
// has not begun), expired (its window has closed) or active (it exempts a
// sale that day). The words follow the one window rule ExemptionCovering
// reads, on the same day (the branch's business day).
func StatusOf(ex *TaxExemption, day time.Time) string {
	if ex == nil {
		return ""
	}
	if !ex.IsActive {
		return "ended"
	}
	if sameDay(ex.EffectiveDate, day) == after {
		return "future"
	}
	if ex.ExpiryDate != nil && sameDay(*ex.ExpiryDate, day) == before {
		return "expired"
	}
	return "active"
}

const (
	before = -1
	same   = 0
	after  = 1
)

// sameDay compares two times by calendar day: before, same or after.
func sameDay(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	switch {
	case ay < by || (ay == by && am < bm) || (ay == by && am == bm && ad < bd):
		return before
	case ay == by && am == bm && ad == bd:
		return same
	default:
		return after
	}
}

// ExemptionStanding is why a sale carries no tax: the certificate that
// exempts it, as the counter shows it.
type ExemptionStanding struct {
	CertificateNumber string `json:"certificate_number"`
	// ExpiryDay is the certificate's last day, YYYY-MM-DD; empty when the
	// certificate does not expire.
	ExpiryDay string `json:"expiry_day,omitempty"`
}

// ExemptionCovering reads the customer's certificate that exempts a sale on
// day, through the caller's executor (the pool, or the transaction the
// caller holds). Nil means no certificate exempts that day. When more than
// one covers, the newest is the one; it carries the certificate the counter
// names. This is the one read behind Exemptions: the SQL window and the
// function's rule are the same words.
func ExemptionCovering(ctx context.Context, exec database.Executor, customerID uuid.UUID, day time.Time) (*ExemptionStanding, error) {
	query := `
		SELECT certificate_number, expiry_date
		FROM tax_exemptions
		WHERE customer_id = $1
		  AND is_active
		  AND effective_date <= $2::date
		  AND (expiry_date IS NULL OR expiry_date >= $2::date)
		ORDER BY created_at DESC
		LIMIT 1`
	var standing ExemptionStanding
	var number *string
	var expiry *time.Time
	if err := exec.QueryRow(ctx, query, customerID, day.Format("2006-01-02")).
		Scan(&number, &expiry); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if number != nil {
		standing.CertificateNumber = *number
	}
	if expiry != nil {
		standing.ExpiryDay = expiry.Format("2006-01-02")
	}
	return &standing, nil
}
