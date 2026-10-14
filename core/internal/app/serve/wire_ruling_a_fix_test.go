// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// Ruling A, review round 1: the customer record's standing goes dealer wide
// with the record while the finance documents behind it keep following
// grants, and a link written by trade commits or rolls back with the document
// that caused it, whichever family wrote the document.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestRulingA_CustomerStandingIsDealerWide: a user granted only yard A reads
// B's customer with its balance and credit limit, and the account summary
// (the same figures) answers 200; the transactions and the statement of the
// same customer answer 404 to the same user (ADR 0007 section 5.8).
func TestRulingA_CustomerStandingIsDealerWide(t *testing.T) {
	f := newKeyPinFixture(t)
	noheaderGrant(t, f, "pin-finance", f.branchA)
	noheaderGrant(t, f, "pin-sales", f.branchA)
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE customers SET balance_due = 4321.00, credit_limit = 9999.00 WHERE id = $1`, f.custB); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"finance", "sales"} {
		for _, hdr := range []map[string]string{nil, headerA(f)} {
			status, buf := f.userAs(t, role, "GET", "/api/v1/customers/"+f.custB.String(), "", hdr)
			if status != http.StatusOK {
				t.Errorf("%s granted A reading B's customer (header %t): %d %s, want 200", role, hdr != nil, status, buf)
				continue
			}
			var c struct {
				Balance *int64 `json:"balance_cents"`
				Limit   *int64 `json:"credit_limit_cents"`
			}
			if err := json.Unmarshal(buf, &c); err != nil {
				t.Fatal(err)
			}
			if c.Balance == nil || *c.Balance != 432100 || c.Limit == nil || *c.Limit != 999900 {
				t.Errorf("%s (header %t): standing %v and %v, want 432100 and 999900", role, hdr != nil, c.Balance, c.Limit)
			}
			if role != "sales" {
				continue // the directory list is a sales role route
			}
			status, buf = f.userAs(t, role, "GET", "/api/v1/customers?q=pin", "", hdr)
			if status != http.StatusOK || !strings.Contains(string(buf), `"balance_cents":432100`) {
				t.Errorf("%s (header %t): the list lacks B's standing: %d %s", role, hdr != nil, status, buf)
			}
		}
	}
	for _, p := range accountSurface(f.custB) {
		for _, hdr := range []map[string]string{nil, headerA(f)} {
			want := http.StatusNotFound
			if summaryRoute(p) {
				want = http.StatusOK // ruling A: the summary is dealer wide
			}
			if status, buf := f.userAs(t, "finance", "GET", p, "", hdr); status != want {
				t.Errorf("finance granted A reading %s (header %t): %d %s, want %d", p, hdr != nil, status, buf, want)
			}
		}
	}
}

// failLinkAudit makes the audit row of a link for the customer fail, after
// the link row itself is written, and removes the trigger at the end. The
// trigger names the customer so no other test sees it.
func failLinkAudit(t *testing.T, f *keyPinFixture, customer uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	name := "fail_link_" + strings.ReplaceAll(customer.String()[:8], "-", "")
	for _, stmt := range []string{
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'forced link failure'; END $$`, name),
		fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW
			WHEN (NEW.action = 'customer.branch_linked' AND NEW.entity_id = '%s')
			EXECUTE FUNCTION %s()`, name, customer, name),
	} {
		if _, err := f.db.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("install the forced failure: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DROP TRIGGER IF EXISTS `+name+` ON audit_log`)
		_, _ = f.db.Pool.Exec(ctx, `DROP FUNCTION IF EXISTS `+name+`()`)
	})
}

