// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// The market index CSV import and the staleness banner (QE-07-b1).
//
// A file of index values is previewed and then applied. The preview names
// every row's fate and the reason a row cannot be applied. The apply writes
// the accepted rows through the same path a manual refresh takes, so the
// exposure scan runs once for each index whose value moved, and importing
// the same file twice changes nothing the second time.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// seedImportIndex inserts one market index with the given code and current
// value, and removes it again when the test ends.
func seedImportIndex(t *testing.T, db *database.DB, code string, current float64) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO market_indices (name, source, current_value, previous_value, unit, index_code)
		 VALUES ($1, 'RANDOM_LENGTHS', $2, $2, 'MBF', $3)
		 ON CONFLICT (index_code) DO UPDATE SET current_value = EXCLUDED.current_value
		 RETURNING id`,
		"QE07 Import "+code, current, code).Scan(&id)
	if err != nil {
		t.Fatalf("seed index %s: %v", code, err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(),
			`DELETE FROM market_index_history WHERE market_index_id = $1`, id)
		_, _ = db.Pool.Exec(context.Background(),
			`DELETE FROM market_indices WHERE id = $1`, id)
	})
	return id
}

// seedImportQuote inserts a sent quote with one line and one active
// escalator on the index, so the exposure scan has something to evaluate and
// the scan's work is countable in the exposure ledger.
func seedImportQuote(t *testing.T, db *database.DB, indexID uuid.UUID, baseIndexValue float64) (quoteID, lineID, escalatorID uuid.UUID) {
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
	      VALUES ($1, 'QE07 Import Co', $2, `+branch+`)`,
		customerID, "QE07-"+uuid.NewString()[:8])
	exec(`INSERT INTO products (id, sku, description, uom_primary, base_price)
	      VALUES ($1, $2, 'QE07 Import Stud', 'EA', 100)`,
		productID, "QE07-"+uuid.NewString()[:8])
	exec(`INSERT INTO quotes (id, customer_id, project_id, state, total_amount, source,
	                          customer_notes, branch_id, exposure_state, created_at, updated_at)
	      VALUES ($1, $2, NULL, 'SENT', 500.00, 'manual', 'QE07 import fixture', `+branch+`,
	              'OK', NOW(), NOW())`, quoteID, customerID)
	exec(`INSERT INTO quote_lines (id, quote_id, product_id, sku, description, customer_note,
	                              quantity, uom, unit_price, line_total)
	      VALUES ($1, $2, $3, 'QE07', 'import line', 'none', 5, 'EA', 100.00, 500.00)`,
		lineID, quoteID, productID)
	exec(`INSERT INTO price_escalators (id, quote_line_id, market_index_id, escalation_type,
	                                   escalation_rate, base_price, base_index_value, effective_date,
	                                   expiration_date, current_state, is_active)
	      VALUES ($1, $2, $3, 'INDEX_DELTA', 0, 100.00, $4, CURRENT_DATE, CURRENT_DATE + 365,
	              'OK', TRUE)`, escalatorID, lineID, indexID, baseIndexValue)

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

// importService wires the import service the way serve does.
func importService(t *testing.T, db *database.DB) *pricing.IndexImportService {
	t.Helper()
	escalators := pricing.NewEscalatorRepository(db)
	exposure := pricing.NewExposureRepository(db)
	scanner := pricing.NewExposureScanner(exposure, escalators, quote.NewRepository(db), nil, db, nil)
	return pricing.NewIndexImportService(escalators, pricing.NewIndexSourceRepository(db), db, scanner, nil)
}

// exposureEventCount counts the exposure ledger rows a scan wrote for the
// quote: one row per escalator the scan evaluated above its threshold, so the
// count is the number of scans.
func exposureEventCount(t *testing.T, db *database.DB, quoteID uuid.UUID) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM quote_exposure_events WHERE quote_id = $1`, quoteID).Scan(&n); err != nil {
		t.Fatalf("count exposure events: %v", err)
	}
	return n
}

// historyCount counts the history rows stored for one index.
func historyCount(t *testing.T, db *database.DB, indexID uuid.UUID) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM market_index_history WHERE market_index_id = $1`, indexID).Scan(&n); err != nil {
		t.Fatalf("count history rows: %v", err)
	}
	return n
}

