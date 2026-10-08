// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// The round 2 review's P3s of PR 40 (C2-2b takes them): the contact order limit
// against the refreshed total, audit rows with the actor for hold, release and
// reopen, a slow tax provider holding no lock, and two concurrent confirms of
// one customer not both passing the credit check.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// RULE (round 2 P3-1, ADR 0005 5.3): the contact's order limit is checked
// against the total the confirm will write, after the tax refresh. A contact
// limited to 60.00, an order saved at 59.88, the branch rate raised so the
// refreshed total is 60.50: 409 contact_authority.
func TestContactLimitIsCheckedAgainstTheRefreshedTotal(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()
	contact := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customer_contacts (id, customer_id, first_name, last_name, can_place_orders, order_limit) VALUES ($1, $2, 'Test', 'Buyer', TRUE, 60.00)`, contact, f.customerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `UPDATE orders SET ordered_by_contact_id = NULL WHERE ordered_by_contact_id = $1`, contact)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_contacts WHERE id = $1`, contact)
	})
	body := f.createBody()
	body["ordered_by_contact_id"] = contact.String()
	r := f.do("POST", "/api/v1/orders", body) // 5500 + 8.875% = 5988
	if r.status != 201 || num(t, r.body, "total_cents") != 5988 {
		t.Fatalf("create = %d total %v: %s", r.status, r.body["total_cents"], r.raw)
	}
	id := str(t, r.body, "id")
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.10 WHERE id = `+branch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = $1 WHERE id = `+branch, f.branchRate)
	})
	r = f.transition(id, 1, "confirmed")
	if r.status != 409 {
		t.Fatalf("confirm over the contact's limit after the rate rose = %d, want 409: %s", r.status, r.raw)
	}
	if _, _, d := errorOf(t, r); len(d) != 1 || d[0]["code"] != "contact_authority" {
		t.Errorf("details = %v, want contact_authority", d)
	}
	g := f.do("GET", "/api/v1/orders/"+id, nil)
	if str(t, g.body, "status") != "draft" {
		t.Errorf("the refused confirm moved the order: %s", g.raw)
	}
}

