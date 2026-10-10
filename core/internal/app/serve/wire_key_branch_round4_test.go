// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// Fix round 4 of the branch bound key pin (principal rulings 43 to 45), every
// leg through the real serve chain with a key minted pinned to branch A and
// bodies that pass validation: each foreign leg is shadowed by an A control
// that proves the body valid, and every change is measured by the rows that
// name the twin customer or the ids the test made, never by whole table
// counts.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// round4Fixture is the pin fixture on the document family mint (the scopes the
// customer, payment, order, quote and draft routes need) with the walled
// twins seeded.
func round4Fixture(t *testing.T) (*keyPinFixture, *walledTwins) {
	t.Helper()
	f := newKeyPinFixture(t)
	f.bound, f.unbound, f.boundKeyID = f.bound2, f.unbound2, f.bound2KeyID
	// A document is taxed at its branch's rate, and a branch with none refuses
	// to save it.
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE locations SET default_tax_rate = 0.05 WHERE id IN ($1, $2)`, f.branchA, f.branchB); err != nil {
		t.Fatalf("seed the branch tax rate: %v", err)
	}
	return f, f.seedWalledTwins(t)
}

// madeRows removes, when the test ends, the document rows the legs created
// through the routes (their children first), so the shared database keeps no
// payments, orders, quotes or credit memos of the fixture's customers.
func (f *keyPinFixture) madeRow(t *testing.T, table string, id uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		exec := func(stmt string) { _, _ = f.db.Pool.Exec(ctx, stmt, id) }
		switch table {
		case "payments":
			exec(`DELETE FROM ar_applications WHERE payment_id = $1`)
		case "orders":
			exec(`DELETE FROM order_lines WHERE order_id = $1`)
		case "quotes":
			exec(`DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id = $1::text`)
			exec(`DELETE FROM quote_lines WHERE quote_id = $1`)
		case "credit_memos":
			exec(`DELETE FROM credit_memo_lines WHERE credit_memo_id = $1`)
			exec(`DELETE FROM ar_applications WHERE credit_memo_id = $1`)
		}
		exec(`DELETE FROM ` + table + ` WHERE id = $1`)
	})
}

// send runs one call for who ("bound", "unbound" or "user") and returns the
// status and body.
func (f *keyPinFixture) send(t *testing.T, who, method, path, body string, hdr map[string]string) (int, []byte) {
	t.Helper()
	switch who {
	case "bound":
		return f.key(t, f.bound, method, path, body, hdr)
	case "unbound":
		return f.key(t, f.unbound, method, path, body, hdr)
	}
	return f.user(t, method, path, body, hdr)
}

// made runs a create that must answer want and tracks the row it made.
func (f *keyPinFixture) made(t *testing.T, who, table, method, path, body string, want int) uuid.UUID {
	t.Helper()
	status, buf := f.send(t, who, method, path, body, nil)
	if status != want {
		t.Errorf("%s %s %s: %d %s, want %d", who, method, path, status, buf, want)
		return uuid.Nil
	}
	var doc struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(buf, &doc); err != nil || doc.ID == uuid.Nil {
		return uuid.Nil
	}
	if table != "" {
		f.madeRow(t, table, doc.ID)
	}
	return doc.ID
}

// heldBy reads what names a customer in the document tables the P1-1 legs
// write to, so a refused leg that wrote shows as a diff on the twin
// customer's own rows.
func (f *keyPinFixture) heldBy(t *testing.T, customer uuid.UUID) map[string]int {
	t.Helper()
	out := map[string]int{}
	for table, col := range map[string]string{"payments": "customer_id", "orders": "customer_id",
		"quotes": "customer_id", "credit_memos": "customer_id", "drafts": "payload->>'customer_id'"} {
		arg := any(customer)
		if table == "drafts" {
			arg = customer.String()
		}
		var n int
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM `+table+` WHERE `+col+` = $1`, arg).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func sameCounts(a, b map[string]int) bool {
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// productLine is one order line the pricing engine prices; quoteLine is the
// quote's, which sends its own price.
func (f *keyPinFixture) productLine() string {
	return fmt.Sprintf(`{"product_id":%q,"quantity":"1"}`, f.posProd.String())
}

func (f *keyPinFixture) quoteLine() string {
	return fmt.Sprintf(`{"product_id":%q,"quantity":"1","unit_price_ten_thousandths":10000}`, f.posProd.String())
}

// refusedPin asserts one bound key call is the pin's 403 naming customer_id
// with its audit row, and that the twin customer's rows did not move.
func (f *keyPinFixture) refusedPin(t *testing.T, label, method, path, body string, twin uuid.UUID) {
	t.Helper()
	before := f.heldBy(t, twin)
	rows := f.auditRows(t)
	status, buf := f.key(t, f.bound, method, path, body, nil)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "a branch bound key may act only on its own branch") {
		t.Errorf("%s: bound key %s %s naming the foreign customer: %d %s, want the pin's 403", label, method, path, status, buf)
	}
	if !strings.Contains(string(buf), "customer_id") {
		t.Errorf("%s: the refusal does not name customer_id: %s", label, buf)
	}
	if after := f.auditRows(t); after != rows+1 {
		t.Errorf("%s: key.branch_refused rows %d then %d, want one new row", label, rows, after)
	}
	if after := f.heldBy(t, twin); !sameCounts(before, after) {
		t.Errorf("%s: the refusal still wrote rows naming the foreign customer: %v then %v", label, before, after)
	}
}

