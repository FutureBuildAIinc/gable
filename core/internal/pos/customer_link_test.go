// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// linkCustomer seeds a customer whose only yard is other, with the cleanup of
// everything a counter sale at another yard writes for it.
func linkCustomer(t *testing.T, db *database.DB, other uuid.UUID, name string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	customer := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`,
		customer, name, "LK-"+customer.String()[:8], other); err != nil {
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
	return customer
}

// linkRows counts the customer's link at the yard and its audit rows naming
// the document (any document when document is empty).
func linkRows(t *testing.T, db *database.DB, customer, yard uuid.UUID, document string) (links, audits int) {
	t.Helper()
	ctx := context.Background()
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM customer_branches WHERE customer_id = $1 AND branch_id = $2`, customer, yard).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'customer.branch_linked' AND entity_id = $1
		AND ($2 = '' OR changes->>'document_id' = $2) AND changes->>'branch_id' = $3`, customer, document, yard.String()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	return links, audits
}

func newSale(register string, customer *uuid.UUID) *pos.Sale {
	return &pos.Sale{RegisterID: register, CashierID: uuid.New(), CustomerID: customer, Currency: "USD"}
}

// RULE (ruling A, ADR 0007 section 5.8): a counter sale for a customer not
// linked to the register's yard links the customer there, with one audit row
// naming the sale and the yard; a second sale adds nothing.
func TestCounterSaleLinksTheCustomerToTheRegistersYard(t *testing.T) {
	repo, register, own, other := registerBranchFixture(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	customer := linkCustomer(t, db, other, "link test")
	first := newSale(register, &customer)
	if err := repo.CreateSale(pinnedTo(own), first); err != nil {
		t.Fatal(err)
	}
	if links, audits := linkRows(t, db, customer, own, first.ID.String()); links != 1 || audits != 1 {
		t.Fatalf("the first sale left %d link(s) and %d audit row(s), want 1 and 1", links, audits)
	}
	if err := repo.CreateSale(pinnedTo(own), newSale(register, &customer)); err != nil {
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

// RULE: a customer named on an open sale later (SetSaleAccount) links to the
// sale's yard the same way, in the account update's transaction.
func TestCustomerNamedLaterLinksToTheSalesYard(t *testing.T) {
	repo, register, own, other := registerBranchFixture(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	customer := linkCustomer(t, db, other, "named later")
	sale := newSale(register, nil)
	if err := repo.CreateSale(pinnedTo(own), sale); err != nil {
		t.Fatal(err)
	}
	if links, audits := linkRows(t, db, customer, own, ""); links != 0 || audits != 0 {
		t.Fatalf("an unnamed sale linked %d and wrote %d audit row(s), want 0 and 0", links, audits)
	}
	if err := repo.SetSaleAccount(pinnedTo(own), sale.ID, &customer, "USD", nil, nil); err != nil {
		t.Fatal(err)
	}
	if links, audits := linkRows(t, db, customer, own, sale.ID.String()); links != 1 || audits != 1 {
		t.Fatalf("naming the customer left %d link(s) and %d audit row(s) for the sale, want 1 and 1", links, audits)
	}
	again := newSale(register, nil)
	if err := repo.CreateSale(pinnedTo(own), again); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSaleAccount(pinnedTo(own), again.ID, &customer, "USD", nil, nil); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'customer.branch_linked' AND entity_id = $1`, customer).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("a second sale naming the customer wrote %d audit rows in all, want 1", total)
	}
}

