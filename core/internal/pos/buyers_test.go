// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Who may buy on the account: an authorized buyer flag and an
// optional per sale cap on the contacts, a per customer "named buyer
// required" setting, and the sale on account that names a buyer from the
// roster or is refused in words naming the account.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// addContact writes a contact of the fixture customer: authorized as a buyer
// with an optional cap in cents.
func (f *fixture) addContact(first, last string, authorized bool, capCents *int64, active bool) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	var capArg any
	if capCents != nil {
		capArg = *capCents
	}
	mustExec(f.t, f.db, `INSERT INTO customer_contacts (id, customer_id, first_name, last_name, authorized_buyer, buyer_sale_cap, is_active)
		VALUES ($1, $2, $3, $4, $5, $6::numeric / 100, $7)`, id, f.customerID, first, last, authorized, capArg, active)
	return id
}

func (f *fixture) namedBuyerRequired() {
	f.t.Helper()
	mustExec(f.t, f.db, `UPDATE customers SET named_buyer_required = TRUE WHERE id = $1`, f.customerID)
}

func ptr64(n int64) *int64 { return &n }

// accountCart is a named cart of qty with the buyer named when given.
func (f *fixture) accountCart(qty string, buyer *uuid.UUID) (string, int64) {
	f.t.Helper()
	id := f.startSale(&f.customerID)
	if r := f.addLine(id, f.productLine(qty)); r.status != http.StatusOK {
		f.t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if buyer != nil {
		r := f.attach(id, map[string]any{"customer_id": f.customerID.String(), "buyer_contact_id": buyer.String()})
		if r.status != http.StatusOK {
			f.t.Fatalf("name the buyer = %d: %s", r.status, r.raw)
		}
	}
	return id, num(f.t, f.getSale(f.t, id), "total_cents")
}

// RULE: the roster is the account's authorized, active contacts; the standing
// states it with each buyer's cap, and whether a named buyer is required.
func TestStandingStatesTheBuyerRoster(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.namedBuyerRequired()
	ada := f.addContact("Ada", "Buyer", true, ptr64(5000), true)
	f.addContact("Bo", "Visitor", false, nil, true)
	f.addContact("Cy", "Retired", true, nil, false)

	r := f.standing(f.customerID, "")
	if r.body["named_buyer_required"] != true {
		t.Errorf("named_buyer_required = %v, want true", r.body["named_buyer_required"])
	}
	buyers, _ := r.body["buyers"].([]any)
	if len(buyers) != 1 {
		t.Fatalf("buyers = %v, want only the authorized, active contact", r.body["buyers"])
	}
	b := buyers[0].(map[string]any)
	if b["id"] != ada.String() || b["name"] != "Ada Buyer" || num(t, b, "cap_cents") != 5000 {
		t.Errorf("buyer = %v, want Ada Buyer with a 5000 cap", b)
	}
}

// RULE: a buyer is named from the roster: a contact of another account, an
// unauthorized contact and an inactive one are refused at the naming, and a
// buyer needs a customer.
func TestTheSaleNamesABuyerFromTheRosterOnly(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ada := f.addContact("Ada", "Buyer", true, nil, true)
	visitor := f.addContact("Bo", "Visitor", false, nil, true)
	retired := f.addContact("Cy", "Retired", true, nil, false)

	// another customer's roster buyer
	other := uuid.New()
	mustExec(t, f.db, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Other Co', $2, `+defaultBranch+`)`,
		other, "OTH-"+uuid.NewString()[:8])
	stranger := uuid.New()
	mustExec(t, f.db, `INSERT INTO customer_contacts (id, customer_id, first_name, last_name, authorized_buyer) VALUES ($1, $2, 'Dee', 'Other', TRUE)`, stranger, other)
	t.Cleanup(func() { mustExec(t, f.db, `DELETE FROM customers WHERE id = $1`, other) })

	sale := f.startSale(&f.customerID)
	before := num(t, f.getSale(t, sale), "revision")
	for name, bad := range map[string]uuid.UUID{"another account's buyer": stranger, "an unauthorized contact": visitor, "an inactive contact": retired} {
		r := f.attach(sale, map[string]any{"customer_id": f.customerID.String(), "buyer_contact_id": bad.String()})
		if r.status != http.StatusBadRequest {
			t.Errorf("naming %s = %d, want 400: %s", name, r.status, r.raw)
			continue
		}
		if _, _, fields := errorOf(t, r); len(fields) == 0 || fields[0] != "buyer_contact_id" {
			t.Errorf("naming %s: fields = %v, want buyer_contact_id", name, fields)
		}
	}
	if r := f.attach(sale, map[string]any{"buyer_contact_id": ada.String()}); r.status != http.StatusBadRequest {
		t.Errorf("a buyer with no customer = %d, want 400: %s", r.status, r.raw)
	}
	r := f.attach(sale, map[string]any{"customer_id": f.customerID.String(), "buyer_contact_id": ada.String()})
	if r.status != http.StatusOK {
		t.Fatalf("naming a roster buyer = %d: %s", r.status, r.raw)
	}
	if r.body["buyer_contact_id"] != ada.String() {
		t.Errorf("buyer_contact_id = %v, want the roster buyer", r.body["buyer_contact_id"])
	}
	if rev(t, r) != before+1 {
		t.Errorf("revision = %d, want %d: one attach is one revision", rev(t, r), before+1)
	}
}

// RULE: an on account sale for an account that asks for a named buyer names
// one from the roster, or is refused 409 buyer_required in words naming the
// account, before anything moves.
func TestAnOnAccountSaleForANamedBuyerAccountNeedsABuyer(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.namedBuyerRequired()
	ada := f.addContact("Ada", "Buyer", true, nil, true)

	sale, total := f.accountCart("2", nil)
	stockBefore := f.stock()
	r := f.completeSale(sale, tender("account", total))
	_, blockers, _ := errorOf(t, r)
	if r.status != http.StatusConflict || len(blockers) != 1 || blockers[0] != "buyer_required" {
		t.Fatalf("an account sale with no buyer = %d: %s, want 409 buyer_required", r.status, r.raw)
	}
	if !strings.Contains(string(r.raw), "Counter Co") {
		t.Errorf("the refusal %s does not name the account", r.raw)
	}
	if f.stock() != stockBefore || f.counterInvoices(t) != 0 {
		t.Errorf("a refused sale moved stock or wrote an invoice")
	}

	// a cash sale for the same account asks for no buyer
	cashSale, cashTotal := f.accountCart("1", nil)
	if r := f.completeSale(cashSale, tender("cash", cashTotal)); r.status != http.StatusOK {
		t.Errorf("a cash sale for a named buyer account = %d, want 200: %s", r.status, r.raw)
	}

	named, namedTotal := f.accountCart("2", &ada)
	if r := f.completeSale(named, tender("account", namedTotal)); r.status != http.StatusOK {
		t.Fatalf("an account sale naming a roster buyer = %d: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}

// RULE: the buyer is checked again at the completion: a buyer taken off the
// roster after the naming is refused 409 buyer_not_authorized, and a buyer's
// per sale cap holds the sale's account portion.
func TestTheCompletionRechecksTheBuyerAndHoldsTheCap(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ada := f.addContact("Ada", "Buyer", true, ptr64(1000), true)

	// a sale over the cap: 2 units are 1198
	over, overTotal := f.accountCart("2", &ada)
	r := f.completeSale(over, tender("account", overTotal))
	_, blockers, _ := errorOf(t, r)
	if r.status != http.StatusConflict || len(blockers) != 1 || blockers[0] != "buyer_cap" {
		t.Fatalf("a sale over the buyer's cap = %d: %s, want 409 buyer_cap", r.status, r.raw)
	}
	if !strings.Contains(string(r.raw), "Ada Buyer") || !strings.Contains(string(r.raw), "10.00") {
		t.Errorf("the refusal %s does not name the buyer and the cap", r.raw)
	}
	// part on account within the cap, the rest in cash, is fine
	cash := overTotal - 1000
	if r := f.completeSale(over, tender("account", 1000), tender("cash", cash)); r.status != http.StatusOK {
		t.Errorf("an account portion at the cap = %d, want 200: %s", r.status, r.raw)
	}

	// taken off the roster after the naming
	gone, goneTotal := f.accountCart("1", &ada)
	mustExec(t, f.db, `UPDATE customer_contacts SET authorized_buyer = FALSE WHERE id = $1`, ada)
	r = f.completeSale(gone, tender("account", goneTotal))
	_, blockers, _ = errorOf(t, r)
	if r.status != http.StatusConflict || len(blockers) != 1 || blockers[0] != "buyer_not_authorized" {
		t.Errorf("a buyer taken off the roster = %d: %s, want 409 buyer_not_authorized", r.status, r.raw)
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pos_transactions WHERE id = $1 AND status = 'COMPLETED'`, uuid.MustParse(gone)).Scan(&n); err != nil || n != 0 {
		t.Errorf("the refused sale completed anyway (n=%d err=%v)", n, err)
	}
}
