// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"encoding/json"
	"sort"

	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// Effects is what an AR act changed: the applications it wrote or reversed,
// the status changes of the documents it touched, and the customers whose
// balance moved. The act's owner writes Events() last, in its transaction,
// after its own leading events (payment.recorded) and before its closing one
// (payment.voided).
type Effects struct {
	// ApplicationIDs are the applications the act wrote, in order.
	ApplicationIDs []uuid.UUID
	// ReversedIDs are the applications the act reversed, in order.
	ReversedIDs []uuid.UUID
	// EntryIDs are the journal entries the act posted, in order.
	EntryIDs []uuid.UUID

	payments  []paymentFact
	invoices  []invoiceFact
	memos     []memoFact
	balances  []balanceFact
	balanceAt map[uuid.UUID]int
}

type paymentFact struct {
	p         paymentRow
	unapplied bool // payment.unapplied, else payment.applied
	amount    int64
	invoices  []uuid.UUID
}

type invoiceFact struct {
	inv    invoiceRow // after the change
	from   string
	reopen bool
}

type memoFact struct {
	m      memoRow
	from   string
	reopen bool
}

type balanceFact struct {
	customerID uuid.UUID
	currency   string
	cents      int64
}

func (fx *Effects) entry(id *uuid.UUID) {
	if id != nil {
		fx.EntryIDs = append(fx.EntryIDs, *id)
	}
}

func (fx *Effects) balance(customerID uuid.UUID, currency string, cents int64) {
	if fx.balanceAt == nil {
		fx.balanceAt = map[uuid.UUID]int{}
	}
	if i, ok := fx.balanceAt[customerID]; ok {
		fx.balances[i].cents = cents
		return
	}
	fx.balanceAt[customerID] = len(fx.balances)
	fx.balances = append(fx.balances, balanceFact{customerID: customerID, currency: currency, cents: cents})
}

// invoice records an invoice change; the row passed is the one before the
// update (its Status is the from status).
func (fx *Effects) invoice(before *invoiceRow, status string, open int64, reopen bool) {
	after := *before
	after.Status, after.Open = status, open
	after.Revision = before.Revision + 1
	fx.invoices = append(fx.invoices, invoiceFact{inv: after, from: before.Status, reopen: reopen})
}

func (fx *Effects) memo(before *memoRow, status string, open int64, reopen bool) {
	after := *before
	after.Status, after.Open = status, open
	after.Revision = before.Revision + 1
	fx.memos = append(fx.memos, memoFact{m: after, from: before.Status, reopen: reopen})
}

func (fx *Effects) payment(p *paymentRow, unapplied bool, amount int64, invoices []uuid.UUID) {
	fx.payments = append(fx.payments, paymentFact{p: *p, unapplied: unapplied, amount: amount, invoices: invoices})
}

// BalanceMoved reports whether the act moved any customer's balance.
func (fx *Effects) BalanceMoved() bool { return len(fx.balances) > 0 }

func raw(v map[string]any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// Events are the events of the act in the order section 9.4 gives: the payment
// level events (payment.applied or payment.unapplied, one per payment touched),
// then the credit memo events, then each invoice's status event, then
// customer.updated (part balance) for every customer whose balance moved.
func (fx *Effects) Events() []outbox.Event {
	var out []outbox.Event
	for _, f := range fx.payments {
		typ := EventPaymentApplied
		if f.unapplied {
			typ = EventPaymentUnapplied
		}
		branch := f.p.BranchID
		ids := make([]string, len(f.invoices))
		for i, id := range f.invoices {
			ids[i] = id.String()
		}
		sort.Strings(ids)
		out = append(out, outbox.Event{Type: typ, EntityType: "payment", EntityID: f.p.ID, BranchID: &branch, Data: raw(map[string]any{
			"number": f.p.Number, "customer_id": f.p.CustomerID, "status": lower(f.p.Status), "revision": f.p.Revision,
			"currency": f.p.Currency, "amount_cents": f.amount, "unapplied_cents": f.p.Unapplied, "invoice_ids": ids,
		})})
	}
	for _, f := range fx.memos {
		typ := EventCreditPartial
		switch {
		case f.reopen:
			typ = EventCreditReopened
		case f.m.Status == "APPLIED":
			typ = EventCreditApplied
		}
		branch := f.m.BranchID
		out = append(out, outbox.Event{Type: typ, EntityType: "credit_memo", EntityID: f.m.ID, BranchID: &branch, Data: raw(map[string]any{
			"number": f.m.Number, "customer_id": f.m.CustomerID, "status": lower(f.m.Status), "from_status": lower(f.from),
			"revision": f.m.Revision, "currency": f.m.Currency, "total_cents": f.m.Total, "open_cents": f.m.Open,
		})})
	}
	for _, f := range fx.invoices {
		typ := EventInvoicePartial
		switch {
		case f.reopen:
			typ = EventInvoiceReopened
		case f.inv.Status == invPaid:
			typ = EventInvoicePaid
		case f.inv.Status == invWrittenOff:
			typ = EventInvoiceWrittenOff
		}
		branch := f.inv.BranchID
		out = append(out, outbox.Event{Type: typ, EntityType: "invoice", EntityID: f.inv.ID, BranchID: &branch, Data: raw(map[string]any{
			"number": f.inv.Number, "customer_id": f.inv.CustomerID, "status": lower(f.inv.Status), "from_status": lower(f.from),
			"revision": f.inv.Revision, "currency": f.inv.Currency, "total_cents": f.inv.Total, "open_cents": f.inv.Open,
		})})
	}
	for _, f := range fx.balances {
		out = append(out, outbox.Event{Type: EventCustomerUpdated, EntityType: "customer", EntityID: f.customerID, Data: raw(map[string]any{
			"part": customerBalancePartName, "customer_id": f.customerID, "balance_cents": f.cents, "currency": f.currency,
		})})
	}
	return out
}

// PaidInvoice is an invoice an act closed as paid.
type PaidInvoice struct {
	ID         uuid.UUID
	TotalCents int64
}

// PaidInvoices lists the invoices the act closed as paid (what FB Brain's
// financial engine is told, after the commit).
func (fx *Effects) PaidInvoices() []PaidInvoice {
	var out []PaidInvoice
	for _, f := range fx.invoices {
		if !f.reopen && f.inv.Status == invPaid {
			out = append(out, PaidInvoice{ID: f.inv.ID, TotalCents: f.inv.Total})
		}
	}
	return out
}

// Merge adds the other act's effects after these: applications, entries and
// documents changed, with the balance of a customer both moved kept at the
// later figure.
func (fx *Effects) Merge(o *Effects) {
	if o == nil {
		return
	}
	fx.ApplicationIDs = append(fx.ApplicationIDs, o.ApplicationIDs...)
	fx.ReversedIDs = append(fx.ReversedIDs, o.ReversedIDs...)
	fx.EntryIDs = append(fx.EntryIDs, o.EntryIDs...)
	fx.payments = append(fx.payments, o.payments...)
	fx.invoices = append(fx.invoices, o.invoices...)
	fx.memos = append(fx.memos, o.memos...)
	for _, b := range o.balances {
		fx.balance(b.customerID, b.currency, b.cents)
	}
}