// RULE: the preview names every row's fate and, for a row that cannot be
// applied, why: an index code no market index carries, a value that is not a
// positive number, an as of date that is not a date, and a second row for an
// index and day the file already carries.
func TestQE07_CSVPreviewRefusals(t *testing.T) {
	db := testutil.RequireDB(t)
	code := "QE07_REF_" + uuid.NewString()[:8]
	seedImportIndex(t, db, code, 100)

	file := fmt.Sprintf(`index_code,value,as_of_date
QE07_NO_SUCH_CODE,100,2026-01-05
%s,0,2026-01-05
%s,-3,2026-01-05
%s,100,not-a-date
%s,110,2026-01-06
%s,120,2026-01-06
`, code, code, code, code, code)

	preview, err := importService(t, db).Preview(context.Background(), []byte(file))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	want := []struct {
		status string
		reason string
	}{
		{"refused", "unknown index code"},
		{"refused", "value must be a positive number"},
		{"refused", "value must be a positive number"},
		{"refused", "as of date must be a date (YYYY-MM-DD)"},
		{"accepted", ""},
		{"refused", "this file already has a row for this index and day"},
	}
	if len(preview.Rows) != len(want) {
		t.Fatalf("the preview has %d rows, want %d: %+v", len(preview.Rows), len(want), preview.Rows)
	}
	for i, w := range want {
		got := preview.Rows[i]
		if got.Status != w.status {
			t.Errorf("row %d (%s) status = %q, want %q", got.Line, got.IndexCode, got.Status, w.status)
		}
		if got.Reason != w.reason {
			t.Errorf("row %d (%s) reason = %q, want %q", got.Line, got.IndexCode, got.Reason, w.reason)
		}
	}
	if preview.RefusedCount != 5 {
		t.Errorf("refused_count = %d, want 5", preview.RefusedCount)
	}
	if preview.AcceptedCount != 1 {
		t.Errorf("accepted_count = %d, want 1", preview.AcceptedCount)
	}
	if preview.IndicesAffected != 1 {
		t.Errorf("indices_affected = %d, want 1", preview.IndicesAffected)
	}
	// The preview writes nothing.
	if n := historyCount(t, db, mustIndexID(t, db, code)); n != 0 {
		t.Errorf("the preview wrote %d history rows, want 0", n)
	}
}

// mustIndexID reads the id of the seeded index.
func mustIndexID(t *testing.T, db *database.DB, code string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT id FROM market_indices WHERE index_code = $1`, code).Scan(&id); err != nil {
		t.Fatalf("read index %s: %v", code, err)
	}
	return id
}

// RULE: the apply writes the file's rows through the same path a manual
// refresh takes. The newest as of date the file carries for an index becomes
// the index's current value, older rows are history only, and the exposure
// scan runs once for that index.
func TestQE07_CSVApplyScansOncePerIndex(t *testing.T) {
	db := testutil.RequireDB(t)
	code := "QE07_APPLY_" + uuid.NewString()[:8]
	indexID := seedImportIndex(t, db, code, 100)
	quoteID, _, _ := seedImportQuote(t, db, indexID, 100)

	file := fmt.Sprintf(`index_code,value,as_of_date
%s,110,2026-01-05
%s,120,2026-01-06
`, code, code)

	result, err := importService(t, db).Apply(context.Background(), []byte(file), "user-1")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.AppliedCount != 2 {
		t.Errorf("applied_count = %d, want 2", result.AppliedCount)
	}
	if result.ChangedIndices != 1 {
		t.Errorf("changed_indices = %d, want 1", result.ChangedIndices)
	}
	if result.Scans != 1 {
		t.Errorf("scans = %d, want 1 (one scan for the index that moved)", result.Scans)
	}

	// The newest day's value is the current value; the older day is history.
	var current float64
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT current_value FROM market_indices WHERE id = $1`, indexID).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 120 {
		t.Errorf("current_value = %v, want the newest day's 120", current)
	}
	if n := historyCount(t, db, indexID); n != 2 {
		t.Errorf("history rows = %d, want 2", n)
	}
	// The history rows carry their own day.
	var days int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM market_index_history WHERE market_index_id = $1 AND as_of_date IS NOT NULL`,
		indexID).Scan(&days); err != nil {
		t.Fatal(err)
	}
	if days != 2 {
		t.Errorf("history rows with a day = %d, want 2", days)
	}
	// The scan ran once: the ledger holds one row for the quote.
	if n := exposureEventCount(t, db, quoteID); n != 1 {
		t.Errorf("exposure ledger rows = %d, want 1 (one scan)", n)
	}
}

// RULE: importing the same file twice changes nothing the second time and
// scans nothing: the value for one index, day and source is stored once.
func TestQE07_CSVReimportIsNoop(t *testing.T) {
	db := testutil.RequireDB(t)
	code := "QE07_REIMPORT_" + uuid.NewString()[:8]
	indexID := seedImportIndex(t, db, code, 100)
	quoteID, _, _ := seedImportQuote(t, db, indexID, 100)

	file := fmt.Sprintf(`index_code,value,as_of_date
