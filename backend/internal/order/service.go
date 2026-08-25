// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// ExposureGate is the narrow pre-ship gate the order module depends on,
// implemented by pricing.PostgresExposureChecker. RequireClearForOrder returns
// a non-nil error when the order's source quote has unresolved index exposure
// (ACK_REQUIRED / BLOCKED), blocking confirm/fulfill until acknowledged or
// overridden.
type ExposureGate interface {
	RequireClearForOrder(ctx context.Context, orderID uuid.UUID) error
}

// ExposureOverrider records an explicit owner override of the pre-ship gate,
// implemented by an adapter over pricing.ExposureService. The override writes
// an OVERRIDDEN exposure event + audit entry, after which the gate clears.
type ExposureOverrider interface {
	OverrideForOrder(ctx context.Context, orderID uuid.UUID, notes, actor, role string) error
}

type Service struct {
	repo              Repository
	db                *database.DB
	inventorySvc      *inventory.Service
	invoiceSvc        *invoice.Service
	customerSvc       *customer.Service
	poSvc             *purchase_order.Service
	auditLog          *audit.Logger
	exposureGate      ExposureGate
	exposureOverrider ExposureOverrider
}

func NewService(repo Repository, inventorySvc *inventory.Service, invoiceSvc *invoice.Service, customerSvc *customer.Service, poSvc *purchase_order.Service, db ...*database.DB) *Service {
	s := &Service{
		repo:         repo,
		inventorySvc: inventorySvc,
		invoiceSvc:   invoiceSvc,
		customerSvc:  customerSvc,
		poSvc:        poSvc,
	}
	if len(db) > 0 {
		s.db = db[0]
	}
	return s
}

// WithAuditLog sets the audit logger for financial operation tracking.
func (s *Service) WithAuditLog(l *audit.Logger) *Service {
	s.auditLog = l
	return s
}

// WithExposureGate wires the lumber-index pre-ship gate. Optional: nil
// disables exposure gating entirely (e.g. tests, or builds without pricing).
func (s *Service) WithExposureGate(gate ExposureGate, overrider ExposureOverrider) *Service {
	s.exposureGate = gate
	s.exposureOverrider = overrider
	return s
}

