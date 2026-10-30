// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// cleanStatementRows removes what a statement test wrote about one crossing.
func cleanStatementRows(t *testing.T, db *database.DB, eventID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action IN ('large_cash.statement.issued', 'large_cash.filing.recorded') AND entity_id = $1`, eventID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM large_cash_statements WHERE payer_key IN (SELECT payer_key FROM large_cash_events WHERE id = $1)`, eventID)
	})
}

// moveCrossing puts a crossing, and the tenders that made it, at an instant.
func moveCrossing(t *testing.T, db *database.DB, eventID uuid.UUID, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `UPDATE large_cash_events SET created_at = $2 WHERE id = $1`, eventID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE large_cash_tenders SET created_at = $2
		WHERE transaction_id IN (SELECT unnest(transaction_ids) FROM large_cash_events WHERE id = $1)`, eventID, at); err != nil {
		t.Fatal(err)
	}
}

// addEarlierCash writes one more counted tender for the crossing's payer, on
// a sale of its own at the instant given, as an earlier receipt would be.
func addEarlierCash(t *testing.T, db *database.DB, eventID uuid.UUID, cents int64, at time.Time) {
	t.Helper()
	ctx := context.Background()
	var payer string
	var branch uuid.UUID
	var reg string
	if err := db.Pool.QueryRow(ctx, `SELECT e.payer_key, e.branch_id, (SELECT register_id FROM pos_transactions WHERE id = e.transaction_ids[1])
		FROM large_cash_events e WHERE e.id = $1`, eventID).Scan(&payer, &branch, &reg); err != nil {
		t.Fatal(err)
	}
	sale := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, status, currency) VALUES ($1, $2, $3, $4, 'COMPLETED', 'USD')`,
		sale, reg, uuid.New(), branch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO large_cash_tenders (transaction_id, branch_id, rule_family, payer_key, counted_cents, reason, tender_index, created_at)
		VALUES ($1, $2, 'US_8300', $3, $4, 'cash', 0, $5)`, sale, branch, payer, cents, at); err != nil {
		t.Fatal(err)
	}
}

// TestTheStatementNeedsAFiledCrossing: the statement says the information
// was furnished to the IRS, so a crossing with no recorded filing is refused
// and nothing is written.
func TestTheStatementNeedsAFiledCrossing(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := largecash.NewService(db, nil, "")
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	cleanStatementRows(t, db, eventID)
	if _, err := svc.IssueStatement(ctx, largecash.IssueStatementIn{EventID: eventID, Actor: "manager-a"}); !errors.Is(err, largecash.ErrStatementNotFiled) {
		t.Fatalf("a statement for an unfiled crossing answered %v, want ErrStatementNotFiled", err)
	}
	var rows, audits int
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM large_cash_statements WHERE payer_key IN (SELECT payer_key FROM large_cash_events WHERE id = $1)`, eventID).Scan(&rows)
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'large_cash.statement.issued' AND entity_id = $1`, eventID).Scan(&audits)
	if rows != 0 || audits != 0 {
		t.Fatalf("a refused statement left %d statement rows and %d audit rows", rows, audits)
	}
	// A crossing that is only the dealer's record is not a report: it has no
	// filing, so no statement.
	if _, err := db.Pool.Exec(ctx, `UPDATE large_cash_events SET state = 'recorded' WHERE id = $1`, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.IssueStatement(ctx, largecash.IssueStatementIn{EventID: eventID, Actor: "manager-a"}); !errors.Is(err, largecash.ErrStatementNotFiled) {
		t.Fatalf("a statement for a recorded crossing answered %v, want ErrStatementNotFiled", err)
	}
}

// TestAVoluntaryFilingIsRefusedOnAReportableCrossing: every reportable
// crossing is over the threshold, so its filing is required and cannot be
// recorded as voluntary.
func TestAVoluntaryFilingIsRefusedOnAReportableCrossing(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := largecash.NewService(db, nil, "")
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	cleanStatementRows(t, db, eventID)
	if _, err := svc.RecordFiling(ctx, eventID, largecash.RecordFilingIn{Voluntary: true, Actor: "manager-a"}); !errors.Is(err, largecash.ErrFilingVoluntaryRefused) {
		t.Fatalf("a voluntary filing on a reportable crossing answered %v, want ErrFilingVoluntaryRefused", err)
	}
	var filed int
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM large_cash_filings WHERE event_id = $1 AND state <> 'not_filed'`, eventID).Scan(&filed)
	if filed != 0 {
		t.Fatalf("a refused voluntary filing left %d filed rows", filed)
	}
}