%s,120,2026-01-06
`, code)

	svc := importService(t, db)
	first, err := svc.Apply(context.Background(), []byte(file), "user-1")
	if err != nil {
		t.Fatalf("the first apply: %v", err)
	}
	if first.AppliedCount != 1 || first.ChangedIndices != 1 || first.Scans != 1 {
		t.Fatalf("the first apply = %+v, want one row, one index and one scan", first)
	}
	rowsAfterFirst := historyCount(t, db, indexID)
	eventsAfterFirst := exposureEventCount(t, db, quoteID)

	second, err := svc.Apply(context.Background(), []byte(file), "user-1")
	if err != nil {
		t.Fatalf("the second apply: %v", err)
	}
	if second.AppliedCount != 0 {
		t.Errorf("the second apply wrote %d rows, want 0", second.AppliedCount)
	}
	if second.ChangedIndices != 0 {
		t.Errorf("the second apply moved %d indices, want 0", second.ChangedIndices)
	}
	if second.Scans != 0 {
		t.Errorf("the second apply ran %d scans, want 0", second.Scans)
	}
	if second.UnchangedCount != 1 {
		t.Errorf("the second apply saw %d unchanged rows, want 1", second.UnchangedCount)
	}
	if n := historyCount(t, db, indexID); n != rowsAfterFirst {
		t.Errorf("history rows = %d after the second apply, want %d", n, rowsAfterFirst)
	}
	if n := exposureEventCount(t, db, quoteID); n != eventsAfterFirst {
		t.Errorf("exposure ledger rows = %d after the second apply, want %d", n, eventsAfterFirst)
	}
}

// RULE: the staleness answer reports an active index whose newest value is
// older than the threshold the deployment set, and leaves a fresh one alone.
func TestQE07_StaleIndexReported(t *testing.T) {
	db := testutil.RequireDB(t)
	staleCode := "QE07_STALE_" + uuid.NewString()[:8]
	freshCode := "QE07_FRESH_" + uuid.NewString()[:8]
	undatedCode := "QE07_UNDATED_" + uuid.NewString()[:8]
	staleID := seedImportIndex(t, db, staleCode, 100)
	freshID := seedImportIndex(t, db, freshCode, 100)
	seedImportIndex(t, db, undatedCode, 100)

	// The stale index's newest value is thirty days old; the fresh one's is
	// yesterday's. The threshold is the seeded seven days.
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -30).Truncate(24 * time.Hour)
	recent := time.Now().UTC().AddDate(0, 0, -1).Truncate(24 * time.Hour)
	for _, row := range []struct {
		id  uuid.UUID
		day time.Time
	}{{staleID, old}, {freshID, recent}} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO market_index_history (market_index_id, value, recorded_at, recorded_by, source, as_of_date, fetched_at)
			 VALUES ($1, 100, $2, 'seed', 'MANUAL', $3, $2)`, row.id, row.day, row.day); err != nil {
			t.Fatalf("seed history: %v", err)
		}
	}

	doc, err := pricing.NewIndexStalenessService(pricing.NewIndexSourceRepository(db)).
		Staleness(ctx, time.Now())
	if err != nil {
		t.Fatalf("Staleness: %v", err)
	}
	// The threshold itself belongs to the settings row, which the Tech Admin
	// tests also write (they save other thresholds), and the two packages
	// run in parallel. So this test holds the threshold it judges against
	// and proves the judgement, not the number: thirty days is stale under
	// any threshold the deployment sets here, and yesterday is not.
	if doc.ThresholdDays < 1 || doc.ThresholdDays > 30 {
		t.Errorf("threshold_days = %d, want a threshold this test's days judge against", doc.ThresholdDays)
	}
	if !doc.Stale {
		t.Error("the document does not report the stale index")
	}
	stale, ok := findFreshness(doc.StaleIndices, staleID)
	if !ok {
		t.Fatalf("the stale index is not among the stale ones: %+v", doc.StaleIndices)
	}
	if stale.DaysOld < 29 || stale.DaysOld > 31 {
		t.Errorf("days_old = %d, want about 30", stale.DaysOld)
	}
	if _, ok := findFreshness(doc.StaleIndices, freshID); ok {
		t.Error("the fresh index is reported as stale")
	}
	if fresh, ok := findFreshness(doc.Indices, freshID); !ok {
		t.Error("the fresh index is missing from the freshness list")
	} else if fresh.Stale {
		t.Error("the fresh index is marked stale")
	}
	// An index whose newest value carries no day cannot be judged old: it
	// says nothing about how old the value is.
	if _, ok := findFreshness(doc.StaleIndices, mustIndexID(t, db, undatedCode)); ok {
		t.Error("the undated index is reported as stale")
	}
	if undated, ok := findFreshness(doc.Indices, mustIndexID(t, db, undatedCode)); !ok {
		t.Error("the undated index is missing from the freshness list")
	} else if undated.DaysOld != -1 {
		t.Errorf("the undated index days_old = %d, want -1", undated.DaysOld)
	}
}