// RULE (round 2 P3-3): a hold, a release and a reopen write an audit row with
// the actor; a finance release of a credit hold, the override of the credit
// check, records who did it and what was held.
func TestHoldReleaseAndReopenAreAuditedWithTheActor(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith()
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET credit_limit = 10.00 WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	post := func(id string, body map[string]any, role, sub string) resp {
		return f.do("POST", "/api/v1/orders/"+id+"/transitions", body, "X-Test-Role", role, "X-Test-Sub", sub)
	}
	r := f.create()
	id := str(t, r.body, "id")
	r = post(id, map[string]any{"to": "confirmed", "revision": 1}, "sales", "u-sales")
	if str(t, r.body, "status") != "on_hold" {
		t.Fatalf("credit hold = %q", r.body["status"])
	}
	r = post(id, map[string]any{"to": "confirmed", "revision": revision(t, r)}, "finance", "u-finance")
	if str(t, r.body, "status") != "confirmed" {
		t.Fatalf("release = %q: %s", r.body["status"], r.raw)
	}
	r = post(id, map[string]any{"to": "on_hold", "revision": revision(t, r), "hold_note": "customer called"}, "sales", "u-sales")
	if r.status != 200 {
		t.Fatalf("manual hold = %d: %s", r.status, r.raw)
	}
	r = post(id, map[string]any{"to": "draft", "revision": revision(t, r)}, "admin", "u-admin")
	if r.status != 200 {
		t.Fatalf("reopen = %d: %s", r.status, r.raw)
	}
	rows, err := db.Pool.Query(ctx, `SELECT action, COALESCE(user_id, ''), COALESCE(changes->>'hold_reason', '') FROM audit_log WHERE entity_type = 'order' AND entity_id = $1 AND action IN ('order.hold', 'order.hold_released', 'order.reopened') ORDER BY created_at, id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var action, user, reason string
		if err := rows.Scan(&action, &user, &reason); err != nil {
			t.Fatal(err)
		}
		got = append(got, action+"/"+user+"/"+reason)
	}
	want := "[order.hold/u-sales/credit_limit order.hold_released/u-finance/CREDIT_LIMIT order.hold/u-sales/manual order.reopened/u-admin/]"
	if fmt.Sprint(got) != want {
		t.Errorf("audit rows = %v, want %s", got, want)
	}
}

// RULE (ADR 0005 14.2): a slow tax provider holds no row lock: a concurrent
// confirm on the same products completes while it waits, and the act answers
// 503 having locked nothing.
func TestSlowTaxProviderHoldsNoRowLock(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	prov := &fakeTax{tax: 50}
	f.serveWith(f.withStock(), func(s *order.Service) *order.Service { return s.WithTaxProvider(prov) })
	f.stock(f.productID, "100")

	a := str(t, f.create().body, "id")
	b := str(t, f.create().body, "id")
	prov.tax = 50
	var bStatus int
	var bOrderStatus string
	released := false
	prov.hook = func() {
		if released {
			return // B's own pricing call
		}
		released = true
		// While A's provider call is in flight, B (same product) confirms.
		done := make(chan struct{})
		go func() {
			defer close(done)
			r := f.transition(b, 1, "confirmed")
			bStatus = r.status
			if r.body != nil {
				bOrderStatus, _ = r.body["status"].(string)
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("a concurrent confirm blocked behind the slow provider: a row lock is held across the provider call")
		}
		prov.err = errors.New("provider timed out")
	}
	r := f.transition(a, 1, "confirmed")
	if r.status != 503 {
		t.Fatalf("A with the provider failing = %d, want 503: %s", r.status, r.raw)
	}
	if bStatus != 200 || bOrderStatus != "confirmed" {
		t.Errorf("B confirmed while the provider waited = %d %q, want 200 confirmed", bStatus, bOrderStatus)
	}
	g := f.do("GET", "/api/v1/orders/"+a, nil)
	if str(t, g.body, "status") != "draft" || revision(t, g) != 1 {
		t.Errorf("A after the 503 = %q rev %d, want an untouched draft", g.body["status"], revision(t, g))
	}
}

// RULE (round 2 P3-6, ADR 0005 section 11): two concurrent confirms of one
// customer's orders cannot both pass the credit check. The limit fits one
// 59.88 order and not two: across repeated races exactly one confirms and the
// other lands on credit hold.
func TestConcurrentConfirmsOfOneCustomerCannotBothPassTheCreditCheck(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock())
	f.stock(f.productID, "1000")
	if _, err := db.Pool.Exec(context.Background(), `UPDATE customers SET credit_limit = 80.00 WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 6; round++ {
		a, b := str(t, f.create().body, "id"), str(t, f.create().body, "id")
		var wg sync.WaitGroup
		start := make(chan struct{})
		statuses := make([]string, 2)
		for i, id := range []string{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r := f.transition(id, 1, "confirmed")
				if r.status != 200 {
					t.Errorf("confirm = %d: %s", r.status, r.raw)
					return
				}
				statuses[i], _ = r.body["status"].(string)
			}()
		}
		close(start)
		wg.Wait()
		confirmed, held := 0, 0
		for _, s := range statuses {
			switch s {
			case "confirmed":
				confirmed++
			case "on_hold":
				held++
			}
		}
		if confirmed != 1 || held != 1 {
			t.Fatalf("round %d: statuses %v, want exactly one confirmed and one on credit hold", round, statuses)
		}
		// reset: cancel both so the exposure starts empty again
		for _, id := range []string{a, b} {
			g := f.do("GET", "/api/v1/orders/"+id, nil)
			f.transition(id, revision(t, g), "cancelled", "reason", "round over")
		}
	}
}