func (s *Service) CreateOrder(ctx context.Context, req CreateOrderRequest) (*Order, error) {
	// 1. Validate inputs
	if req.CustomerID == uuid.Nil {
		return nil, fmt.Errorf("customer_id is required")
	}

	if len(req.Lines) == 0 {
		return nil, fmt.Errorf("order must have at least one line item")
	}

	o := &Order{
		CustomerID: req.CustomerID,
		QuoteID:    req.QuoteID,
		Status:     StatusDraft,
	}

	// Auto-populate salesperson from the customer's assigned rep
	cust, err := s.customerSvc.GetCustomer(ctx, req.CustomerID)
	if err == nil && cust.SalespersonID != nil {
		o.SalespersonID = cust.SalespersonID
	}

	var totalCents int64
	for _, l := range req.Lines {
		if l.Quantity <= 0 {
			return nil, fmt.Errorf("line quantity must be positive")
		}
		if l.PriceEach < 0 {
			return nil, fmt.Errorf("line price must be non-negative")
		}

		line := OrderLine{
			ID:               uuid.New(), // Generate ID upfront for linking
			ProductID:        l.ProductID,
			Quantity:         l.Quantity,
			PriceEach:        l.PriceEach,
			IsSpecialOrder:   l.IsSpecialOrder,
			VendorID:         l.VendorID,
			SpecialOrderCost: l.SpecialOrderCost,
		}
		o.Lines = append(o.Lines, line)
		totalCents += int64(math.Round(l.Quantity * float64(l.PriceEach)))
	}
	o.TotalAmount = totalCents

	// 2. Persist Order + POs in a single transaction
	if s.db != nil {
		err := s.db.RunInTx(ctx, func(txCtx context.Context) error {
			if err := s.repo.CreateOrder(txCtx, o); err != nil {
				return fmt.Errorf("failed to create order: %w", err)
			}
			for _, line := range o.Lines {
				if line.IsSpecialOrder && line.VendorID != nil {
					description := fmt.Sprintf("Special Order for Customer %s (Order %s)", o.CustomerID, o.ID)
					// TODO: align with int64 cents — CreateFromSOLine accepts float64 dollars
					soCostDollars := float64(line.SpecialOrderCost) / 100.0
					if err := s.poSvc.CreateFromSOLine(txCtx, line.ID, line.VendorID, description, line.Quantity, soCostDollars); err != nil {
						return fmt.Errorf("failed to create PO for special order line: %w", err)
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		// Fallback for tests without DB handle
		if err := s.repo.CreateOrder(ctx, o); err != nil {
			return nil, fmt.Errorf("failed to create order: %w", err)
		}
		for _, line := range o.Lines {
			if line.IsSpecialOrder && line.VendorID != nil {
				description := fmt.Sprintf("Special Order for Customer %s (Order %s)", o.CustomerID, o.ID)
				// TODO: align with int64 cents — CreateFromSOLine accepts float64 dollars
				soCostDollars := float64(line.SpecialOrderCost) / 100.0
				if err := s.poSvc.CreateFromSOLine(ctx, line.ID, line.VendorID, description, line.Quantity, soCostDollars); err != nil {
					return nil, fmt.Errorf("failed to create PO for special order line: %w", err)
				}
			}
		}
	}

	return o, nil
}

func (s *Service) ConfirmOrder(ctx context.Context, id uuid.UUID) error {
	// 1. Get Order
	o, err := s.repo.GetOrder(ctx, id)
	if err != nil {
		return err
	}

	if o.Status != StatusDraft {
		return fmt.Errorf("cannot confirm order in status %s", o.Status)
	}

	// 1.25 Pre-ship exposure gate: block confirm when the source quote has
	// unresolved lumber-index exposure (ACK_REQUIRED / BLOCKED). Cleared via
	// acknowledgment or an explicit owner override.
	if s.exposureGate != nil {
		if err := s.exposureGate.RequireClearForOrder(ctx, id); err != nil {
			return err
		}
	}

	// 1.5 Check Credit Limit
	cust, err := s.customerSvc.GetCustomer(ctx, o.CustomerID)
	if err != nil {
		return fmt.Errorf("failed to get customer details: %w", err)
	}

	// If a credit limit is set and the live open balance + this order would
	// exceed it, place the order ON HOLD.
	over, err := s.overCreditLimit(ctx, o.CustomerID, cust.CreditLimit, o.TotalAmount)
	if err != nil {
		return err
	}
	if over {
		if err := s.repo.UpdateStatus(ctx, id, StatusOnHold); err != nil {
			return fmt.Errorf("failed to update order status to ON_HOLD: %w", err)
		}
		return fmt.Errorf("credit limit exceeded: order placed ON HOLD")
	}

	// 2. Wrap inventory allocation + status update in a single transaction
	txFn := func(txCtx context.Context) error {
		var allocated []OrderLine

		for _, line := range o.Lines {
			if err := s.inventorySvc.Allocate(txCtx, line.ProductID, line.Quantity); err != nil {
				// Rollback previous allocations within the tx
				for _, prev := range allocated {
					_ = s.inventorySvc.Release(txCtx, prev.ProductID, prev.Quantity)
				}
				return fmt.Errorf("failed to allocate stock for product %s: %w", line.ProductID, err)
			}
			allocated = append(allocated, line)
		}

		// 3. Update Status
		if err := s.repo.UpdateStatus(txCtx, id, StatusConfirmed); err != nil {
			return fmt.Errorf("failed to update order status: %w", err)
		}

		return nil
	}

	if s.db != nil {
		if err := s.db.RunInTx(ctx, txFn); err != nil {
			return err
		}
	} else {
		// Fallback for tests without DB handle
		if err := txFn(ctx); err != nil {
			return err
		}
	}

	// Audit log: order confirmed (non-transactional, after commit)
	if s.auditLog != nil {
		s.auditLog.Log(ctx, audit.Entry{
			Action:     "order.confirmed",
			EntityType: "order",
			EntityID:   id,
			Changes: map[string]interface{}{
				"customer_id":  o.CustomerID,
				"total_amount": o.TotalAmount,
				"line_count":   len(o.Lines),
			},
		})
	}

	return nil
}

// ErrOrderAlreadyCancelled is returned when a cancel is attempted on an order
// that is already CANCELLED. It is a distinct error so callers can render it as
// a 409 rather than a 500 — cancelling twice is a client mistake, not a server
// fault, and it must not be allowed to look like a fresh cancellation.
var ErrOrderAlreadyCancelled = errors.New("order is already cancelled")

// ErrOrderNotCancellable is returned when the order's current status forbids
// cancellation — today that means FULFILLED, which has already shipped stock,
// issued an invoice and posted to AR/GL. Reversing that is a credit memo, not
// a cancel, and pretending otherwise would leave the ledger and the yard
// disagreeing with the order.
var ErrOrderNotCancellable = errors.New("order cannot be cancelled in its current status")

// CancelOrder moves an order to CANCELLED, releasing any stock that
// ConfirmOrder allocated.
//
// The state machine this respects is the one ConfirmOrder and FulfillOrder
// already enforce:
//
//	DRAFT     -> CANCELLED   nothing allocated, nothing invoiced
//	ON_HOLD   -> CANCELLED   ConfirmOrder sets ON_HOLD *before* allocating
//	                         (credit-limit branch returns early), so there is
//	                         no allocation to release
//	CONFIRMED -> CANCELLED   stock is allocated; release it in the same tx as
//	                         the status write, or a cancel that half-failed
//	                         would strand allocated inventory nobody can sell
//	FULFILLED -> refused     ErrOrderNotCancellable
//	CANCELLED -> refused     ErrOrderAlreadyCancelled (not idempotent on
//	                         purpose: a second "cancelled" 200 tells a caller
//	                         it just did something it did not do)
func (s *Service) CancelOrder(ctx context.Context, id uuid.UUID, reason string) error {
	o, err := s.repo.GetOrder(ctx, id)
	if err != nil {
		return err
	}

	switch o.Status {
	case StatusCancelled:
		return ErrOrderAlreadyCancelled
	case StatusDraft, StatusOnHold, StatusConfirmed:
		// cancellable
	default:
		return fmt.Errorf("%w: %s", ErrOrderNotCancellable, o.Status)
	}

	// Only a CONFIRMED order holds an allocation. Releasing on DRAFT/ON_HOLD
	// would credit stock that was never taken.
	releaseStock := o.Status == StatusConfirmed && s.inventorySvc != nil

	txFn := func(txCtx context.Context) error {
		if releaseStock {
			for _, line := range o.Lines {
				err := s.inventorySvc.Release(txCtx, line.ProductID, line.Quantity)
				switch {
				case err == nil:
					// released
				case errors.Is(err, inventory.ErrNothingAllocated):
					// Nothing to give back. A CONFIRMED order is *supposed* to
					// hold an allocation, but one can be absent legitimately —
					// stock adjusted out from under it, an order confirmed
					// before allocation existed, or a seeded fixture. Failing
					// the cancellation here would mean a customer cannot cancel
					// because of a bookkeeping mismatch they did not cause, and
					// would leave the order stuck CONFIRMED forever.
					//
					// This is deliberately narrow: only the "nothing allocated"
					// sentinel is tolerated. A real inventory failure still
					// aborts the transaction and the cancellation.
					slog.Warn("cancel: no allocation to release",
						"order_id", id, "product_id", line.ProductID, "quantity", line.Quantity)
				default:
					return fmt.Errorf("failed to release stock for product %s: %w", line.ProductID, err)
				}
			}
		}
		if err := s.repo.UpdateStatus(txCtx, id, StatusCancelled); err != nil {
			return fmt.Errorf("failed to update order status: %w", err)
		}
		return nil
	}

	if s.db != nil {
		if err := s.db.RunInTx(ctx, txFn); err != nil {
			return err
		}
	} else {
		// Fallback for tests without DB handle
		if err := txFn(ctx); err != nil {
			return err
		}
	}

	if s.auditLog != nil {
		s.auditLog.Log(ctx, audit.Entry{
			Action:     "order.cancelled",
			EntityType: "order",
			EntityID:   id,
			Changes: map[string]interface{}{
				"customer_id":     o.CustomerID,
				"previous_status": string(o.Status),
				"total_amount":    o.TotalAmount,
				"stock_released":  releaseStock,
				"reason":          reason,
			},
		})
	}

	return nil
}

// CheckExposureGate reports whether the order is currently blocked by the
// lumber-index pre-ship gate. Returns nil when clear (or gating disabled).
func (s *Service) CheckExposureGate(ctx context.Context, id uuid.UUID) error {
	if s.exposureGate == nil {
		return nil
	}
	return s.exposureGate.RequireClearForOrder(ctx, id)
}

// OverrideExposure records an explicit owner override of the pre-ship gate for
// an order, writing an OVERRIDDEN exposure event + audit entry via the pricing
// service. After this succeeds the gate clears for the order's source quote.
func (s *Service) OverrideExposure(ctx context.Context, id uuid.UUID, notes, actor, role string) error {
	if s.exposureOverrider == nil {
		return fmt.Errorf("exposure override not available")
	}
	if len(notes) < 10 {
		return fmt.Errorf("override notes must be at least 10 characters")
	}
	if err := s.exposureOverrider.OverrideForOrder(ctx, id, notes, actor, role); err != nil {
		return err
	}
	// Mirror into the order audit trail so the override is discoverable from
	// the order even though the exposure event lives on the quote.
	if s.auditLog != nil {
		s.auditLog.Log(ctx, audit.Entry{
			Action:     "order.exposure.overridden",
			EntityType: "order",
			EntityID:   id,
			UserID:     actor,
			Changes: map[string]interface{}{
				"notes": notes,
				"role":  role,
			},
		})
	}
	return nil
}

// overCreditLimit reports whether posting orderTotalCents would push the
// customer past their credit limit. The current balance is computed live from
// open invoices — the denormalized customers.balance_due column is unmaintained
// (stale for seed data, never updated by invoicing/POS) and must not gate credit.
func (s *Service) overCreditLimit(ctx context.Context, customerID uuid.UUID, creditLimit float64, orderTotalCents int64) (bool, error) {
	if creditLimit <= 0 {
		return false, nil // no limit configured
	}
	openCents, err := s.invoiceSvc.GetCustomerOpenBalanceCents(ctx, customerID)
	if err != nil {
		return false, fmt.Errorf("failed to compute current balance: %w", err)
	}
	limitCents := int64(math.Round(creditLimit * 100))
	return openCents+orderTotalCents > limitCents, nil
}

func (s *Service) ListOrders(ctx context.Context) ([]Order, error) {
	return s.repo.ListOrders(ctx)
}

func (s *Service) ListOrdersPaginated(ctx context.Context, limit, offset int) ([]Order, int, error) {
	return s.repo.ListOrdersPaginated(ctx, limit, offset)
}

func (s *Service) GetOrder(ctx context.Context, id uuid.UUID) (*Order, error) {
	return s.repo.GetOrder(ctx, id)
}

func (s *Service) FulfillOrder(ctx context.Context, id uuid.UUID) error {
	// 1. Get Order
	o, err := s.repo.GetOrder(ctx, id)
	if err != nil {
		return err
	}

	if o.Status != StatusConfirmed {
		return fmt.Errorf("cannot fulfill order in status %s (must be CONFIRMED)", o.Status)
	}

	// 1.25 Pre-ship exposure gate (re-checked at fulfill in case the index
	// moved between confirm and ship).
	if s.exposureGate != nil {
		if err := s.exposureGate.RequireClearForOrder(ctx, id); err != nil {
			return err
		}
	}

	// 1.5 Check Credit Limit
	cust, err := s.customerSvc.GetCustomer(ctx, o.CustomerID)
	if err != nil {
		return fmt.Errorf("failed to get customer: %w", err)
	}
	over, err := s.overCreditLimit(ctx, o.CustomerID, cust.CreditLimit, o.TotalAmount)
	if err != nil {
		return err
	}
	if over {
		return fmt.Errorf("credit limit exceeded: order total %d cents would push the customer over their limit", o.TotalAmount)
	}

	// 2. Wrap all DB mutations in a single transaction:
	//    inventory fulfill, invoice creation, customer balance update, order status update.
	//    If any step fails, the entire transaction rolls back automatically.
	txFn := func(txCtx context.Context) error {
		// 2a. Fulfill Inventory
		var fulfilled []OrderLine
		for _, line := range o.Lines {
			if err := s.inventorySvc.Fulfill(txCtx, line.ProductID, line.Quantity); err != nil {
				// Rollback previous fulfillments within the tx
				for _, prev := range fulfilled {
					_ = s.inventorySvc.RevertFulfillment(txCtx, prev.ProductID, prev.Quantity)
				}
				return fmt.Errorf("failed to fulfill inventory for product %s: %w", line.ProductID, err)
			}
			fulfilled = append(fulfilled, line)
		}

		// 2b. Create the invoice + post it to the ledgers — but only if the order
		//     was not already invoiced (e.g. via the delivery-completion path).
		//     AR is summed from invoices, so a second invoice would double-bill
		//     the customer. Skipping is idempotent and safe.
		alreadyInvoiced, err := s.invoiceSvc.ExistsInvoiceForOrder(txCtx, o.ID)
		if err != nil {
			return fmt.Errorf("failed to check existing invoice: %w", err)
		}
		if !alreadyInvoiced {
			// PriceEach is already in cents. TotalAmount is deliberately NOT
			// carried over: the order total is PRE-TAX, and supplying it here
			// used to make CreateInvoice treat the invoice as already priced
			// and skip the tax calculation entirely. The lines are the input;
			// CreateInvoice recomputes subtotal/tax and sets the tax-inclusive
			// TotalAmount, exactly as the delivery-completion path does.
			inv := &invoice.Invoice{
				OrderID:    o.ID,
				CustomerID: o.CustomerID,
				BranchID:   o.BranchID, // so the invoice + its tax rate come from the order's branch
				Status:     invoice.InvoiceStatusUnpaid,
			}
			for _, ol := range o.Lines {
				inv.Lines = append(inv.Lines, invoice.InvoiceLine{
					ProductID: ol.ProductID,
					Quantity:  ol.Quantity,
					PriceEach: ol.PriceEach,
				})
			}
			if err := s.invoiceSvc.CreateInvoice(txCtx, inv); err != nil {
				return fmt.Errorf("failed to create invoice: %w", err)
			}

			// 2c. Post to the GL + customer AR subledger using the tax-inclusive
			//     invoice total (the single AR writer for this path). This
			//     replaces the old raw, pre-tax customers.balance_due bump that
			//     left the recorded balance disagreeing with the invoice total.
			if err := s.invoiceSvc.PostInvoiceToLedger(txCtx, inv); err != nil {
				return fmt.Errorf("failed to post invoice to ledgers: %w", err)
			}
		}

		// 2d. Update Order Status
		if err := s.repo.UpdateStatus(txCtx, id, StatusFulfilled); err != nil {
			return fmt.Errorf("failed to update order status: %w", err)
		}

		return nil
	}

	if s.db != nil {
		if err := s.db.RunInTx(ctx, txFn); err != nil {
			return err
		}
	} else {
		// Fallback for tests without DB handle
		if err := txFn(ctx); err != nil {
			return err
		}
	}

	// Audit log: order fulfilled (after commit). This is the money-moving step
	// — it issues the invoice, posts AR/GL, and depletes stock — so it belongs
	// in the financial audit trail alongside order.confirmed.
	if s.auditLog != nil {
		s.auditLog.Log(ctx, audit.Entry{
			Action:     "order.fulfilled",
			EntityType: "order",
			EntityID:   id,
			Changes: map[string]interface{}{
				"customer_id":  o.CustomerID,
				"total_amount": o.TotalAmount,
				"line_count":   len(o.Lines),
			},
		})
	}

	return nil
}
