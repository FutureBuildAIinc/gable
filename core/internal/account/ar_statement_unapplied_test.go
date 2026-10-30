// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// The statement lists the payments still unapplied as of the period's end and
// states the net balance, which equals the aging's total on the same day (G1
// walk note N13, the money read's P2-3). The statement is read through its
// wire form so a missing field fails the leg rather than the build.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

func TestARStatementListsUnappliedCashAndStatesTheNetBalance(t *testing.T) {
	w := newARWorld(t)
	today := w.businessDay()
	w.invoice(100000, today.AddDate(0, 0, -20))
	pay := w.payment(50000, today.AddDate(0, 0, -5), "CHECK") // a deposit, nothing applied
	var number string
	if err := w.db.Pool.QueryRow(context.Background(), `SELECT number FROM payments WHERE id = $1`, pay).Scan(&number); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	st, err := w.svc.Statement(ctx, account.StatementQuery{CustomerID: w.customerID, From: today.AddDate(0, 0, -30), To: today})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(st)
	var wire struct {
		Currencies []map[string]any `json:"currencies"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil || len(wire.Currencies) != 1 {
		t.Fatalf("statement wire form: %v, %d currencies", err, len(wire.Currencies))
	}
	c := wire.Currencies[0]
	if got := c["closing_balance_cents"]; got != float64(100000) {
		t.Errorf("closing balance %v, want 100000 (the subledger has no row for cash on receipt)", got)
	}
	if got := c["unapplied_cents"]; got != float64(50000) {
		t.Errorf("unapplied cash %v, want 50000", got)
	}
	if got := c["net_balance_cents"]; got != float64(50000) {
		t.Errorf("net balance %v, want 50000", got)
	}
	list, _ := c["unapplied_payments"].([]any)
	if len(list) != 1 {
		t.Fatalf("%d unapplied payments listed, want 1", len(list))
	}
	row, _ := list[0].(map[string]any)
	if row["number"] != number || row["received_on"] != date(today.AddDate(0, 0, -5)) || row["unapplied_cents"] != float64(50000) {
		t.Errorf("unapplied payment row %v, want number %s, received %s, 50000", row, number, date(today.AddDate(0, 0, -5)))
	}

	// The net balance is the aging's total on the same day.
	sum, err := w.svc.AgingSummary(ctx, account.AgingQuery{AsOf: today, Basis: "due_date", CustomerID: &w.customerID})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Totals) != 1 || int64(sum.Totals[0].TotalCents) != 50000 {
		t.Fatalf("aging totals %+v, want one total of 50000", sum.Totals)
	}

	// As of a day before the money arrived, nothing is unapplied.
	st, err = w.svc.Statement(ctx, account.StatementQuery{CustomerID: w.customerID, From: today.AddDate(0, 0, -30), To: today.AddDate(0, 0, -10)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(st)
	_ = json.Unmarshal(raw, &wire)
	if got := wire.Currencies[0]["unapplied_cents"]; got != float64(0) {
		t.Errorf("unapplied cash before it arrived %v, want 0", got)
	}
}

// The documents a statement lists follow the caller's reach; the balances
// cover the whole account (FROM-PRINCIPAL 117): a caller pinned to one yard
// sees only that yard's unapplied payments in the list, and the net balance
// of the whole account, which is the unwalled aging's total.
func TestARStatementUnappliedListFollowsTheWallAndTheBalanceDoesNot(t *testing.T) {
	w := newARWorld(t)
	today := w.businessDay()
	w.invoice(100000, today.AddDate(0, 0, -20))
	own := w.payment(50000, today.AddDate(0, 0, -5), "CHECK")

	other := uuid.New()
	w.exec(`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, other, "UW-"+other.String()[:8])
	t.Cleanup(func() {
		w.exec(`UPDATE payments SET branch_id = `+w.branchSQL()+` WHERE branch_id = $1`, other)
		w.exec(`UPDATE gl_journal_lines SET branch_id = `+w.branchSQL()+` WHERE branch_id = $1`, other)
		w.exec(`UPDATE gl_journal_entries SET branch_id = `+w.branchSQL()+` WHERE branch_id = $1`, other)
		w.exec(`DELETE FROM locations WHERE id = $1`, other)
	})
	otherPay := uuid.New()
	w.mustAct("a check at another yard", func(ctx context.Context) (*account.Effects, error) {
		_, fx, e := w.svc.RecordPayment(ctx, account.RecordPaymentIn{PaymentID: otherPay, CustomerID: w.customerID, BranchID: other,
			Currency: "USD", Method: "CHECK", AmountCents: 7700, ReceivedOn: today.AddDate(0, 0, -4), Actor: "u-test"})
		return fx, e
	})
	var ownNumber, otherNumber string
	for id, dst := range map[uuid.UUID]*string{own: &ownNumber, otherPay: &otherNumber} {
		if err := w.db.Pool.QueryRow(context.Background(), `SELECT number FROM payments WHERE id = $1`, id).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	q := account.StatementQuery{CustomerID: w.customerID, From: today.AddDate(0, 0, -30), To: today}

	// pinned to the world's yard
	pinned := middleware.WithBranchContext(context.Background(), &middleware.BranchContext{BranchID: &w.branchID, IsAdmin: true})
	st, err := w.svc.Statement(pinned, q)
	if err != nil {
		t.Fatal(err)
	}
	c := st.Currencies[0]
	if len(c.UnappliedPayments) != 1 || c.UnappliedPayments[0].Number != ownNumber {
		t.Errorf("a pinned caller lists %+v, want only its own yard's %s", c.UnappliedPayments, ownNumber)
	}
	if int64(c.UnappliedCents) != 57700 || int64(c.NetBalanceCents) != 100000-57700 {
		t.Errorf("unapplied %d, net %d: the balances cover the whole account (57700, 42300)", c.UnappliedCents, c.NetBalanceCents)
	}

	// an unwalled caller lists both and agrees with the unwalled aging
	st, err = w.svc.Statement(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	c = st.Currencies[0]
	if len(c.UnappliedPayments) != 2 {
		t.Errorf("an unwalled caller lists %d payments, want 2", len(c.UnappliedPayments))
	}
	sum, err := w.svc.AgingSummary(context.Background(), account.AgingQuery{AsOf: today, Basis: "due_date", CustomerID: &w.customerID})
	if err != nil {
		t.Fatal(err)
	}
	if int64(sum.Totals[0].TotalCents) != int64(c.NetBalanceCents) {
		t.Errorf("aging total %d, net balance %d", sum.Totals[0].TotalCents, c.NetBalanceCents)
	}
}
