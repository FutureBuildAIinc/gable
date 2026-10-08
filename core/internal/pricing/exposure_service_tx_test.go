// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// Acknowledge and RequestAck run their writes in one transaction with the
// outbox event inside it, so a failed event write rolls the acknowledgment
// back with it instead of leaving a committed ack whose notification was
// lost (fix round 1, P2-5). These tests run against Postgres because the
// guarantee is the transaction's, not the fakes'.

// failingRecorder is an EventRecorder whose Write always fails, standing in
// for an events_outbox insert that cannot land.
type failingRecorder struct{ err error }

func (f failingRecorder) Write(context.Context, outbox.Event) error { return f.err }

// seedAckQuote inserts a customer, a SENT quote with an ACK_REQUIRED rollup,
// a line and an active escalator in the ACK_REQUIRED state, and returns the
// ids. Everything is removed again in t.Cleanup.
func seedAckQuote(t *testing.T, db *database.DB) (quoteID, lineID, escalatorID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %s: %v", sql, err)
		}
	}
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	customerID, productID := uuid.New(), uuid.New()
	quoteID, lineID, escalatorID = uuid.New(), uuid.New(), uuid.New()

	exec(`INSERT INTO customers (id, name, account_number, primary_branch_id)
	      VALUES ($1, 'Ack Rollback Co', $2, `+branch+`)`,
		customerID, "ACKRB-"+uuid.NewString()[:8])
	exec(`INSERT INTO products (id, sku, description, uom_primary, base_price)
	      VALUES ($1, $2, 'Ack Rollback Stud', 'EA', 100)`,
		productID, "ACKRB-"+uuid.NewString()[:8])
	exec(`INSERT INTO quotes (id, customer_id, project_id, state, total_amount, source,
	                          customer_notes, branch_id, exposure_state, created_at, updated_at)
	      VALUES ($1, $2, NULL, 'SENT', 500.00, 'manual', 'rollback fixture', `+branch+`,
	              'ACK_REQUIRED', NOW(), NOW())`, quoteID, customerID)
	exec(`INSERT INTO quote_lines (id, quote_id, product_id, sku, description, customer_note,
	                              quantity, uom, unit_price, line_total)
	      VALUES ($1, $2, $3, 'ACKRB', 'rollback line', 'none', 5, 'EA', 100.00, 500.00)`,
		lineID, quoteID, productID)
	exec(`INSERT INTO price_escalators (id, quote_line_id, escalation_type, escalation_rate,
	                                   base_price, effective_date, expiration_date, current_state, is_active)
	      VALUES ($1, $2, 'INDEX_DELTA', 0, 100.00, CURRENT_DATE, CURRENT_DATE + 365,
	              'ACK_REQUIRED', TRUE)`, escalatorID, lineID)

	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quote_exposure_events WHERE quote_id = $1`, quoteID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM price_escalators WHERE id = $1`, escalatorID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quote_lines WHERE id = $1`, lineID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE id = $1`, quoteID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
	})
	return quoteID, lineID, escalatorID
}

// ackRollbackCounts reads the three writes Acknowledge makes for the quote:
// the ledger rows, the escalator state and the quote's rollup.
func ackRollbackCounts(t *testing.T, db *database.DB, quoteID, escalatorID uuid.UUID) (events int, escState, quoteState string) {
	t.Helper()
	ctx := context.Background()
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM quote_exposure_events WHERE quote_id = $1`, quoteID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT current_state FROM price_escalators WHERE id = $1`, escalatorID).Scan(&escState); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT exposure_state FROM quotes WHERE id = $1`, quoteID).Scan(&quoteState); err != nil {
		t.Fatal(err)
	}
	return events, escState, quoteState
}

// A failed outbox write rolls the whole acknowledgment back: no ledger row,
// no rollup flip, no escalator flip, and the caller sees the error rather
// than a 500 for an ack that actually committed.
func TestAcknowledge_FailedOutboxWriteRollsBack(t *testing.T) {
	db := testutil.RequireDB(t)
	quoteID, _, escalatorID := seedAckQuote(t, db)
	ctx := context.Background()

	exposure := NewExposureRepository(db)
	checker := &fakeChecker{status: ExposureStatus{
		State: ExposureStateAckRequired, ExposureDollars: 500,
	}}
	quotes := quote.NewRepository(db)
	svc := NewExposureService(exposure, newMockEscalatorRepo(), quotes, nil, checker, nil).
		WithOutbox(failingRecorder{err: errors.New("outbox insert failed")}).
		WithTxRunner(db)

	if _, err := svc.Acknowledge(ctx, quoteID,
		AcknowledgmentRequest{Method: AckMethodEmail, Notes: "customer confirmed over email"},
		"user-1", "sales"); err == nil || !strings.Contains(err.Error(), "outbox insert failed") {
		t.Fatalf("Acknowledge error = %v, want the outbox write's failure", err)
	}

	events, escState, quoteState := ackRollbackCounts(t, db, quoteID, escalatorID)
	if events != 0 {
		t.Errorf("ledger rows = %d after a rolled back ack, want 0", events)
	}
	if escState != string(ExposureStateAckRequired) {
		t.Errorf("escalator state = %s, want still ACK_REQUIRED", escState)
	}
	if quoteState != string(ExposureStateAckRequired) {
		t.Errorf("quote rollup = %s, want still ACK_REQUIRED", quoteState)
	}
}

// The happy path commits all three writes together with the outbox event.
func TestAcknowledge_CommitsEventAndRollupTogether(t *testing.T) {
	db := testutil.RequireDB(t)
	quoteID, _, escalatorID := seedAckQuote(t, db)
	ctx := context.Background()

	exposure := NewExposureRepository(db)
	checker := &fakeChecker{status: ExposureStatus{
		State: ExposureStateAckRequired, ExposureDollars: 500,
	}}
	quotes := quote.NewRepository(db)
	svc := NewExposureService(exposure, newMockEscalatorRepo(), quotes, nil, checker, nil).
		WithOutbox(outbox.NewWriter(db, "default")).
		WithTxRunner(db)

	if _, err := svc.Acknowledge(ctx, quoteID,
		AcknowledgmentRequest{Method: AckMethodEmail, Notes: "customer confirmed over email"},
		"user-1", "sales"); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	events, escState, quoteState := ackRollbackCounts(t, db, quoteID, escalatorID)
	if events != 1 {
		t.Errorf("ledger rows = %d, want 1", events)
	}
	if escState != string(ExposureStateAcknowledged) {
		t.Errorf("escalator state = %s, want ACKNOWLEDGED", escState)
	}
	if quoteState != string(ExposureStateAcknowledged) {
		t.Errorf("quote rollup = %s, want ACKNOWLEDGED", quoteState)
	}
	var outboxRows int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = 'quote.exposure.acknowledged'`,
		quoteID).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if outboxRows != 1 {
		t.Errorf("outbox rows = %d, want the one notification event", outboxRows)
	}
}
