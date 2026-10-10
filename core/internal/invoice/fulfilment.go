// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// FulfilmentLine is one line of the invoice a fulfilment bills, in the shared
// line shape of ADR 0005 section 2.2 (money in cents and scale 4 integers),
// plus the order line it bills and its cost (section 8.4).
type FulfilmentLine struct {
	ID                 uuid.UUID
	Position           int
	LineType           string // PRODUCT, KIT, COMPONENT, CHARGE, TEXT
	ParentLineID       *uuid.UUID
	ProductID          *uuid.UUID
	ChargeCodeID       *uuid.UUID
	SKU                *string
	Description        string
	Quantity           *int64 // scale 4
	UOM                *string
	PriceUOM           *string
	UOMQty             *int64
	PriceUOMQty        *int64
	UnitPrice          *int64 // scale 4
	PricedUnitPrice    *int64 // scale 4: what the engine resolved
	PriceSource        string
	OverrideReason     *string
	PriceAdjustedBy    *string
	DiscountPercent    *int64 // scale 4
	DiscountCents      *int64
	DiscountReason     *string
	LineTotalCents     *int64
	Taxable            bool
	RevenueAccountCode *string
	OrderLineID        *uuid.UUID
	UnitCost           *int64 // scale 4
	CostCents          int64
}

// RevenueLeg is one revenue account's credit of the invoice entry.
type RevenueLeg struct {
	AccountCode string
	Cents       int64
}

// FulfilmentInvoice is the invoice an order fulfilment bills (ADR 0005 5.6 and
// 6.1) with everything its entry needs: the revenue by account, the tax and
// the cost of goods sold. The number, the terms and their dates are filled by
// CreateFulfilmentInvoice (the number is minted late, section 11).
type FulfilmentInvoice struct {
	ID         uuid.UUID
	Number     string
	BranchID   uuid.UUID
	OrderID    uuid.UUID
	CustomerID uuid.UUID
	Currency   string

	DeliveryType   string // DELIVERY or PICKUP
	PickedUpBy     *string
	DeliveryID     *uuid.UUID
	ShipToID       *uuid.UUID
	ShipToSnapshot []byte // JSON, or nil
	ProjectID      *uuid.UUID
	InvoiceDate    time.Time // the branch's local date

	PaymentTermsID  uuid.UUID
	DueDate         time.Time
	DiscountDueDate *time.Time
	DiscountPercent *int64 // scale 4

	SubtotalCents int64
	TaxCents      int64
	TotalCents    int64
	TaxRate       *string // the decimal rate "0.088750", nil when a provider priced it
	TaxExempt     bool
	TaxSource     string // EXEMPT, PROVIDER, SHIP_TO_RATE, BRANCH_RATE

	Lines     []FulfilmentLine
	Revenue   []RevenueLeg
	CostCents int64 // the entry's COGS legs

	Actor string

	// Effects is what the AR core did posting the invoice: the caller writes
	// its events (customer.updated, part balance) last.
	Effects *account.Effects
}

// CounterInvoice is the invoice a completed counter sale bills (ADR 0005
// 14.2 C2-5): origin POS, pickup, no order, everything its entry needs. The
// number, the terms and their dates are filled by CreateCounterInvoice
// (the number is minted late, section 11).
type CounterInvoice struct {
	ID         uuid.UUID
	Number     string
	BranchID   uuid.UUID
	CustomerID uuid.UUID
	Currency   string

	PickedUpBy  *string
	InvoiceDate time.Time // the branch's local date

	SubtotalCents int64
	TaxCents      int64
	TotalCents    int64
	TaxRate       *string // the decimal rate "0.088750", nil when a provider priced it
	TaxExempt     bool
	TaxSource     string // EXEMPT, PROVIDER, BRANCH_RATE

	Lines     []FulfilmentLine
	Revenue   []RevenueLeg
	CostCents int64 // the entry's COGS legs

	Actor string

	// Effects is what the AR core did posting the invoice: the caller writes
	// its events (invoice.created, customer.updated, part balance) last.
	Effects *account.Effects
}