// TestRulingA_ARolledBackFirstDocumentLeavesNoLink: for each family that
// links on its first document at a yard (quote, order, counter sale, and a customer named on a counter sale; the
// fulfilment invoice is TestFulfilmentInvoiceRollsBackWithItsLink in the order package), a create whose link audit fails rolls back to no document, no
// customer_branches row and no audit row. Each family first runs once with
// no failure as the control, so a 4xx that never reached the link cannot
// pass.
func TestRulingA_ARolledBackFirstDocumentLeavesNoLink(t *testing.T) {
	f := newKeyPinFixture(t)
	B := f.custB.String()
	reg := noheaderRegister(t, f, f.branchA, f.yardA)
	hdr := headerA(f)
	count := func(table string) int {
		return noheaderCount(t, f, `SELECT count(*) FROM `+table+` WHERE customer_id = $1`, f.custB)
	}
	orderBody := fmt.Sprintf(`{"customer_id":%q,"delivery_type":"pickup","lines":[%s]}`, B, f.productLine())
	// setup makes whatever the create needs before the failure is forced and
	// returns the create itself, which answers the status code.
	families := []struct {
		name, table string
		setup       func() func() int
	}{
		{"quote", "quotes", func() func() int {
			return func() int {
				s, _ := f.userAs(t, "admin", "POST", "/api/v1/quotes",
					fmt.Sprintf(`{"customer_id":%q,"lines":[%s]}`, B, f.quoteLine()), hdr)
				return s
			}
		}},
		{"order", "orders", func() func() int {
			return func() int {
				s, _ := f.userAs(t, "admin", "POST", "/api/v1/orders", orderBody, hdr)
				return s
			}
		}},
		{"counter sale", "pos_transactions", func() func() int {
			return func() int {
				s, _ := f.userAs(t, "admin", "POST", "/api/v1/pos/transactions",
					fmt.Sprintf(`{"register_id":%q,"customer_id":%q,"cashier_id":%q}`, reg, B, uuid.NewString()), nil)
				return s
			}
		}},
		// The sale is opened with no customer before the failure is forced;
		// the create under test is the customer named on it afterwards.
		{"counter sale customer named later", "pos_transactions", func() func() int {
			s, buf := f.userAs(t, "admin", "POST", "/api/v1/pos/transactions",
				fmt.Sprintf(`{"register_id":%q,"cashier_id":%q}`, reg, uuid.NewString()), nil)
			var sale struct {
				ID string `json:"id"`
			}
			if s != http.StatusCreated || json.Unmarshal(buf, &sale) != nil || sale.ID == "" {
				t.Fatalf("open the sale: %d %s", s, buf)
			}
			return func() int {
				s, _ := f.userAs(t, "admin", "PUT", "/api/v1/pos/transactions/"+sale.ID+"/customer",
					fmt.Sprintf(`{"customer_id":%q}`, B), nil)
				return s
			}
		}},
	}
	for _, c := range families {
		t.Run(c.name, func(t *testing.T) {
			unlink(t, f, f.custB, f.branchA)
			run := c.setup()
			if status := run(); status != http.StatusCreated && status != http.StatusOK {
				t.Fatalf("control: %s answered %d with no failure forced, want 2xx", c.name, status)
			}
			if links, audits := linkRows(t, f, f.custB, f.branchA); links != 1 || len(audits) != 1 {
				t.Fatalf("control: %d link(s) and %d audit row(s), want 1 and 1", links, len(audits))
			}
			unlink(t, f, f.custB, f.branchA)
			run = c.setup()
			before := count(c.table)
			failLinkAudit(t, f, f.custB)
			if status := run(); status < 400 {
				t.Errorf("%s with the link audit failing answered %d, want an error", c.name, status)
			}
			if links, audits := linkRows(t, f, f.custB, f.branchA); links != 0 || len(audits) != 0 {
				t.Errorf("%s rolled back left %d link(s) and %d audit row(s), want none", c.name, links, len(audits))
			}
			if after := count(c.table); after != before {
				t.Errorf("%s rolled back left %d row(s) of %s for the customer, want %d", c.name, after, c.table, before)
			}
		})
	}
}
