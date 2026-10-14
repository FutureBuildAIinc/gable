// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The counter's customer through the wire (round 21 of the counter's pull
// request, with ruling A): a customer is named at the create, in the offline
// sync and at the completion by the one rule the lookup and the attach follow.
// A cashier reaches every customer, whatever the cashier's grants or header;
// a branch bound key keeps its pin and is refused, with nothing written.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNoHeader_CounterNamesACustomerUnderRulingA(t *testing.T) {
	f := newKeyPinFixture(t)
	noheaderGrant(t, f, "pin-cashier", f.branchA)
	noheaderTaxRate(t, f, f.branchA)
	noheaderStock(t, f)
	regNA := noheaderRegister(t, f, f.branchA, f.yardA)

	named := func(customer uuid.UUID) int {
		return noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE customer_id = $1`, customer)
	}
	foreignBefore := named(f.custB)
	status, buf := f.cashierNoHeader(t, "POST", "/api/v1/pos/transactions",
		fmt.Sprintf(`{"register_id":%q,"customer_id":%q}`, regNA, f.custB.String()))
	if status != http.StatusCreated {
		t.Errorf("no header create naming B's customer: %d %s, want 201 (ruling A)", status, buf)
	}
	if after := named(f.custB); after != foreignBefore+1 {
		t.Errorf("the served create wrote %d sales naming B's customer, want 1", after-foreignBefore)
	}
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/transactions",
		fmt.Sprintf(`{"register_id":%q,"customer_id":%q}`, regNA, f.custID.String()))
	if status != http.StatusCreated {
		t.Errorf("no header create naming A's customer: %d %s, want 201", status, buf)
	}

	// The offline sync: both items sync (ruling A), and B's customer's
	// invoice posts at the selling yard.
	sync := func(customer uuid.UUID) (string, string) {
		// B's customer holds a tax exemption, so its sale totals the 100 untaxed
		cents := 105
		if customer == f.custB {
			cents = 100
		}
		client := uuid.NewString()
		return client, fmt.Sprintf(`{"batch_id":"cw-%s","register_id":%q,"items":[{"client_id":%q,"cashier_id":%q,`+
			`"customer_id":%q,"items":[{"product_id":%q,"quantity":"1"}],`+
			`"tenders":[{"method":"account","amount_cents":%d}],"client_created_at":%q}]}`,
			uuid.NewString()[:8], regNA, client, uuid.NewString(), customer.String(),
			f.posProd.String(), cents, time.Now().UTC().Format(time.RFC3339))
	}
	invoicesBefore := noheaderCount(t, f, `SELECT COUNT(*) FROM invoices WHERE customer_id = $1 AND branch_id = $2`, f.custB, f.branchA)
	client, req := sync(f.custB)
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/sync", req)
	if status != http.StatusOK || !strings.Contains(string(buf), `"synced_count":1`) {
		t.Errorf("no header sync of B's customer: %d %s, want the sale synced", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE id = $1`, client); n != 1 {
		t.Errorf("the synced sale was not written")
	}
	if after := noheaderCount(t, f, `SELECT COUNT(*) FROM invoices WHERE customer_id = $1 AND branch_id = $2`, f.custB, f.branchA); after != invoicesBefore+1 {
		t.Errorf("the synced item posted %d invoices at A for B's customer, want 1", after-invoicesBefore)
	}
	_, req = sync(f.custID)
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/sync", req)
	if status != http.StatusOK || !strings.Contains(string(buf), `"synced_count":1`) {
		t.Errorf("no header sync of A's customer: %d %s, want the sale synced", status, buf)
	}

	// The return names a customer through the same rule (round 22, ruling A
	// here): a user's free account return naming B's customer is served and
	// credits that customer; the bound key stays a 403 with nothing written.
	noheaderPurgeReturn(t, f, regNA)
	retBody := func(customer uuid.UUID) string {
		return fmt.Sprintf(`{"register_id":%q,"customer_id":%q,"refund_method":"account","reason":"wall",`+
			`"lines":[{"product_id":%q,"quantity":"1","unit_price_ten_thousandths":10000}]}`,
			regNA, customer.String(), f.posProd.String())
	}
	memosBefore := noheaderCount(t, f, `SELECT COUNT(*) FROM credit_memos WHERE customer_id = $1`, f.custB)
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/returns", retBody(f.custB))
	if status != http.StatusCreated {
		t.Errorf("no header return naming B's customer: %d %s, want 201 (ruling A)", status, buf)
	}
	if after := noheaderCount(t, f, `SELECT COUNT(*) FROM credit_memos WHERE customer_id = $1`, f.custB); after != memosBefore+1 {
		t.Errorf("the return wrote %d credit memos to B's customer, want 1", after-memosBefore)
	}
	if keyStatus, keyBuf := f.key(t, f.bound, "POST", "/api/v1/pos/returns", retBody(f.custB), nil); keyStatus != http.StatusForbidden {
		t.Errorf("bound key return naming B's customer: %d %s, want 403", keyStatus, keyBuf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_returns WHERE register_id = $1`, regNA); n != 1 {
		t.Errorf("the user's return and the refused key's left %d returns, want 1", n)
	}

	// The bound key keeps its pin: a machine key is no cashier, and the
	// foreign customer is held at the wall besides; nothing is written.
	foreignBefore = named(f.custB)
	keyStatus, keyBuf := f.key(t, f.bound, "POST", "/api/v1/pos/transactions",
		fmt.Sprintf(`{"register_id":%q,"customer_id":%q}`, regNA, f.custB.String()), nil)
	if keyStatus != http.StatusForbidden {
		t.Errorf("bound key create naming B's customer: %d %s, want 403", keyStatus, keyBuf)
	}
	if after := named(f.custB); after != foreignBefore {
		t.Errorf("the bound key's create wrote %d sales naming B's customer", after-foreignBefore)
	}
	_, req = sync(f.custB)
	keyStatus, keyBuf = f.key(t, f.bound, "POST", "/api/v1/pos/sync", req, nil)
	if keyStatus != http.StatusForbidden {
		t.Errorf("bound key sync naming B's customer: %d %s, want 403", keyStatus, keyBuf)
	}
}

// The composition of ruling A and the one naming rule, through the wire: a
// cashier held to B's grants, with no header and with B's header, creates a
// sale naming A's customer and completes it with an account tender (200, one
// invoice at B, the selling yard); a key bound to B naming A's customer at the
// create, the sync and the completion is refused with nothing written.
func TestNoHeader_CounterCompletesAnotherYardsCustomerAtTheSellingYard(t *testing.T) {
	f := newKeyPinFixture(t)
	noheaderGrant(t, f, "pin-cashier", f.branchB)
	noheaderTaxRate(t, f, f.branchB)
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 100, 0)`,
		f.posProd, f.yardB); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
	regNB := noheaderRegister(t, f, f.branchB, f.yardB)
	boundB := f.mintWithScopes(t, "pos-key-b", pinKeyScopes, &f.branchB)

	invoicesAtB := func() int {
		return noheaderCount(t, f, `SELECT COUNT(*) FROM invoices WHERE customer_id = $1 AND branch_id = $2`, f.custID, f.branchB)
	}
	for _, hdr := range []map[string]string{nil, {"X-Branch-Id": f.branchB.String()}} {
		label := "no header"
		if hdr != nil {
			label = "header B"
		}
		before := invoicesAtB()
		status, buf := f.userAs(t, "cashier", "POST", "/api/v1/pos/transactions",
			fmt.Sprintf(`{"register_id":%q,"customer_id":%q}`, regNB, f.custID.String()), hdr)
		if status != http.StatusCreated {
			t.Fatalf("%s create naming A's customer: %d %s, want 201", label, status, buf)
		}
		var sale struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		if err := json.Unmarshal(buf, &sale); err != nil {
			t.Fatal(err)
		}
		status, buf = f.userAs(t, "cashier", "POST", "/api/v1/pos/transactions/"+sale.ID+"/items",
			`{"line":`+f.productLine()+`}`, hdr)
		if status != http.StatusOK {
			t.Fatalf("%s add line: %d %s", label, status, buf)
		}
		var cur struct {
			Total    int64 `json:"total_cents"`
			Revision int64 `json:"revision"`
		}
		if err := json.Unmarshal(buf, &cur); err != nil {
			t.Fatal(err)
		}
		status, buf = f.userAs(t, "cashier", "POST", "/api/v1/pos/transactions/"+sale.ID+"/complete",
			fmt.Sprintf(`{"tenders":[{"method":"account","amount_cents":%d}],"revision":%d}`, cur.Total, cur.Revision), hdr)
		if status != http.StatusOK {
			t.Fatalf("%s completion naming A's customer: %d %s, want 200", label, status, buf)
		}
		if got := invoicesAtB() - before; got != 1 {
			t.Errorf("%s: %d invoices at B for A's customer, want 1", label, got)
		}
	}

	// The key bound to B: A's customer is refused at the create, the sync
	// and the completion, and nothing is written.
	salesBefore := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE customer_id = $1`, f.custID)
	invBefore := invoicesAtB()
	keyStatus, keyBuf := f.key(t, boundB, "POST", "/api/v1/pos/transactions",
		fmt.Sprintf(`{"register_id":%q,"customer_id":%q}`, regNB, f.custID.String()), nil)
	if keyStatus != http.StatusForbidden && keyStatus != http.StatusNotFound {
		t.Errorf("key bound to B, create naming A's customer: %d %s, want refused", keyStatus, keyBuf)
	}
	client := uuid.NewString()
	keyStatus, keyBuf = f.key(t, boundB, "POST", "/api/v1/pos/sync", fmt.Sprintf(`{"batch_id":"cw-%s","register_id":%q,"items":[{"client_id":%q,"cashier_id":%q,`+
		`"customer_id":%q,"items":[{"product_id":%q,"quantity":"1"}],`+
		`"tenders":[{"method":"account","amount_cents":105}],"client_created_at":%q}]}`,
		uuid.NewString()[:8], regNB, client, uuid.NewString(), f.custID.String(),
		f.posProd.String(), time.Now().UTC().Format(time.RFC3339)), nil)
	if keyStatus != http.StatusForbidden {
		t.Errorf("key bound to B, sync naming A's customer: %d %s, want 403", keyStatus, keyBuf)
	}
	// The completion leg is a real sale a cashier opened at B naming A's
	// customer, completed by the key bound to B: the pin is what refuses it
	// (404 "customer not found"), the sale stays open and nothing is posted.
	status, buf := f.userAs(t, "cashier", "POST", "/api/v1/pos/transactions",
		fmt.Sprintf(`{"register_id":%q,"customer_id":%q}`, regNB, f.custID.String()), nil)
	if status != http.StatusCreated {
		t.Fatalf("cashier create for the key's completion leg: %d %s", status, buf)
	}
	var keySale struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &keySale); err != nil {
		t.Fatal(err)
	}
	status, buf = f.userAs(t, "cashier", "POST", "/api/v1/pos/transactions/"+keySale.ID+"/items",
		`{"line":`+f.productLine()+`}`, nil)
	if status != http.StatusOK {
		t.Fatalf("cashier add line for the key's completion leg: %d %s", status, buf)
	}
	var keyCur struct {
		Total    int64 `json:"total_cents"`
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(buf, &keyCur); err != nil {
		t.Fatal(err)
	}
	salesBefore = noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE customer_id = $1`, f.custID)
	keyStatus, keyBuf = f.key(t, boundB, "POST", "/api/v1/pos/transactions/"+keySale.ID+"/complete",
		fmt.Sprintf(`{"tenders":[{"method":"account","amount_cents":%d}],"revision":%d}`, keyCur.Total, keyCur.Revision), nil)
	if keyStatus != http.StatusNotFound || !strings.Contains(string(keyBuf), "customer not found") {
		t.Errorf("key bound to B, completion of B's sale naming A's customer: %d %s, want 404 customer not found", keyStatus, keyBuf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE id = $1 AND status = 'OPEN'`, keySale.ID); n != 1 {
		t.Errorf("the sale is no longer open after the key's refused completion")
	}
	if after := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE customer_id = $1`, f.custID); after != salesBefore {
		t.Errorf("the bound key's legs wrote %d sales naming A's customer", after-salesBefore)
	}
	if after := invoicesAtB(); after != invBefore {
		t.Errorf("the bound key's legs posted %d invoices", after-invBefore)
	}
}