// CreateCounterInvoice writes the invoice of a completed counter sale inside
// the caller's transaction, on the same path a fulfilment's invoice takes:
// the customer row is locked, THEN the gapless number is minted,
// immediately before the insert; the balanced entry (DR 1020 the total and
// 5010 the cost; CR each revenue account, 2020 the tax and 1030 the cost)
// and the subledger debit go through the AR core. origin POS,
// delivery_type PICKUP, no order.
func (s *Service) CreateCounterInvoice(ctx context.Context, in *CounterInvoice) error {
	if !database.InTx(ctx) {
		return gl.ErrNoTransaction
	}
	store, ok := s.repo.(FulfilmentStore)
	if !ok {
		return errors.New("invoice: the repository cannot store a fulfilment invoice")
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	if len(in.Lines) == 0 {
		return fmt.Errorf("invoice must have lines")
	}
	terms, err := store.TermsFor(ctx, in.CustomerID)
	if err != nil {
		return err
	}
	if err := store.LockCustomer(ctx, in.CustomerID); err != nil {
		return err
	}
	if in.Number, err = store.NextInvoiceNumber(ctx); err != nil {
		return err
	}
	inv := &FulfilmentInvoice{
		ID: in.ID, Number: in.Number, BranchID: in.BranchID, CustomerID: in.CustomerID, Currency: in.Currency,
		DeliveryType: "PICKUP", PickedUpBy: in.PickedUpBy, InvoiceDate: in.InvoiceDate,
		SubtotalCents: in.SubtotalCents, TaxCents: in.TaxCents, TotalCents: in.TotalCents,
		TaxRate: in.TaxRate, TaxExempt: in.TaxExempt, TaxSource: in.TaxSource,
		Lines: in.Lines, Revenue: in.Revenue, CostCents: in.CostCents, Actor: in.Actor,
		PaymentTermsID: terms.ID,
	}
	inv.DueDate = terms.Terms.DueDate(in.InvoiceDate)
	if err := store.InsertCounterInvoice(ctx, inv); err != nil {
		return err
	}
	if s.account != nil {
		legs := []gl.Leg{{AccountCode: gl.AccountCodeCOGS, Description: "Cost of Goods Sold", Debit: in.CostCents}}
		for _, r := range in.Revenue {
			legs = append(legs, gl.Leg{AccountCode: r.AccountCode, Description: "Revenue " + r.AccountCode, Credit: r.Cents})
		}
		legs = append(legs,
			gl.Leg{AccountCode: gl.AccountCodeSalesTax, Description: "Sales Tax Payable", Credit: in.TaxCents},
			gl.Leg{AccountCode: gl.AccountCodeInventory, Description: "Inventory", Credit: in.CostCents},
		)
		fx, err := s.account.PostInvoice(ctx, account.PostInvoiceIn{InvoiceID: in.ID, CustomerID: in.CustomerID,
			Number: in.Number, Currency: in.Currency, TotalCents: in.TotalCents, On: in.InvoiceDate, Actor: in.Actor, Legs: legs})
		if err != nil {
			return mapPostingError(fmt.Errorf("failed to post the invoice: %w", err))
		}
		in.Effects = fx
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action: "invoice.created", EntityType: "invoice", EntityID: in.ID, UserID: in.Actor,
			Changes: map[string]interface{}{
				"number": in.Number, "customer_id": in.CustomerID, "order_id": nil, "total_cents": in.TotalCents,
				"cost_cents": in.CostCents, "origin": "POS",
			},
		}); err != nil {
			return fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	return nil
}

// FulfilmentStore is the repository half the fulfilment invoice needs; the
// Postgres repository implements it.
type FulfilmentStore interface {
	TermsFor(ctx context.Context, customerID uuid.UUID) (*CustomerTerms, error)
	LockCustomer(ctx context.Context, customerID uuid.UUID) error
	NextInvoiceNumber(ctx context.Context) (string, error)
	InsertFulfilmentInvoice(ctx context.Context, in *FulfilmentInvoice) error
	InsertCounterInvoice(ctx context.Context, in *FulfilmentInvoice) error
}

// CreateFulfilmentInvoice writes the invoice of an order fulfilment inside the
// caller's transaction (ADR 0005 5.6 steps 5 and 6, 8.2), in section 11's
// order: the customer row is locked, THEN the gapless number is minted,
// immediately before the insert; the journal entry and the subledger follow.
// It posts the balanced invoice entry through gl.PostEntry (DR 1020 total and
// 5010 cost; CR each revenue account, 2020 tax and 1030 cost) and the AR
// subledger debit through the AR core (C2-4 moved both postings there). It
// refuses to run outside a transaction; the events are the caller's, written
// last.
func (s *Service) CreateFulfilmentInvoice(ctx context.Context, in *FulfilmentInvoice) error {
	if !database.InTx(ctx) {
		return gl.ErrNoTransaction
	}
	store, ok := s.repo.(FulfilmentStore)
	if !ok {
		return errors.New("invoice: the repository cannot store a fulfilment invoice")
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	if len(in.Lines) == 0 {
		return fmt.Errorf("invoice must have lines")
	}

	// The terms the invoice snapshots: due date and early pay discount from
	// the customer's terms at the invoice date (ADR 0005 7.2).
	terms, err := store.TermsFor(ctx, in.CustomerID)
	if err != nil {
		return err
	}
	in.PaymentTermsID = terms.ID
	in.DueDate = terms.Terms.DueDate(in.InvoiceDate)
	in.DiscountDueDate = terms.Terms.DiscountDueDate(in.InvoiceDate)
	if in.DiscountDueDate != nil && terms.Terms.DiscountPercent != nil {
		pct := int64(*terms.Terms.DiscountPercent)
		in.DiscountPercent = &pct
	} else {
		in.DiscountDueDate = nil
	}

	// Step 7 then 8: the customer row, then the gapless counter. The mint
	// holds the series until the commit, so it comes after every other lock
	// of the transaction and just before the insert.
	if err := store.LockCustomer(ctx, in.CustomerID); err != nil {
		return err
	}
	if in.Number, err = store.NextInvoiceNumber(ctx); err != nil {
		return err
	}
	if err := store.InsertFulfilmentInvoice(ctx, in); err != nil {
		return err
	}

	// The invoice's posting goes through the AR core (ADR 0005 8.2, 9.3): the
	// balanced entry (DR 1020 for the total, which the core adds, and the cost
	// relieved from inventory), the subledger debit, invoices.gl_entry_id.
	if s.account != nil {
		legs := []gl.Leg{{AccountCode: gl.AccountCodeCOGS, Description: "Cost of Goods Sold", Debit: in.CostCents}}
		for _, r := range in.Revenue {
			legs = append(legs, gl.Leg{AccountCode: r.AccountCode, Description: "Revenue " + r.AccountCode, Credit: r.Cents})
		}
		legs = append(legs,
			gl.Leg{AccountCode: gl.AccountCodeSalesTax, Description: "Sales Tax Payable", Credit: in.TaxCents},
			gl.Leg{AccountCode: gl.AccountCodeInventory, Description: "Inventory", Credit: in.CostCents},
		)
		fx, err := s.account.PostInvoice(ctx, account.PostInvoiceIn{InvoiceID: in.ID, CustomerID: in.CustomerID, Number: in.Number,
			Currency: in.Currency, TotalCents: in.TotalCents, On: in.InvoiceDate, Actor: in.Actor, Legs: legs})
		if err != nil {
			return mapPostingError(fmt.Errorf("failed to post the invoice: %w", err))
		}
		in.Effects = fx
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action: "invoice.created", EntityType: "invoice", EntityID: in.ID, UserID: in.Actor,
			Changes: map[string]interface{}{
				"number": in.Number, "customer_id": in.CustomerID, "order_id": in.OrderID, "total_cents": in.TotalCents,
				"cost_cents": in.CostCents, "delivery_id": in.DeliveryID,
			},
		}); err != nil {
			return fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	return nil
}
