// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// RULE (CP-05 A1, the deposit path): an order deposit never books a card that
// ran nowhere. The recorded payment route (the counter's deposit, with or
// without the till) refuses a card and books no payment and calls no
// gateway: a card deposit is only ever the gateway charge of
// POST /api/v1/payments/card, which runs the card before it books anything.
func TestCP05_ACounterDepositByCardIsRefusedAndBooksNothing(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	gw := f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	orderID := str(t, f.create().body, "id")
	f.transition(orderID, 1, "confirmed")

	count := func() int64 {
		var n int64
		if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM payments WHERE order_id = $1`, orderID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before, charges := count(), gw.charges.Load()
	r := f.doUnique("POST", "/api/v1/payments", map[string]any{
		"customer_id": f.customerID.String(), "order_id": orderID, "amount_cents": 1000, "method": "card",
	}, "req-cp05-card-deposit")
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "a card payment is taken through POST /api/v1/payments/card") {
		t.Fatalf("a recorded card deposit = %d: %s, wanted 400 naming the card route", r.status, r.raw)
	}
	if n := count(); n != before {
		t.Fatalf("the refused card deposit booked %d payments", n-before)
	}
	if n := gw.charges.Load() - charges; n != 0 {
		t.Fatalf("the refused card deposit called the gateway %d times", n)
	}
}