// RULE: a value for a day older than the index's newest stored day is
// history only: it never replaces a newer day's value, and nothing is
// scanned for it.
func TestQE07_CSVOlderDayDoesNotReplaceNewer(t *testing.T) {
	db := testutil.RequireDB(t)
	code := "QE07_OLDER_" + uuid.NewString()[:8]
	indexID := seedImportIndex(t, db, code, 100)
	quoteID, _, _ := seedImportQuote(t, db, indexID, 100)

	// A newer day's value is stored first, by hand.
	newer := time.Now().UTC().Truncate(24 * time.Hour)
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO market_index_history (market_index_id, value, recorded_at, recorded_by, source, as_of_date, fetched_at)
		 VALUES ($1, 150, $2, 'seed', 'MANUAL', $3::date, $2)`, indexID, newer, newer); err != nil {
		t.Fatalf("seed the newer value: %v", err)
	}

	older := newer.AddDate(0, 0, -10)
	file := fmt.Sprintf(`index_code,value,as_of_date
%s,90,%s
`, code, older.Format("2006-01-02"))

	result, err := importService(t, db).Apply(context.Background(), []byte(file), "user-1")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.AppliedCount != 1 {
		t.Errorf("applied_count = %d, want 1 (the older day is stored as history)", result.AppliedCount)
	}
	if result.ChangedIndices != 0 {
		t.Errorf("changed_indices = %d, want 0 (an older day never replaces a newer one)", result.ChangedIndices)
	}
	if result.Scans != 0 {
		t.Errorf("scans = %d, want 0", result.Scans)
	}
	var current float64
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT current_value FROM market_indices WHERE id = $1`, indexID).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 100 {
		t.Errorf("current_value = %v, want the newer day's value to stand (100)", current)
	}
	if n := historyCount(t, db, indexID); n != 2 {
		t.Errorf("history rows = %d, want 2 (the newer value and the older history)", n)
	}
	if n := exposureEventCount(t, db, quoteID); n != 0 {
		t.Errorf("exposure ledger rows = %d, want 0 (nothing moved)", n)
	}
}

// findFreshness looks one index up in a freshness list.
func findFreshness(list []pricing.IndexFreshness, id uuid.UUID) (pricing.IndexFreshness, bool) {
	for _, f := range list {
		if f.IndexID == id {
			return f, true
		}
	}
	return pricing.IndexFreshness{}, false
}

// RULE: three contenders applying one file at pool size 4 store the value
// once and scan once. The unique index for one index, day and source is what
// settles it: the losers write nothing and move nothing.
func TestQE07_CSVConcurrentApplyStoresOnce(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	code := "QE07_CONC_" + uuid.NewString()[:8]
	indexID := seedImportIndex(t, db, code, 100)
	quoteID, _, _ := seedImportQuote(t, db, indexID, 100)

	file := fmt.Sprintf(`index_code,value,as_of_date
%s,120,2026-01-06
`, code)

	const contenders = 3
	var wg sync.WaitGroup
	errs := make([]error, contenders)
	results := make([]*pricing.IndexImportResult, contenders)
	start := make(chan struct{})
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := importService(t, db).Apply(context.Background(), []byte(file), fmt.Sprintf("user-%d", i))
			results[i] = res
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("contender %d: %v", i, err)
		}
	}
	scans := 0
	for i, res := range results {
		if res == nil {
			t.Fatalf("contender %d produced no result", i)
		}
		scans += res.Scans
	}
	if scans != 1 {
		t.Errorf("the three applies ran %d scans in total, want 1", scans)
	}
	if n := historyCount(t, db, indexID); n != 1 {
		t.Errorf("history rows = %d, want 1 (the value is stored once)", n)
	}
	if n := exposureEventCount(t, db, quoteID); n != 1 {
		t.Errorf("exposure ledger rows = %d, want 1 (one scan)", n)
	}
	var current float64
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT current_value FROM market_indices WHERE id = $1`, indexID).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 120 {
		t.Errorf("current_value = %v, want 120", current)
	}
}
