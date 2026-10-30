// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"testing"
	"time"

	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// The taxpayer identification number the fixture captures. The export carries
// it in full; nothing else may.
const fixtureTIN = "12-3456789"

// crossingOptions are what a fixture crossing is made of.
type crossingOptions struct {
	country    string
	name       string
	cashCents  int64
	payer      *largecash.PayerIn
	declined   bool
	customerID *uuid.UUID
}

// usPayer is the captured identity of a United States walk-in who paid cash.
func usPayer() *largecash.PayerIn {
	return &largecash.PayerIn{Name: "River Payer", Address: "1 Test Road", Occupation: "builder",
		DateOfBirth: "1980-01-02", IDType: "passport", IDNumber: "RIVER-ID-0001", TaxpayerID: fixtureTIN}
}

// seedCrossing writes one crossing the test owns: the branch, the sale and
// the event the capture wrote.
func seedCrossing(t *testing.T, db *database.DB, svc *largecash.Service, opts crossingOptions) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	branch, yard, reg, sale := uuid.New(), uuid.New(), "LC-"+uuid.NewString()[:8], uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	name := opts.name
	if name == "" {
		name = "worklist fixture"
	}
	exec(`INSERT INTO locations (id, type, code, name, country_code, default_tax_rate) VALUES ($1, 'BRANCH', $2, $3, $4, 0)`,
		branch, "LC-"+branch.String()[:8], name, opts.country)
	exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`,
		yard, "LCY-"+branch.String()[:8], branch)
	exec(`INSERT INTO pos_registers (id, location_id, name, branch_id) VALUES ($1, $2, $3, $4)`, reg, yard, name, branch)
	exec(`INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, status, currency) VALUES ($1, $2, $3, $4, 'COMPLETED', 'USD')`,
		sale, reg, uuid.New(), branch)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM large_cash_filings WHERE event_id IN (SELECT id FROM large_cash_events WHERE branch_id = $1)`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM large_cash_events WHERE branch_id = $1`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE register_id = $1`, reg)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_registers WHERE id = $1`, reg)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, yard, branch)
	})
	facts := &largecash.CompletionFacts{BranchID: branch, TransactionID: sale, Currency: "USD",
		Payer: opts.payer, PayerDeclined: opts.declined, CustomerID: opts.customerID}
	if opts.cashCents > 0 {
		facts.Tenders = []largecash.TenderFacts{{Method: "cash", AmountCents: opts.cashCents}}
	}
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		return svc.Capture(ctx, facts)
	}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	var eventID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM large_cash_events WHERE transaction_ids @> ARRAY[$1::uuid]`, sale).Scan(&eventID); err != nil {
		t.Fatalf("the fixture wrote no crossing: %v", err)
	}
	return eventID
}

// rowFor finds the worklist row of one crossing.
func rowFor(t *testing.T, list *largecash.Worklist, eventID uuid.UUID) largecash.WorklistRow {
	t.Helper()
	for _, row := range list.Items {
		if row.EventID == eventID {
			return row
		}
	}
	t.Fatalf("the worklist carries no row for the crossing %s", eventID)
	return largecash.WorklistRow{}
}

// TestWorklistCarriesTheDeadlineTheSeverityAndTheAction: the manager's row
// explains itself: the deadline is the receipt plus 15 days, the filing is
// live, the action is to file, and the row never carries the payer's
// taxpayer identification number.
func TestWorklistCarriesTheDeadlineTheSeverityAndTheAction(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := keyedService(t, db)
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})

	list, err := svc.Worklist(ctx)
	if err != nil {
		t.Fatal(err)
	}
	row := rowFor(t, list, eventID)
	if row.FilingState != largecash.FilingStateReportable {
		t.Fatalf("filing state = %q, want %q", row.FilingState, largecash.FilingStateReportable)
	}
	if row.FilingDeadline == nil {
		t.Fatal("the row carries no filing deadline")
	}
	var anchor time.Time
	if err := db.Pool.QueryRow(ctx, `SELECT created_at FROM large_cash_events WHERE id = $1`, eventID).Scan(&anchor); err != nil {
		t.Fatal(err)
	}
	if want := largecash.FilingDeadline(anchor); !row.FilingDeadline.Equal(want) {
		t.Fatalf("the filing deadline = %s, want %s (the receipt anchor plus 15 days)", row.FilingDeadline, want)
	}
	if row.PastDeadline {
		t.Fatal("a fresh crossing is not past its deadline")
	}
	if row.Action != largecash.ActionFile {
		t.Fatalf("the action = %q, want %q", row.Action, largecash.ActionFile)
	}
	if row.Severity <= 0 {
		t.Fatalf("the severity = %d, want a positive score", row.Severity)
	}
	if row.RuleFamily != largecash.RuleUS {
		t.Fatalf("the rule family = %q, want %q", row.RuleFamily, largecash.RuleUS)
	}
	if row.ThresholdCents != largecash.ThresholdCents {
		t.Fatalf("the threshold = %d, want %d", row.ThresholdCents, largecash.ThresholdCents)
	}
	if row.TotalCents <= largecash.ThresholdCents {
		t.Fatalf("the reportable total = %d, want more than the threshold", row.TotalCents)
	}
	if row.AdviserNote != largecash.AdviserNote {
		t.Fatalf("the adviser note = %q, want %q", row.AdviserNote, largecash.AdviserNote)
	}
	// The taxpayer identification number never rides on the manager's queue.
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), fixtureTIN) {
		t.Fatalf("the worklist row carries the taxpayer identification number: %s", raw)
	}
}

