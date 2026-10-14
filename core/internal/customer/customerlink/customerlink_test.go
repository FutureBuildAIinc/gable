// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerlink_test

import (
	"context"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/customer/customerlink"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// Three contenders create the first quote for one customer at one new yard,
// each in its own transaction, on a pool of four: exactly one link and exactly
// one audit row, whichever wins.
func TestConcurrentFirstDocumentsLinkOnce(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	home, yard, customer := uuid.New(), uuid.New(), uuid.New()
	for _, b := range []uuid.UUID{home, yard} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, b, "CL-"+b.String()[:8]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'concurrent link', $2, $3)`,
		customer, "CL-"+customer.String()[:8], home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customer)
		for _, b := range []uuid.UUID{home, yard} {
			_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, b)
		}
	})
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, customer, home); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.RunInTx(ctx, func(txCtx context.Context) error {
				id := uuid.New()
				if _, err := db.GetExecutor(txCtx).Exec(txCtx,
					`INSERT INTO quotes (id, customer_id, state, total_amount, branch_id) VALUES ($1, $2, 'DRAFT', 0, $3)`,
					id, customer, yard); err != nil {
					return err
				}
				return customerlink.Ensure(txCtx, db, "quotes", id)
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("contender: %v", err)
		}
	}
	var links, audits int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM customer_branches WHERE customer_id = $1 AND branch_id = $2`, customer, yard).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = $1 AND entity_id = $2`, customerlink.Action, customer).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if links != 1 || audits != 1 {
		t.Errorf("three first documents left %d link(s) and %d audit row(s), want 1 and 1", links, audits)
	}
}
