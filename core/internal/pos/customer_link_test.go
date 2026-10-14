// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// RULE (ruling A, ADR 0007 section 5.8): a counter sale for a customer not
// linked to the register's yard links the customer there, with one audit row
// naming the sale and the yard; a second sale adds nothing.
func TestCounterSaleLinksTheCustomerToTheRegistersYard(t *testing.T) {
	repo, register, own, other := registerBranchFixture(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	customer := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'link test', $2, $3)`,
		customer, "LK-"+customer.String()[:8], other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE customer_id = $1`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customer)
	})
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, customer, other); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sale := func() *pos.POSTransaction {
		return &pos.POSTransaction{RegisterID: register, CashierID: uuid.New(), CustomerID: &customer,
			Status: pos.TransactionStatusOpen, ClientCreatedAt: &now}
	}
	first := sale()
	if err := repo.CreateTransaction(pinnedTo(own), first); err != nil {
		t.Fatal(err)
	}
	count := func() (links, audits int) {
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM customer_branches WHERE customer_id = $1 AND branch_id = $2`, customer, own).Scan(&links); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'customer.branch_linked' AND entity_id = $1
			AND changes->>'document_id' = $2 AND changes->>'branch_id' = $3`, customer, first.ID.String(), own.String()).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		return links, audits
	}
	if links, audits := count(); links != 1 || audits != 1 {
		t.Fatalf("the first sale left %d link(s) and %d audit row(s), want 1 and 1", links, audits)
	}
	if err := repo.CreateTransaction(pinnedTo(own), sale()); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'customer.branch_linked' AND entity_id = $1`, customer).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("a second sale at the same yard wrote %d audit rows in all, want 1", total)
	}
}
