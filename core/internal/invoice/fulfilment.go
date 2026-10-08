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
	PriceSource        string
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
// the cost of goods sold.
type FulfilmentInvoice struct {
	ID         uuid.UUID
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
}

// FulfilmentStore is the repository half the fulfilment invoice needs; the
// Postgres repository implements it.
type FulfilmentStore interface {
	InsertFulfilmentInvoice(ctx context.Context, in *FulfilmentInvoice) error
	SetInvoiceGLEntry(ctx context.Context, invoiceID, entryID uuid.UUID) error
}

// CreateFulfilmentInvoice writes the invoice of an order fulfilment inside the
// caller's transaction (ADR 0005 5.6 step 5 and 6, 8.2): the invoice with its
// lines, the balanced invoice entry through gl.PostEntry (DR 1020 total and
// 5010 cost; CR each revenue account, 2020 tax and 1030 cost), and the AR
// subledger debit through today's PostTransaction. It refuses to run outside
// a transaction. Until C2-4 moves both postings into the AR core this is the
// path; the events are the caller's, written last.
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
	if err := store.InsertFulfilmentInvoice(ctx, in); err != nil {
		return err
	}

	// The invoice entry: the total and the cost relieved from inventory.
	legs := []gl.Leg{
		{AccountCode: gl.AccountCodeAR, Description: "Accounts Receivable", Debit: in.TotalCents},
		{AccountCode: gl.AccountCodeCOGS, Description: "Cost of Goods Sold", Debit: in.CostCents},
	}
	for _, r := range in.Revenue {
		legs = append(legs, gl.Leg{AccountCode: r.AccountCode, Description: "Revenue " + r.AccountCode, Credit: r.Cents})
	}
	legs = append(legs,
		gl.Leg{AccountCode: gl.AccountCodeSalesTax, Description: "Sales Tax Payable", Credit: in.TaxCents},
		gl.Leg{AccountCode: gl.AccountCodeInventory, Description: "Inventory", Credit: in.CostCents},
	)
	invID := in.ID
	entry, err := s.gl.PostEntry(ctx, gl.PostingInput{
		EntryDate: in.InvoiceDate, Memo: fmt.Sprintf("Invoice %s", in.ID), Source: gl.SourceInvoice,
		SourceRefID: &invID, Currency: in.Currency, PostedBy: in.Actor, Legs: legs,
	})
	if err != nil {
		return fmt.Errorf("failed to post the invoice entry: %w", err)
	}
	if entry != nil {
		if err := store.SetInvoiceGLEntry(ctx, in.ID, entry.ID); err != nil {
			return err
		}
	}

	if s.account != nil {
		if _, err := s.account.PostTransaction(ctx, in.CustomerID, account.TransactionTypeInvoice, in.TotalCents, &invID, "Invoice #"+in.ID.String()); err != nil {
			return fmt.Errorf("failed to post the invoice to the account ledger: %w", err)
		}
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action: "invoice.created", EntityType: "invoice", EntityID: in.ID, UserID: in.Actor,
			Changes: map[string]interface{}{
				"customer_id": in.CustomerID, "order_id": in.OrderID, "total_cents": in.TotalCents,
				"cost_cents": in.CostCents, "delivery_id": in.DeliveryID,
			},
		}); err != nil {
			return fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	return nil
}