// TestFilingIsRefusedForACanadianRecordAndAnIdentityDeclinedCrossing: a
// Canadian crossing is the dealer's own record and not a report, and an
// identity declined crossing has no payer to file for. Neither is a filing.
func TestFilingIsRefusedForACanadianRecordAndAnIdentityDeclinedCrossing(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := keyedService(t, db)
	// the Canadian dealer policy, on, at the threshold
	if err := svc.SaveSettings(ctx, &largecash.Settings{CountryDefault: "US", CAEnabled: true,
		CAThresholdCents: largecash.ThresholdCents}, "statement-refusal-test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE user_id = 'statement-refusal-test'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key LIKE 'largecash.%' OR key = 'country.default'`)
	})
	canada := seedCrossing(t, db, svc, crossingOptions{country: "CA", name: "canada fixture",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	if _, err := svc.RecordFiling(ctx, canada, largecash.RecordFilingIn{AcknowledgementNo: "ACK-1", Actor: "manager-a"}); err == nil {
		t.Fatal("a Canadian crossing accepted a filing")
	} else if !strings.Contains(err.Error(), "own record") {
		t.Fatalf("the Canadian refusal = %v, want the dealer's own record", err)
	}
	// the walk-in who would not say who they were: over the floor, under the
	// threshold, so the sale went ahead and the row is for a manager.
	declined := seedCrossing(t, db, svc, crossingOptions{country: "US", name: "declined fixture",
		cashCents: 500_000, declined: true})
	if _, err := svc.RecordFiling(ctx, declined, largecash.RecordFilingIn{AcknowledgementNo: "ACK-2", Actor: "manager-a"}); err == nil {
		t.Fatal("an identity declined crossing accepted a filing")
	} else if !strings.Contains(err.Error(), "declined to identify") {
		t.Fatalf("the identity declined refusal = %v, want the manager review", err)
	}
	// and the row says so, and offers no filing.
	list, err := svc.Worklist(ctx)
	if err != nil {
		t.Fatal(err)
	}
	row := rowFor(t, list, declined)
	if row.Action != largecash.ActionManagerReview {
		t.Fatalf("the identity declined row's action = %q, want %q", row.Action, largecash.ActionManagerReview)
	}
	if row.Note != largecash.IdentityDeclinedNote {
		t.Fatalf("the identity declined row's note = %q, want %q", row.Note, largecash.IdentityDeclinedNote)
	}
	if row.FilingState != largecash.FilingStateNotReportable {
		t.Fatalf("the identity declined row's filing state = %q, want %q", row.FilingState, largecash.FilingStateNotReportable)
	}
}

// TestThreeManagersFilingTheSameCrossingHaveOneWinner: the house's
// concurrency rule: three contenders at pool size four. Exactly one of them
// lands the filing and the other two are told the crossing is already filed,
// and the crossing carries one filing row.
func TestThreeManagersFilingTheSameCrossingHaveOneWinner(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	svc := keyedService(t, db)
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'large_cash.filing.recorded' AND entity_id = $1`, eventID)
	})
	var start sync.WaitGroup
	var wg sync.WaitGroup
	start.Add(1)
	errs := make([]error, 3)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			errs[i] = db.RunInTx(ctx, func(ctx context.Context) error {
				_, err := svc.RecordFiling(ctx, eventID, largecash.RecordFilingIn{
					AcknowledgementNo: "ACK-THREE", Actor: "manager-three"})
				return err
			})
		}(i)
	}
	start.Done()
	wg.Wait()
	winners, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			winners++
		case strings.Contains(err.Error(), "already filed"):
			conflicts++
		default:
			t.Fatalf("the three contenders answered %v, want one winner and two conflicts", err)
		}
	}
	if winners != 1 || conflicts != 2 {
		t.Fatalf("the three contenders gave %d winners and %d conflicts, want one and two", winners, conflicts)
	}
	var rows int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM large_cash_filings WHERE event_id = $1 AND state = 'filed'`, eventID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("the crossing carries %d filing rows, want one", rows)
	}
}