// TestKeyBranchPin_BodyCustomerWall is round 9 P1-1: a payment (with its GL
// entry), a credit memo, an order (the tax exemption included), a quote, a
// document moved onto another customer and a draft promotion that names a
// customer held only by branch B are refused to a key pinned to A, and the
// A control of each proves the body valid. The unbound key and the user keep
// the reach they had.
func TestKeyBranchPin_BodyCustomerWall(t *testing.T) {
	f, _ := round4Fixture(t)
	A, B := f.custID.String(), f.custB.String()
	line := f.productLine()

	creates := []struct {
		name, table, path string
		body              func(customer string) string
	}{
		{"payment", "payments", "/api/v1/payments", func(c string) string {
			return fmt.Sprintf(`{"customer_id":%q,"amount_cents":100,"method":"cash"}`, c)
		}},
		{"credit memo", "credit_memos", "/api/v1/credit-memos", func(c string) string {
			return fmt.Sprintf(`{"customer_id":%q,"reason_code":"other","reason":"pin","lines":[`+
				`{"description":"pin adjustment","quantity":"-1","uom":"EA","unit_price_ten_thousandths":10000}]}`, c)
		}},
		{"order", "orders", "/api/v1/orders", func(c string) string {
			return fmt.Sprintf(`{"customer_id":%q,"delivery_type":"pickup","lines":[%s]}`, c, line)
		}},
		{"quote", "quotes", "/api/v1/quotes", func(c string) string {
			return fmt.Sprintf(`{"customer_id":%q,"lines":[%s]}`, c, f.quoteLine())
		}},
	}
	for _, c := range creates {
		f.refusedPin(t, c.name, "POST", c.path, c.body(B), f.custB)
		f.made(t, "bound", c.table, "POST", c.path, c.body(A), http.StatusCreated)
		for _, who := range []string{"unbound", "user"} {
			f.made(t, who, c.table, "POST", c.path, c.body(B), http.StatusCreated)
		}
	}

	// The card and intent creates name the customer the same way. No gateway
	// is configured here, so the A control answers 503 or 400 and never 403.
	for _, path := range []string{"/api/v1/payments/card", "/api/v1/payments/intent"} {
		body := func(c string) string {
			return fmt.Sprintf(`{"customer_id":%q,"amount_cents":100,"token_id":"teststub"}`, c)
		}
		f.refusedPin(t, path, "POST", path, body(B), f.custB)
		if status, buf := f.key(t, f.bound, "POST", path, body(A), nil); status == http.StatusForbidden {
			t.Errorf("bound key %s for the pin's own customer: %d %s, want past the wall", path, status, buf)
		}
	}

	// A document moved onto another customer: the quote update and the draft
	// order update that change customer_id. The foreign leg runs before its
	// control, which spends the revision.
	quotePut := func(c string) string {
		return fmt.Sprintf(`{"customer_id":%q,"revision":1,"lines":[%s]}`, c, f.quoteLine())
	}
	f.refusedPin(t, "quote move", "PUT", "/api/v1/quotes/"+f.quoteA.String(), quotePut(B), f.custB)
	f.allow(t, "bound", "PUT", "/api/v1/quotes/"+f.quoteA.String(), quotePut(A), nil, http.StatusOK)

	draftOrder := f.made(t, "bound", "orders", "POST", "/api/v1/orders",
		fmt.Sprintf(`{"customer_id":%q,"delivery_type":"pickup","lines":[%s]}`, A, line), http.StatusCreated)
	orderPut := func(c string) string {
		return fmt.Sprintf(`{"customer_id":%q,"delivery_type":"pickup","revision":1,"lines":[%s]}`, c, line)
	}
	f.refusedPin(t, "order move", "PUT", "/api/v1/orders/"+draftOrder.String(), orderPut(B), f.custB)
	f.allow(t, "bound", "PUT", "/api/v1/orders/"+draftOrder.String(), orderPut(A), nil, http.StatusOK)

	// The draft promotion rechecks the payload's customer: a draft naming B is
	// stored (the create is not where the document appears) and refused where
	// it would become one. The draft open on the A payload promotes.
	for _, kind := range []struct{ module, table, body string }{
		{"quotes", "quotes", `{"payload":{"customer_id":%q,"lines":[` + f.quoteLine() + `]}}`},
		{"orders", "orders", `{"payload":{"customer_id":%q,"delivery_type":"pickup","lines":[` + line + `]}}`},
	} {
		foreign := f.made(t, "bound", "drafts", "POST", "/api/v1/drafts/"+kind.module, fmt.Sprintf(kind.body, B), http.StatusCreated)
		own := f.made(t, "bound", "drafts", "POST", "/api/v1/drafts/"+kind.module, fmt.Sprintf(kind.body, A), http.StatusCreated)
		f.refusedPin(t, "promote "+kind.module, "POST", "/api/v1/drafts/"+kind.module+"/"+foreign.String()+"/promote", `{"revision":1}`, f.custB)
		f.made(t, "bound", kind.table, "POST", "/api/v1/drafts/"+kind.module+"/"+own.String()+"/promote", `{"revision":1}`, http.StatusCreated)
	}
}
