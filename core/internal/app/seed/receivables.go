// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"log/slog"
	"math"
	"sync"
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

	// The history's own fields; the base seed leaves them zero.
	OrderID uuid.UUID // the order the payment is recorded against
	PaidOn  time.Time // the day the payment is received, Date plus 7 days when zero
	Method  string    // the payment method, CHECK when empty
	// Stamp is the moment the history puts on the rows the act stamps with the
	// present (zero for the base seed, which keeps them).
	Stamp time.Time
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
			today, err := r.acct.LocalDate(ctx, in.BranchID, r.acct.Now())
			if err != nil {
				return err
			}
			// An invoice dated after the seed's day is left unpaid: no check
			// is received before the invoice it pays or after the day the
			// seed runs.
			if in.Paid <= 0 || dayOf(in.Date) > dayOf(today) {
				if !in.Stamp.IsZero() {
					return nil
				}
				return r.stampInvoice(ctx, in, uuid.Nil, time.Time{})
			}
			var currency string
			if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT currency FROM invoices WHERE id = $1`, in.ID).Scan(&currency); err != nil {
				return err
			}
			note := "Payment in full"
			if in.Paid < in.total() {
				note = "Partial payment"
			}
			method, receivedOn := in.Method, in.PaidOn
			if method == "" {
				method = "CHECK"
			}
			if receivedOn.IsZero() {
				receivedOn = in.Date.AddDate(0, 0, 7)
			}
			// The seed never dates money after the day it runs: a check the
			// rule would date later is received on the seed's own day.
			if dayOf(receivedOn) > dayOf(today) {
				receivedOn = today
			}
			var orderID *uuid.UUID
			if in.OrderID != uuid.Nil {
				orderID = &in.OrderID
			}
			payID, _, err := r.acct.RecordPayment(ctx, account.RecordPaymentIn{
				CustomerID: in.CustomerID, BranchID: in.BranchID, Currency: currency, Method: method, AmountCents: in.Paid,
				Reference: fmt.Sprintf("CHK-%d", 1000+in.Date.YearDay()*7%9000), Notes: note, ReceivedOn: receivedOn,
				OrderID: orderID, Actor: "seed", Applications: []account.ApplyLine{{InvoiceID: in.ID, AmountCents: in.Paid}}})
			if err != nil {
				return err
			}
			if in.Stamp.IsZero() {
				return r.stampInvoice(ctx, in, payID, receivedOn)
			}
			return r.stampDay(ctx, in, payID)
		})
		if err != nil {
			log.Printf("Seed: invoice %s was not posted: %v", in.ID, err)
			continue
		}
		posted++
	}
	return posted
}

// historyWorkers is how many connections the history posts on at once; the AR
// core's pool holds ten.
const historyWorkers = 6

// postParallel posts the invoices on several connections at once, which the
// history's volume needs to seed inside the stack's wait. A customer's
// invoices all go to one worker, in the order given, so its subledger runs in
// that order exactly as a single run writes it; the counters the acts draw on
// (payment and entry numbers) are sequences, so the workers do not queue on
// one another. The call returns once every worker is done.
func (r *receivables) postParallel(ctx context.Context, invoices []seedInvoice) int {
	lanes := make([][]seedInvoice, historyWorkers)
	for _, in := range invoices {
		w := int(in.CustomerID[0]) % historyWorkers
		lanes[w] = append(lanes[w], in)
	}
	counts := make([]int, historyWorkers)
	var wg sync.WaitGroup
	for w := range lanes {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			counts[w] = r.post(ctx, lanes[w])
		}(w)
	}
	wg.Wait()
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// stampDay puts the history's day on the rows the acts wrote with the present,
// through the AR core (the only writer of its tables, ADR 0005 section 9.3): the
// invoice's rows take the history's moment and the payment's a second later, in
// the same transaction as the acts.
func (r *receivables) stampDay(ctx context.Context, in seedInvoice, payID uuid.UUID) error {
	return r.acct.RestampHistory(ctx, in.ID, payID, in.Stamp, in.Stamp.Add(time.Second))
}

// dayOf is the calendar day of a moment, the form days compare in.
func dayOf(t time.Time) string { return t.Format("2006-01-02") }

// seedToday is the seed's own business day: the default branch's calendar day
// now, in its time zone (the day the AR core refuses money past).
func seedToday(db *sql.DB) time.Time {
	var d time.Time
	err := db.QueryRow(`SELECT (NOW() AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l
		WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')), 'UTC'))::date`).Scan(&d)
	if err != nil {
		return time.Now()
	}
	return d
}

// stampInvoice puts the documents' own days on the rows the base seed's acts
// wrote with the present: the invoice's rows take the invoice's day, the
// payment's the day it was received, so a seeded ledger reads like a real one.
// The stamps sit at midday and never after the present.
func (r *receivables) stampInvoice(ctx context.Context, in seedInvoice, payID uuid.UUID, receivedOn time.Time) error {
	now := r.acct.Now()
	at := func(d time.Time) time.Time {
		t := time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, time.UTC)
		if t.After(now) {
			t = now
		}
		return t
	}
	invoicedAt, paidAt := at(in.Date), invoicedAtOr(at(receivedOn), at(in.Date))
	return r.acct.RestampDay(ctx, in.ID, payID, invoicedAt, paidAt)
}

// invoicedAtOr answers the payment's moment, never before the invoice's.
func invoicedAtOr(paid, invoiced time.Time) time.Time {
	if paid.Before(invoiced) {
		return invoiced
	}
	return paid
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