// TestTheFilingRaceHasOneWinner: two managers record a filing on one
// crossing at the same time. One takes the crossing and the other is told
// the crossing is already filed. The filing row is the lock: the first
// manager's transaction holds the row while the second manager's claim
// waits on it, and when the first commits the second finds the row filed
// and has nothing to take.
func TestTheFilingRaceHasOneWinner(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	svc := keyedService(t, db)
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'large_cash.filing.recorded' AND entity_id = $1`, eventID)
	})
	// The first manager takes the crossing's filing row and waits before it
	// commits, so the second manager really does arrive while the row is
	// taken.
	first, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(ctx) }()
	if _, err := first.Exec(ctx, `
		INSERT INTO large_cash_filings (event_id, state, filed_date, acknowledgement_no, recorded_by)
		VALUES ($1, 'filed', CURRENT_DATE, 'ACK-FIRST', 'manager-first')`, eventID); err != nil {
		t.Fatal(err)
	}
	// The second manager records the filing now. Its read of the crossing
	// sees no filing at all (the first manager's row is not committed), so
	// it reaches the claim and waits on the row the first manager holds.
	errCh := make(chan error, 1)
	go func() {
		errCh <- db.RunInTx(ctx, func(ctx context.Context) error {
			_, err := svc.RecordFiling(ctx, eventID, largecash.RecordFilingIn{
				AcknowledgementNo: "ACK-SECOND", Actor: "manager-second"})
			return err
		})
	}()
	time.Sleep(150 * time.Millisecond)
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = <-errCh
	if err == nil {
		t.Fatal("both managers recorded a filing on one crossing")
	}
	if !strings.Contains(err.Error(), "already filed") {
		t.Fatalf("the second manager's answer = %v, want the crossing is already filed", err)
	}
	var rows int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM large_cash_filings WHERE event_id = $1 AND state = 'filed'`, eventID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("the crossing carries %d filing rows, want one", rows)
	}
	var ack string
	if err := db.Pool.QueryRow(ctx, `SELECT acknowledgement_no FROM large_cash_filings WHERE event_id = $1`, eventID).Scan(&ack); err != nil {
		t.Fatal(err)
	}
	if ack != "ACK-FIRST" {
		t.Fatalf("the crossing's filing = %q, want the first manager's %q", ack, "ACK-FIRST")
	}
}