// Three contenders create the first counter sale for one customer at the
// register's yard, each through CreateSale on a pool of four: exactly one
// link and one audit row, and every sale written.
func TestConcurrentFirstCounterSalesLinkOnce(t *testing.T) {
	_, register, own, other := registerBranchFixture(t)
	db := testutil.RequireDBMaxConns(t, 4)
	repo := pos.NewRepository(db)
	ctx := context.Background()
	customer := linkCustomer(t, db, other, "concurrent counter link")

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- repo.CreateSale(pinnedTo(own), newSale(register, &customer))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("contender: %v", err)
		}
	}
	var sales int
	links, audits := linkRows(t, db, customer, own, "")
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM pos_transactions WHERE customer_id = $1`, customer).Scan(&sales); err != nil {
		t.Fatal(err)
	}
	if links != 1 || audits != 1 || sales != 3 {
		t.Errorf("three first counter sales left %d link(s), %d audit row(s) and %d sale(s), want 1, 1 and 3", links, audits, sales)
	}
}

// The same on the later naming: three contenders each open a sale and name
// the customer, on a pool of four.
func TestConcurrentFirstSaleAccountsLinkOnce(t *testing.T) {
	_, register, own, other := registerBranchFixture(t)
	db := testutil.RequireDBMaxConns(t, 4)
	repo := pos.NewRepository(db)
	ctx := context.Background()
	customer := linkCustomer(t, db, other, "concurrent account link")
	sales := make([]*pos.Sale, 3)
	for i := range sales {
		sales[i] = newSale(register, nil)
		if err := repo.CreateSale(pinnedTo(own), sales[i]); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for _, s := range sales {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- repo.SetSaleAccount(pinnedTo(own), s.ID, &customer, "USD", nil, nil)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("contender: %v", err)
		}
	}
	links, audits := linkRows(t, db, customer, own, "")
	var named int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM pos_transactions WHERE customer_id = $1`, customer).Scan(&named); err != nil {
		t.Fatal(err)
	}
	if links != 1 || audits != 1 || named != 3 {
		t.Errorf("three first account namings left %d link(s), %d audit row(s) and %d named sale(s), want 1, 1 and 3", links, audits, named)
	}
}

// failLinkAudit makes the audit row of a link for the customer fail, after
// the link row itself is written, and removes the trigger at the end.
func failLinkAudit(t *testing.T, db *database.DB, customer uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	name := "fail_pos_link_" + strings.ReplaceAll(customer.String()[:8], "-", "")
	for _, stmt := range []string{
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'forced link failure'; END $$`, name),
		fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW
			WHEN (NEW.action = 'customer.branch_linked' AND NEW.entity_id = '%s')
			EXECUTE FUNCTION %s()`, name, customer, name),
	} {
		if _, err := db.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("install the forced failure: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DROP TRIGGER IF EXISTS `+name+` ON audit_log`)
		_, _ = db.Pool.Exec(ctx, `DROP FUNCTION IF EXISTS `+name+`()`)
	})
}

// The link commits with the write it follows: a link whose audit row fails
// leaves no sale, no link and no audit row, whether the repository is called
// with no transaction open (it opens its own) for the create or the naming.
func TestCounterLinkRollsBackWithTheSaleWrite(t *testing.T) {
	repo, register, own, other := registerBranchFixture(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	customer := linkCustomer(t, db, other, "rollback")
	open := newSale(register, nil)
	if err := repo.CreateSale(pinnedTo(own), open); err != nil {
		t.Fatal(err)
	}
	failLinkAudit(t, db, customer)

	if err := repo.CreateSale(pinnedTo(own), newSale(register, &customer)); err == nil {
		t.Fatal("a sale whose link audit failed was created, want an error")
	}
	if err := repo.SetSaleAccount(pinnedTo(own), open.ID, &customer, "USD", nil, nil); err == nil {
		t.Fatal("a naming whose link audit failed was written, want an error")
	}
	var sales, named int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM pos_transactions WHERE customer_id = $1`, customer).Scan(&sales); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM pos_transactions WHERE id = $1 AND customer_id IS NOT NULL`, open.ID).Scan(&named); err != nil {
		t.Fatal(err)
	}
	links, audits := linkRows(t, db, customer, own, "")
	if sales != 0 || named != 0 || links != 0 || audits != 0 {
		t.Errorf("rolled back left %d sale(s) naming the customer, %d named sale(s), %d link(s) and %d audit row(s), want none", sales, named, links, audits)
	}
}
