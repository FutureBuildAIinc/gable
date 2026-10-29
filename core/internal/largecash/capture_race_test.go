// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash_test

import (
	"context"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// TestCaptureSerializesOnThePayer: three transactions capture a cash tender
// each for one payer at once, on a pool of 4, through the real service. Each
// is under the threshold alone and the three are over it together, so only
// the payer's advisory lock lets the last one see the first two: without it
// every contender reads an empty window and no crossing is ever written.
// The route level race (pos) is serialized upstream by the customer row, so
// this is the test that holds the lock itself.
func TestCaptureSerializesOnThePayer(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	svc := largecash.NewService(db, nil, "")
	branch, yard, reg := uuid.New(), uuid.New(), "REG-"+uuid.NewString()[:8]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO locations (id, type, code, name, country_code, default_tax_rate) VALUES ($1, 'BRANCH', $2, 'capture race', 'US', 0)`, branch, "CR-"+branch.String()[:8])
	exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`, yard, "CRY-"+branch.String()[:8], branch)
	exec(`INSERT INTO pos_registers (id, location_id, name, branch_id) VALUES ($1, $2, 'capture race', $3)`, reg, yard, branch)
	sales := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for _, id := range sales {
		exec(`INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, status, currency) VALUES ($1, $2, $3, $4, 'COMPLETED', 'USD')`,
			id, reg, uuid.New(), branch)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM large_cash_events WHERE branch_id = $1`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE register_id = $1`, reg)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_registers WHERE id = $1`, reg)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, yard, branch)
	})
	payer := &largecash.PayerIn{Name: "Race Payer", Address: "1 Test Road", Occupation: "builder",
		DateOfBirth: "1980-01-02", IDType: "passport", IDNumber: "RACE-ID-0001", TaxpayerID: "99-0000001"}
	const each = largecash.ThresholdCents * 45 / 100
	var wg sync.WaitGroup
	errs := make([]error, len(sales))
	for i, id := range sales {
		wg.Add(1)
		go func(i int, id uuid.UUID) {
			defer wg.Done()
			errs[i] = db.RunInTx(ctx, func(ctx context.Context) error {
				return svc.Capture(ctx, &largecash.CompletionFacts{BranchID: branch, TransactionID: id, Currency: "USD", Payer: payer,
					Tenders: []largecash.TenderFacts{{Method: "cash", AmountCents: each}}})
			})
		}(i, id)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("contender %d: %v", i, err)
		}
	}
	var events int
	var total int64
	if err := db.Pool.QueryRow(ctx, `SELECT count(*), COALESCE(max(total_cents), 0) FROM large_cash_events WHERE branch_id = $1`, branch).Scan(&events, &total); err != nil {
		t.Fatal(err)
	}
	if events != 1 || total != 3*each {
		t.Fatalf("crossings = %d with total %d, want exactly one with the three tenders' %d", events, total, 3*each)
	}
}