// TestTheForm8300ExportCarriesTheTinAndNothingElseDoes: the export is the
// one surface that carries the payer's taxpayer identification number, and
// the audit body never carries it.
func TestTheForm8300ExportCarriesTheTinAndNothingElseDoes(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := keyedService(t, db)
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'large_cash.form_8300.exported' AND entity_id = $1`, eventID)
	})
	form, err := svc.Form8300(ctx, eventID, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if form.Payer.TaxpayerID != fixtureTIN {
		t.Fatalf("the export's taxpayer identification number = %q, want %q", form.Payer.TaxpayerID, fixtureTIN)
	}
	if form.Payer.Name == "" || form.Payer.Address == "" {
		t.Fatalf("the export carries no payer: %+v", form.Payer)
	}
	if form.Note != largecash.GableDoesNotFile {
		t.Fatalf("the export's note = %q, want %q", form.Note, largecash.GableDoesNotFile)
	}
	if form.Filer.BranchName == "" {
		t.Fatal("the export names no filer")
	}
	var body string
	if err := db.Pool.QueryRow(ctx,
		`SELECT changes::text FROM audit_log WHERE action = 'large_cash.form_8300.exported' AND entity_id = $1`,
		eventID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, fixtureTIN) {
		t.Fatalf("the audit body carries the taxpayer identification number: %s", body)
	}
	if !strings.Contains(body, "payer_captured") {
		t.Fatalf("the audit body does not say a payer was captured: %s", body)
	}
}

// TestTheStatementRefusesTheSuspicion: the first refusal research 6.3
// names. The statement must not say the dealer noted the transaction was
// suspicious. The second, a voluntary filing, is pinned in
// statement_fix_test.go.
func TestTheStatementRefusesTheSuspicion(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := keyedService(t, db)
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action IN ('large_cash.statement.issued', 'large_cash.filing.recorded') AND entity_id = $1`, eventID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM large_cash_statements WHERE payer_key IN (SELECT payer_key FROM large_cash_events WHERE id = $1)`, eventID)
	})
	// The first refusal: the customer statement must not carry the dealer's
	// suspicion of the transaction.
	if _, err := svc.IssueStatement(ctx, largecash.IssueStatementIn{EventID: eventID,
		IncludeSuspicion: true, Actor: "manager-a"}); err == nil {
		t.Fatal("the statement carried the dealer's suspicion")
	} else if !strings.Contains(err.Error(), "suspicious") {
		t.Fatalf("the suspicion refusal = %v, want the suspicion rule", err)
	}
	// The second refusal is pinned in statement_fix_test.go.
}

// TestTheStatementCarriesTheContentAndItsState: the statement names the
// business, the contact, the aggregate and the sentence that says the
// information was furnished to the IRS, and its state is its own.
func TestTheStatementCarriesTheContentAndItsState(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := keyedService(t, db)
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action IN ('large_cash.statement.issued', 'large_cash.filing.recorded') AND entity_id = $1`, eventID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM large_cash_statements WHERE payer_key IN (SELECT payer_key FROM large_cash_events WHERE id = $1)`, eventID)
	})
	if _, err := svc.RecordFiling(ctx, eventID, largecash.RecordFilingIn{AcknowledgementNo: "ACK-1", Actor: "manager-a"}); err != nil {
		t.Fatal(err)
	}
	list, err := svc.Statements(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found *largecash.Statement
	for i := range list.Items {
		if list.Items[i].PayerName == "River Payer" {
			found = &list.Items[i]
			break
		}
	}
	if found == nil {
		t.Fatal("the year carries no statement for the payer")
	}
	if found.State != largecash.StatementStateNotIssued {
		t.Fatalf("the statement's state = %q, want %q", found.State, largecash.StatementStateNotIssued)
	}
	if found.Note != largecash.StatementFurnishedNote {
		t.Fatalf("the statement's note = %q, want %q", found.Note, largecash.StatementFurnishedNote)
	}
	if found.AggregateCents <= 0 {
		t.Fatalf("the statement's aggregate = %d, want the year's reportable cash", found.AggregateCents)
	}
	issued, err := svc.IssueStatement(ctx, largecash.IssueStatementIn{EventID: eventID, Actor: "manager-a"})
	if err != nil {
		t.Fatal(err)
	}
	if issued.State != largecash.StatementStateIssued {
		t.Fatalf("the issued statement's state = %q, want %q", issued.State, largecash.StatementStateIssued)
	}
	if issued.IssuedDate == nil {
		t.Fatal("the issued statement carries no issue date")
	}
	// The statement never carries the dealer's suspicion.
	raw, err := json.Marshal(issued)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "suspect") {
		t.Fatalf("the statement carries the dealer's suspicion: %s", raw)
	}
}

// TestTheDeadlineSweepMarksAndIsIdempotent: the sweep marks the filings
// that are past their deadline and still not filed, and a second pass marks
// nothing.
func TestTheDeadlineSweepMarksAndIsIdempotent(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := keyedService(t, db)
	eventID := seedCrossing(t, db, svc, crossingOptions{country: "US",
		cashCents: largecash.ThresholdCents + 200_000, payer: usPayer()})
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM large_cash_filings WHERE event_id = $1`, eventID)
	})
	// the crossing's own receipt, twenty days ago: its deadline has passed
	var anchor time.Time
	if err := db.Pool.QueryRow(ctx, `SELECT created_at FROM large_cash_events WHERE id = $1`, eventID).Scan(&anchor); err != nil {
		t.Fatal(err)
	}
	past := anchor.AddDate(0, 0, -20)
	if _, err := db.Pool.Exec(ctx, `UPDATE large_cash_events SET created_at = $2 WHERE id = $1`, eventID, past); err != nil {
		t.Fatal(err)
	}
	marked, err := svc.MarkPastDeadlines(ctx, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if marked < 1 {
		t.Fatalf("the sweep marked %d filings, want at least the fixture's one", marked)
	}
	var flag bool
	if err := db.Pool.QueryRow(ctx, `SELECT past_deadline FROM large_cash_filings WHERE event_id = $1`, eventID).Scan(&flag); err != nil {
		t.Fatal(err)
	}
	if !flag {
		t.Fatal("the sweep did not mark the filing past its deadline")
	}
	again, err := svc.MarkPastDeadlines(ctx, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("the second sweep marked %d filings, want none", again)
	}
}
