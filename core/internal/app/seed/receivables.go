// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"math"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// receivables posts the seed's invoices, payments and credit memos through the
// AR core (ADR 0005 section 9.3), so the seeded balances, the subledger, the
// documents' open amounts and the ledger agree. The seed's other tables are raw
// SQL on database/sql; money that moves AR goes only through here.
type receivables struct {
	db   *database.DB
	acct *account.Service
}

func newReceivables(dbURL string) (*receivables, error) {
	pdb, err := database.Connect(dbURL)
	if err != nil {
		return nil, fmt.Errorf("the seed's AR core could not connect: %w", err)
	}
	logger := slog.Default()
	return &receivables{db: pdb, acct: account.NewService(pdb, gl.NewService(gl.NewRepository(pdb), nil, logger), logger)}, nil
}

func (r *receivables) close() { r.db.Close() }

// seedInvoice is an invoice the seed wrote (UNPAID, opened at its total by the
// column trigger) that still has to be posted, and the share of it paid.
type seedInvoice struct {
	ID         uuid.UUID
	CustomerID uuid.UUID
	BranchID   uuid.UUID
	Date       time.Time
	Subtotal   int64 // cents
	Tax        int64 // cents
	Paid       int64 // cents of the total paid by a seeded check, 0 for none
}

func (s seedInvoice) total() int64 { return s.Subtotal + s.Tax }

// cents rounds dollars to whole cents, half away from zero.
func cents(dollars float64) int64 { return int64(math.Round(dollars * 100)) }

// post posts each invoice (DR 1020, CR 4010 and 2020) and records the check that
// paid it, applied in the same act.
func (r *receivables) post(ctx context.Context, invoices []seedInvoice) int {
	posted := 0
	for _, in := range invoices {
		err := r.db.RunInTx(ctx, func(ctx context.Context) error {
			if _, err := r.acct.PostInvoice(ctx, account.PostInvoiceIn{
				InvoiceID: in.ID, CustomerID: in.CustomerID, TotalCents: in.total(), On: in.Date, Actor: "seed",
				Legs: []gl.Leg{
					{AccountCode: gl.AccountCodeRevenue, Description: "Sales Revenue", Credit: in.Subtotal},
					{AccountCode: gl.AccountCodeSalesTax, Description: "Sales Tax Payable", Credit: in.Tax},
				}}); err != nil {
				return err
			}
			if in.Paid <= 0 {
				return nil
			}
			var currency string
			if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT currency FROM invoices WHERE id = $1`, in.ID).Scan(&currency); err != nil {
				return err
			}
			note := "Payment in full"
			if in.Paid < in.total() {
				note = "Partial payment"
			}
			_, _, err := r.acct.RecordPayment(ctx, account.RecordPaymentIn{
				CustomerID: in.CustomerID, BranchID: in.BranchID, Currency: currency, Method: "CHECK", AmountCents: in.Paid,
				Reference: fmt.Sprintf("CHK-%d", 1000+in.Date.YearDay()*7%9000), Notes: note, ReceivedOn: in.Date.AddDate(0, 0, 7),
				Actor: "seed", Applications: []account.ApplyLine{{InvoiceID: in.ID, AmountCents: in.Paid}}})
			return err
		})
		if err != nil {
			log.Printf("Seed: invoice %s was not posted: %v", in.ID, err)
			continue
		}
		posted++
	}
	return posted
}

// postMemo posts a draft credit memo the seed inserted (one ADJUST line, no
// tax): the entry reverses revenue, the receivable falls.
func (r *receivables) postMemo(ctx context.Context, memoID, customerID uuid.UUID, currency string, amountCents int64, on time.Time) error {
	return r.db.RunInTx(ctx, func(ctx context.Context) error {
		var number string
		if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT credit_memo_next_number()`).Scan(&number); err != nil {
			return err
		}
		_, err := r.acct.PostCreditMemo(ctx, account.PostCreditMemoIn{MemoID: memoID, CustomerID: customerID, Number: number, Currency: currency,
			MemoDate: on, SubtotalCents: -amountCents, TaxCents: 0, TotalCents: -amountCents, Actor: "seed",
			Legs: []gl.Leg{{AccountCode: gl.AccountCodeRevenue, Description: "Sales Revenue", Debit: amountCents}}})
		return err
	})
}