// TestTheStatementCarriesTheDealersContactAndTheAggregateOfTheYear: the
// business block is the dealer's information contact (the receiving branch
// and the dealer profile), the payer is the addressee, and the aggregate is
// the payer's counted cash received in the calendar year in the branch's own
// zone, not the crossing's running total.
func TestTheStatementCarriesTheDealersContactAndTheAggregateOfTheYear(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := largecash.NewService(db, nil, "")
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US", name: "Yard Contact Branch",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	cleanStatementRows(t, db, eventID)
	var branch uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT branch_id FROM large_cash_events WHERE id = $1`, eventID).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET address = '9 Dealer Road', city = 'Kingston', state = 'ON', zip = 'K7L 1A1', phone = '555-0100', timezone = 'America/Toronto' WHERE id = $1`, branch); err != nil {
		t.Fatal(err)
	}
	// The crossing is the second receipt of the payer: an earlier one in the
	// last calendar year carried the running total over the threshold with it.
	// The event's running total is 12,000 dollars; the year's own cash is 6,000.
	if _, err := db.Pool.Exec(ctx, `UPDATE large_cash_events SET total_cents = 1200000, amount_cents = 600000 WHERE id = $1`, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE large_cash_tenders SET counted_cents = 600000 WHERE transaction_id IN (SELECT unnest(transaction_ids) FROM large_cash_events WHERE id = $1)`, eventID); err != nil {
		t.Fatal(err)
	}
	moveCrossing(t, db, eventID, time.Date(2031, 1, 10, 15, 0, 0, 0, time.UTC))
	addEarlierCash(t, db, eventID, 600_000, time.Date(2030, 12, 20, 15, 0, 0, 0, time.UTC))
	if _, err := db.Pool.Exec(ctx, `INSERT INTO large_cash_filings (event_id, state, filed_date, acknowledgement_no, recorded_by)
		VALUES ($1, 'filed', CURRENT_DATE, 'ACK-CONTACT', 'manager-a')
		ON CONFLICT DO NOTHING`, eventID); err != nil {
		t.Fatal(err)
	}
	st, err := svc.IssueStatement(ctx, largecash.IssueStatementIn{EventID: eventID, Actor: "manager-a"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Year != 2031 {
		t.Fatalf("the statement year = %d, want 2031", st.Year)
	}
	if st.AggregateCents != 600_000 {
		t.Fatalf("the statement's aggregate = %d, want the year's own 600000 counted cash, not the running total", st.AggregateCents)
	}
	if st.PayerName != "River Payer" || st.PayerAddress != "1 Test Road" {
		t.Fatalf("the addressee = %q at %q, want the payer", st.PayerName, st.PayerAddress)
	}
	if !strings.Contains(st.BusinessAddress, "9 Dealer Road") || !strings.Contains(st.BusinessAddress, "Kingston") {
		t.Fatalf("the business address = %q, want the receiving branch's address", st.BusinessAddress)
	}
	if st.Telephone != "555-0100" {
		t.Fatalf("the business telephone = %q, want the receiving branch's", st.Telephone)
	}
	if st.BusinessName == "" || st.BusinessName == st.PayerName || st.ContactName == "" {
		t.Fatalf("the business block = %q / %q, want the dealer's name and a contact", st.BusinessName, st.ContactName)
	}
	// The year's list reads the same figure.
	list, err := svc.Statements(ctx, 2031)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list.Items {
		if row.PayerKey == st.PayerKey && row.AggregateCents != 600_000 {
			t.Fatalf("the year's list aggregate = %d, want 600000", row.AggregateCents)
		}
	}
}

// TestTheStatementYearIsTheBranchesOwn: a crossing late on New Year's Eve in
// the branch's zone belongs to that year's statement, though it is the next
// year in UTC.
func TestTheStatementYearIsTheBranchesOwn(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := largecash.NewService(db, nil, "")
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	cleanStatementRows(t, db, eventID)
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET timezone = 'America/Toronto' WHERE id = (SELECT branch_id FROM large_cash_events WHERE id = $1)`, eventID); err != nil {
		t.Fatal(err)
	}
	// 23:30 on 31 December in Toronto is 04:30 on 1 January in UTC.
	moveCrossing(t, db, eventID, time.Date(2032, 1, 1, 4, 30, 0, 0, time.UTC))
	if _, err := db.Pool.Exec(ctx, `INSERT INTO large_cash_filings (event_id, state, filed_date, acknowledgement_no, recorded_by)
		VALUES ($1, 'filed', CURRENT_DATE, 'ACK-YEAR', 'manager-a') ON CONFLICT DO NOTHING`, eventID); err != nil {
		t.Fatal(err)
	}
	st, err := svc.IssueStatement(ctx, largecash.IssueStatementIn{EventID: eventID, Actor: "manager-a"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Year != 2031 {
		t.Fatalf("the statement year = %d, want 2031 (the branch's own year)", st.Year)
	}
}

// TestThreeManagersIssuingTheSameStatementHaveOneWinner: pool size 4, three
// contenders on the real service. One issues and the other two are told it
// is issued; one audit row, and the stored issuer is the winner's.
func TestThreeManagersIssuingTheSameStatementHaveOneWinner(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	svc := largecash.NewService(db, nil, "")
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	cleanStatementRows(t, db, eventID)
	if _, err := svc.RecordFiling(ctx, eventID, largecash.RecordFilingIn{AcknowledgementNo: "ACK-RACE", Actor: "manager-a"}); err != nil {
		t.Fatal(err)
	}
	var start, wg sync.WaitGroup
	start.Add(1)
	errs := make([]error, 3)
	actors := []string{"manager-one", "manager-two", "manager-three"}
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			_, errs[i] = svc.IssueStatement(ctx, largecash.IssueStatementIn{EventID: eventID, Actor: actors[i]})
		}(i)
	}
	start.Done()
	wg.Wait()
	winner, conflicts := -1, 0
	for i, err := range errs {
		switch {
		case err == nil:
			if winner >= 0 {
				t.Fatalf("two contenders issued the one statement")
			}
			winner = i
		case errors.Is(err, largecash.ErrStatementAlreadyIssued):
			conflicts++
		default:
			t.Fatalf("a contender answered %v, want an issue or a conflict", err)
		}
	}
	if winner < 0 || conflicts != 2 {
		t.Fatalf("the contenders gave winner %d and %d conflicts, want one winner and two", winner, conflicts)
	}
	var issuer string
	var audits int
	_ = db.Pool.QueryRow(ctx, `SELECT st.issued_by FROM large_cash_statements st JOIN large_cash_events e ON e.payer_key = st.payer_key WHERE e.id = $1`, eventID).Scan(&issuer)
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'large_cash.statement.issued' AND entity_id = $1`, eventID).Scan(&audits)
	if issuer != actors[winner] || audits != 1 {
		t.Fatalf("stored issuer %q and %d audit rows, want %q and one", issuer, audits, actors[winner])
	}
}

// TestAVoluntaryFilingRowIsStillNeverStated: a voluntary row can only exist
// by a write outside the service now; the statement still refuses it.
func TestAVoluntaryFilingRowIsStillNeverStated(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := largecash.NewService(db, nil, "")
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	cleanStatementRows(t, db, eventID)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO large_cash_filings (event_id, state, filed_date, recorded_by) VALUES ($1, 'voluntary', CURRENT_DATE, 'manager-a')`, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.IssueStatement(ctx, largecash.IssueStatementIn{EventID: eventID, Actor: "manager-a"}); !errors.Is(err, largecash.ErrStatementRefusesVoluntary) {
		t.Fatalf("a voluntary row answered %v, want ErrStatementRefusesVoluntary", err)
	}
}
