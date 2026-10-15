// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

var errRollBack = errors.New("roll back the test's rows")

// A customer two yards share: the statement's balance covers the whole
// account, so the aging it prints covers the whole account too, whichever
// yard prints it. Everything runs in one transaction that is rolled back.
func TestStatementAgingCoversTheWholeAccountOfACustomerTwoYardsShare(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	acct := account.NewService(db, gl.NewService(gl.NewRepository(db), nil, slog.Default()), slog.Default())
	yardA, yardB, cust := uuid.New(), uuid.New(), uuid.New()

	err := db.RunInTx(ctx, func(ctx context.Context) error {
		ex := db.GetExecutor(ctx)
		for _, y := range []uuid.UUID{yardA, yardB} {
			if _, err := ex.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, y, "SCP-"+y.String()[:8]); err != nil {
				return err
			}
		}
		if _, err := ex.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Two Yard Co', $2, $3)`,
			cust, "TY-"+cust.String()[:8], yardA); err != nil {
			return err
		}
		for _, y := range []uuid.UUID{yardA, yardB} {
			if _, err := ex.Exec(ctx, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, cust, y); err != nil {
				return err
			}
		}
		today := time.Now().UTC()
		for i, c := range []struct {
			yard  uuid.UUID
			cents int64
		}{{yardA, 10000}, {yardB, 25000}} {
			id := uuid.New()
			if _, err := ex.Exec(ctx, `INSERT INTO invoices (id, customer_id, branch_id, status, origin, number, currency, total_amount, subtotal,
					tax_rate, tax_amount, amount_open, due_date, invoice_date)
				VALUES ($1, $2, $3, 'UNPAID', 'POS', $4, 'USD', $5::bigint::numeric / 100, $5::bigint::numeric / 100, 0, 0,
					$5::bigint::numeric / 100, $6::date, $7::date)`,
				id, cust, c.yard, "SCP-"+id.String()[:8], c.cents, today.AddDate(0, 0, 20+i).Format("2006-01-02"),
				today.AddDate(0, 0, -3).Format("2006-01-02")); err != nil {
				return err
			}
			if _, err := acct.PostInvoice(ctx, account.PostInvoiceIn{InvoiceID: id, CustomerID: cust, Number: "SCP-" + id.String()[:8],
				Currency: "USD", TotalCents: c.cents, On: today.AddDate(0, 0, -3), Actor: "u-test",
				Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Credit: c.cents}}}); err != nil {
				return err
			}
		}

		// yard A prints
		ctx = middleware.WithBranchContext(ctx, &middleware.BranchContext{BranchID: &yardA, IsAdmin: true})
		st, err := acct.Statement(ctx, account.StatementQuery{CustomerID: cust, From: today.AddDate(0, 0, -30), To: today})
		if err != nil {
			return err
		}
		svc := NewService(newRepo()).WithStatements(acct)
		got := svc.statementAging(ctx, st)["USD"]
		if got == nil {
			t.Fatal("no aging for the statement's currency")
		}
		if int64(got.TotalCents) != 35000 || st.Currencies[0].NetBalanceCents != 35000 {
			t.Errorf("aging total %d, net balance %d: both must be the account's 35000", got.TotalCents, st.Currencies[0].NetBalanceCents)
		}
		return errRollBack
	})
	if !errors.Is(err, errRollBack) {
		t.Fatal(err)
	}
}
