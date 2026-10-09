// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/domain"
	"github.com/gablelbm/gable/internal/edi"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// salesVelocityLister is the narrow read interface RefreshReorderTargets needs.
// Sized to one method so unit tests can supply a fake without spinning up pgx.
type salesVelocityLister interface {
	ListSalesVelocity(ctx context.Context, lookbackDays int) ([]SalesVelocity, error)
}

// EventRecorder writes a domain event into the transactional outbox, as the
// last statement of the act's transaction (ADR 0003 section 2).
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// The events C4-1a writes (ADR 0008 section 10.1). purchase_order.received
// also drains the order module's back order queue (ADR 0005 5.4).
const (
	EventCreated = "purchase_order.created"
	EventSent    = "purchase_order.sent"
	EventReceived = "purchase_order.received"
	EventReorderRecommended = "reorder.recommended"
)

// TxRunner runs fn inside a transaction; *database.DB satisfies it, and a
// test can inject a gated runner (the recipe's saturation test).
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type Service struct {
	events       EventRecorder
	repo         *Repository
	db           *database.DB
	tx           TxRunner
	edi          *edi.Service
	inventorySvc *inventory.Service
	productSvc   *product.Service
	vendorSvc    *vendor.Service
	aiClient     *ai.Client
	velocityRepo salesVelocityLister
}

func NewService(repo *Repository, db *database.DB, ediSvc *edi.Service, inventorySvc *inventory.Service, productSvc *product.Service, vendorSvc *vendor.Service) *Service {
	return &Service{repo: repo, db: db, tx: db, edi: ediSvc, inventorySvc: inventorySvc, productSvc: productSvc, vendorSvc: vendorSvc}
}

// WithTxRunner swaps the transaction runner (the recipe's gated saturation
// test injects one; serve keeps the database).
func (s *Service) WithTxRunner(r TxRunner) *Service {
	s.tx = r
	return s
}

// WithOutbox wires the outbox the purchasing acts write their events to.
// Optional: nil writes no event (unit tests).
func (s *Service) WithOutbox(events EventRecorder) *Service {
	s.events = events
	return s
}

// WithVelocityRepo wires the sales-velocity reader used by both
// RefreshReorderTargets and the recommendation engine.
func (s *Service) WithVelocityRepo(v salesVelocityLister) *Service {
	s.velocityRepo = v
	return s
}

// WithAIClient sets the AI client for freight invoice extraction.
func (s *Service) WithAIClient(c *ai.Client) {
	s.aiClient = c
}

// createEvent writes the purchasing acts' event data: a small summary,
// never the document (ADR 0008 section 10.1).
func poEventData(po *PurchaseOrder, fromStatus string) json.RawMessage {
	data := map[string]any{
		"number": po.Number, "vendor_id": po.VendorID, "branch_id": po.BranchID,
		"status": po.Status, "revision": po.Revision, "currency": po.Currency,
	}
	if po.LineCount > 0 || len(po.Lines) > 0 {
		var total httpx.Cents
		for _, l := range po.Lines {
			total += l.LineTotalCents
		}
		data["total_cents"] = int64(total)
	}
	if fromStatus != "" {
		data["from_status"] = fromStatus
	}
	raw, _ := json.Marshal(data)
	return raw
}

func (s *Service) recordEvent(ctx context.Context, eventType string, entityID uuid.UUID, branch uuid.UUID, data json.RawMessage) error {
	if s.events == nil {
		return nil
	}
	b := branch
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: "purchase_order", EntityID: entityID, BranchID: &b, Data: data,
	})
}

// qtyFloat and priceFloat read the internal float seams (the EDI demo, the
// vendor statistics) off the wire types.
func qtyFloat(q httpx.Quantity) float64 { return float64(int64(q)) / 10000 }
func priceFloat(p httpx.Price) float64  { return float64(int64(p)) / 10000 }

// ErrUnitNotStockUnit is the unit hold (ADR 0008 section 1): while the unit
// catalogue has not landed, a purchase line is held to the product's
// stocking unit, the pair 1 and 1, as ADR 0005 held sales lines.
var ErrUnitNotStockUnit = &httpx.Error{Status: 409, Code: httpx.CodeConflict,
	Message: "a purchase line is held to the product's stocking unit until the unit catalogue lands",
	Details: []httpx.FieldError{httpx.Blocker("unit_not_stock_unit",
		"a purchase line is held to the product's stocking unit until the unit catalogue lands")}}

// ErrEmptyPurchaseOrder and friends are the submit guards (ADR 0008 6.2's
// vocabulary, on today's states).
var (
	ErrVendorRequired = &httpx.Error{Status: 409, Code: httpx.CodeConflict,
		Message: "cannot submit PO without a vendor",
		Details: []httpx.FieldError{httpx.Blocker("vendor_required", "a submitted purchase order names a vendor")}}
)

// CreatePO validates and writes a purchase order from a parsed draft: one
// transaction for the header, its lines and the creation event (which is
// the last statement). The unit hold settles each line against its
// product's stocking unit inside the transaction.
func (s *Service) CreatePO(ctx context.Context, draft *CreateDraft) (*PurchaseOrder, error) {
	po := &PurchaseOrder{
		ID:       uuid.New(),
		VendorID: &draft.VendorID,
		Status:   StatusDraft.Wire(),
		Source:   draft.Source.Wire(),
		Currency: draft.Currency,
		BranchID: uuid.Nil,
	}
	if draft.BranchID != nil {
		po.BranchID = *draft.BranchID
	}
	err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		// The unit hold and the defaults that need the product.
		stocking := map[uuid.UUID]string{}
		unitFor := func(l *CreateLineDraft, i int) error {
			if l.UOM == "" {
				if l.ProductID == nil {
					return httpx.BadRequest("lines["+fmt.Sprint(i)+"].uom is required",
						httpx.FieldError{Field: fmt.Sprintf("lines[%d].uom", i), Message: "is required on a line with no product"})
				}
				uom, ok := stocking[*l.ProductID]
				if !ok {
					var err error
					if uom, err = s.repo.ProductStockingUnit(txCtx, *l.ProductID); err != nil {
						if errors.Is(err, ErrNotFound) {
							return httpx.BadRequest(fmt.Sprintf("lines[%d].product_id names a record that does not exist", i),
								httpx.FieldError{Field: fmt.Sprintf("lines[%d].product_id", i), Message: "names a record that does not exist"})
						}
						return err
					}
					stocking[*l.ProductID] = uom
				}
				l.UOM = uom
			}
			if l.PriceUOM == "" {
				l.PriceUOM = l.UOM
			}
			if l.UOMQty == 0 {
				l.UOMQty = one
			}
			if l.PriceUOMQty == 0 {
				l.PriceUOMQty = one
			}
			// The hold: the line's unit is the product's stocking unit, the
			// pair 1 and 1 and the price unit the line's own (ADR 0008
			// section 1, before C3-2A-units).
			if l.ProductID != nil {
				stock, ok := stocking[*l.ProductID]
				if !ok {
					var err error
					if stock, err = s.repo.ProductStockingUnit(txCtx, *l.ProductID); err != nil {
						return err
					}
					stocking[*l.ProductID] = stock
				}
				if l.UOM != stock || l.PriceUOM != l.UOM || l.UOMQty != one || l.PriceUOMQty != one {
					return ErrUnitNotStockUnit
				}
			}
			return nil
		}
		if err := s.repo.CreatePO(txCtx, po); err != nil {
			return err
		}
		for i := range draft.Lines {
			l := &draft.Lines[i]
			if err := unitFor(l, i); err != nil {
				return err
			}
			line := &PurchaseOrderLine{
				ID:          uuid.New(),
				POID:        po.ID,
				ProductID:   l.ProductID,
				Description: l.Description,
				Quantity:    l.Quantity,
				UOM:         l.UOM,
				PriceUOM:    l.PriceUOM,
				UOMQty:      l.UOMQty,
				PriceUOMQty: l.PriceUOMQty,
				UnitCostTenThousandths: l.UnitCostTenThousandths,
				Position:    i + 1,
				LinkedSOLineID: l.LinkedSOLineID,
			}
			if l.ProductID != nil {
				stock := stocking[*l.ProductID]
				line.StockUOM = &stock
				q := l.Quantity
				line.StockQuantity = &q
			}
			if err := s.repo.AddPOLine(txCtx, line); err != nil {
				return err
			}
			po.Lines = append(po.Lines, *line)
		}
		po.LineCount = len(po.Lines)
		// The event is the last write (ADR 0003 section 2).
		return s.recordEvent(txCtx, EventCreated, po.ID, po.BranchID, poEventData(po, ""))
	})
	if err != nil {
		return nil, err
	}
	return po, nil
}

// LineInput is the internal create line seam: the A2A receiver and the
// special order path build these, with floats from their own wires.
type LineInput struct {
	ProductID      *uuid.UUID
	Description    string
	Quantity       httpx.Quantity
	UnitCost       httpx.Price
	LinkedSOLineID *uuid.UUID
}

// CreateFromLines is the internal create path for callers that are not the
// HTTP boundary: the same transaction, unit hold and event.
func (s *Service) CreateFromLines(ctx context.Context, vendorID uuid.UUID, source Source, lines []LineInput, branchID *uuid.UUID) (*PurchaseOrder, error) {
	draft := &CreateDraft{VendorID: vendorID, Source: source, Currency: "USD", BranchID: branchID}
	for _, l := range lines {
		draft.Lines = append(draft.Lines, CreateLineDraft{
			ProductID: l.ProductID, Description: l.Description, Quantity: l.Quantity,
			UnitCostTenThousandths: l.UnitCost, LinkedSOLineID: l.LinkedSOLineID,
		})
	}
	return s.CreatePO(ctx, draft)
}

// ErrA2ADuplicate answers the A2A seam's 409: the idempotency log row
// already exists, so this webhook's purchase order was created before.
var ErrA2ADuplicate = errors.New("a2a idempotency key already processed")

// CreatePOFromA2A is the A2A seam's create (ADR 0008 section 11): the
// idempotency log row is inserted first, ON CONFLICT DO NOTHING, in the
// same transaction as the purchase order it creates; a conflict means the
// key was processed and answers duplicate=true, so no second purchase
// order can appear however often or concurrently the webhook arrives.
// The event and the log row's created_po_id share the transaction.
func (s *Service) CreatePOFromA2A(ctx context.Context, idempotencyKey string, webhook *InboundPOWebhook, vendorID uuid.UUID, lines []LineInput) (*PurchaseOrder, bool, error) {
	var out *PurchaseOrder
	err := s.db.RunInTx(ctx, func(txCtx context.Context) error {
		inserted, err := s.repo.InsertA2ALog(txCtx, idempotencyKey, webhook.EventType, webhook.Payload, webhook.TraceID)
		if err != nil {
			return err
		}
		if !inserted {
			out = nil
			return ErrA2ADuplicate
		}
		po, err := s.CreateFromLines(txCtx, vendorID, SourceA2A, lines, nil)
		if err != nil {
			return err
		}
		if err := s.repo.SetA2ALogPO(txCtx, idempotencyKey, po.ID); err != nil {
			return err
		}
		out = po
		return nil
	})
	if errors.Is(err, ErrA2ADuplicate) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return out, false, nil
}

// CreateFromSOLine creates or updates a DRAFT PO for the vendor of the special
// order item, linking its line to the order line it serves and naming the
// product, so the receipt puts the stock on hand and the order module's
// release picks it up (ADR 0005 5.4). The header and the line are one
// transaction: a line that cannot be linked leaves no empty header behind.
func (s *Service) CreateFromSOLine(ctx context.Context, soLineId uuid.UUID, productID *uuid.UUID, vendorId *uuid.UUID, description string, qty float64, cost float64) error {
	quantity, err := floatQuantity(qty)
	if err != nil {
		return err
	}
	unitCost, err := floatPrice(cost)
	if err != nil {
		return err
	}
	return s.db.RunInTx(ctx, func(txCtx context.Context) error {
		var po *PurchaseOrder
		if vendorId != nil {
			po, _ = s.repo.GetDraftPOByVendor(txCtx, vendorId)
		}
		if po == nil {
			if vendorId == nil {
				return fmt.Errorf("a special order purchase order needs a vendor")
			}
			po = &PurchaseOrder{
				ID:       uuid.New(),
				VendorID: vendorId,
				Status:   StatusDraft.Wire(),
				Source:   SourceSpecialOrder.Wire(),
			}
			if err := s.repo.CreatePO(txCtx, po); err != nil {
				return fmt.Errorf("failed to create PO: %w", err)
			}
		}
		line := &PurchaseOrderLine{
			ID: uuid.New(), POID: po.ID, ProductID: productID, Description: description,
			Quantity: quantity, UnitCostTenThousandths: unitCost, LinkedSOLineID: &soLineId,
			UOMQty: one, PriceUOMQty: one,
		}
		if productID != nil {
			uom, err := s.repo.ProductStockingUnit(txCtx, *productID)
			if err != nil {
				return err
			}
			line.UOM, line.PriceUOM, line.StockUOM = uom, uom, &uom
			q := quantity
			line.StockQuantity = &q
		}
		if err := s.repo.AddPOLine(txCtx, line); err != nil {
			return fmt.Errorf("failed to add PO line: %w", err)
		}
		return nil
	})
}

// quantityFromFloat converts an internal float figure to the wire's scale 4.
func quantityFromFloat(f float64) httpx.Quantity {
	q, err := httpx.ParseQuantity(fmt.Sprintf("%.4f", f))
	if err != nil {
		return 0
	}
	return q
}

// floatQuantity converts an internal float seam (the A2A payload, the
// special order path) to the wire's exact scale 4, refusing the values it
// cannot carry exactly.
func floatQuantity(f float64) (httpx.Quantity, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("quantity %v is not a finite decimal", f)
	}
	q, err := httpx.ParseQuantity(fmt.Sprintf("%.4f", f))
	if err != nil {
		return 0, fmt.Errorf("quantity %v is not exact at scale 4", f)
	}
	return q, nil
}

// floatPrice converts an internal float cost seam to scale 4.
func floatPrice(f float64) (httpx.Price, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("unit cost %v is not a finite decimal", f)
	}
	p, err := httpx.ParsePrice(fmt.Sprintf("%.4f", f))
	if err != nil {
		return 0, fmt.Errorf("unit cost %v is not exact at scale 4", f)
	}
	return p, nil
}

// ListPOs is the list page: the filters, the branch wall and the cursor
// position, with the flag that says another page follows.
func (s *Service) ListPOs(ctx context.Context, f ListFilter) ([]PurchaseOrderSummary, bool, error) {
	return s.repo.ListPOsPage(ctx, f)
}

func (s *Service) CountPOs(ctx context.Context, f ListFilter) (int64, error) {
	return s.repo.CountPOs(ctx, f)
}

// GetSourceSummary returns PO counts grouped by source. Used by the
// purchasing dashboard's "% replenishments automated" widget.
func (s *Service) GetSourceSummary(ctx context.Context) (map[string]int, error) {
	return s.repo.GetSourceSummary(ctx)
}

func (s *Service) GetPO(ctx context.Context, id uuid.UUID) (*PurchaseOrder, error) {
	po, err := s.repo.GetPO(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, httpx.NotFound("no such purchase order")
	}
	return po, err
}

// GetPOBranch returns the branch the purchase order belongs to, or nil when
// there is no such purchase order.
func (s *Service) GetPOBranch(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
	return s.repo.GetPOBranch(ctx, id)
}

// SubmitPO sends a draft (ADR 0008 section 6.2's edge on today's states):
// the purchase order row is locked, the revision precondition and the
// status are checked under the lock, the vendor must be named, and the EDI
// demo write and the sent event follow. A purchase order that is not a
// draft is refused; the base commit sent a RECEIVED order again.
func (s *Service) SubmitPO(ctx context.Context, id uuid.UUID, ifMatch string, bodyRevision *int64) (*PurchaseOrder, error) {
	err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		po, err := s.repo.LockPO(txCtx, id)
		if errors.Is(err, ErrNotFound) {
			return httpx.NotFound("no such purchase order")
		}
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(po.Revision, ifMatch, bodyRevision); err != nil {
			return err
		}
		if po.Status != StatusDraft.Wire() {
			return httpx.InvalidStateTransition(fmt.Sprintf("a purchase order cannot be submitted from %s", po.Status))
		}
		if po.VendorID == nil {
			return ErrVendorRequired
		}
		now := time.Now().UTC()
		if err := s.repo.UpdatePOStatus(txCtx, id, StatusSent, &now, po.Revision); err != nil {
			return err
		}
		po.Status = StatusSent.Wire()
		po.Revision++
		po.SentAt = httpx.PtrTimestamp(&now)
		// The demo EDI write: replaced by the send outbox in C4-2 F.
		if s.edi != nil {
			lines, err := s.repo.LoadLines(txCtx, id)
			if err != nil {
				return err
			}
			poLines := make([]domain.POlineData, len(lines))
			for i, l := range lines {
				qty := qtyFloat(l.Quantity)
				cost := priceFloat(l.UnitCostTenThousandths)
				poLines[i] = domain.POlineData{
					LineNumber: i + 1,
					Quantity:   qty,
					Cost:       cost,
					ItemCode:   "UNKNOWN",
				}
			}
			poData := domain.POData{
				ID:       po.ID,
				PONumber: po.Number,
				VendorID: *po.VendorID,
				Lines:    poLines,
			}
			if err := s.edi.SendExamplePO(txCtx, poData); err != nil {
				return fmt.Errorf("failed to send EDI: %w", err)
			}
		}
		return s.recordEvent(txCtx, EventSent, po.ID, po.BranchID, poEventData(po, StatusDraft.Wire()))
	})
	if err != nil {
		return nil, err
	}
	return s.GetPO(ctx, id)
}

// ReceivePO processes goods receipt against a PO (the act C4-1a makes
// correct in place; C4-2 C's receipts replace its route). One transaction,
// in ADR 0008 section 4's order: the purchase order locked and its status
// and revision checked under the lock; each line's total received capped at
// its ordered quantity by the over receipt setting; the receipt's
// locations held to the purchase order's branch; the stock rows locked in
// (product, inventory id) order; the average moved under the product lock
// with Q read once before the stock write; the cost update's error
// returned, never dropped.
func (s *Service) ReceivePO(ctx context.Context, poID uuid.UUID, ifMatch string, bodyRevision *int64, lines []ReceiveLineDraft) (*PurchaseOrder, error) {
	byLine := map[uuid.UUID]ReceiveLineDraft{}
	for _, l := range lines {
		if _, dup := byLine[l.LineID]; dup {
			return nil, httpx.BadRequest("a line is received twice in one receipt",
				httpx.FieldError{Field: "lines", Message: "a line appears twice"})
		}
		byLine[l.LineID] = l
	}
	err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		// 1. Lock the purchase order; check the revision and the status.
		po, err := s.repo.LockPO(txCtx, poID)
		if errors.Is(err, ErrNotFound) {
			return httpx.NotFound("no such purchase order")
		}
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(po.Revision, ifMatch, bodyRevision); err != nil {
			return err
		}
		if po.Status != StatusSent.Wire() && po.Status != StatusPartialReceive.Wire() {
			return httpx.InvalidStateTransition(fmt.Sprintf("PO must be in sent or partial status to receive (current: %s)", po.Status))
		}
		poLines, err := s.repo.LoadLines(txCtx, poID)
		if err != nil {
			return err
		}
		lineMap := map[uuid.UUID]*PurchaseOrderLine{}
		for i := range poLines {
			lineMap[poLines[i].ID] = &poLines[i]
		}

		// 2. The over receipt rule: each line's total received, this receipt
		//    included, at most ordered x (1 + over_receipt_percent / 100).
		pct, err := s.repo.OverReceiptPercent(txCtx)
		if err != nil {
			return err
		}
		pctNum, okPct := new(big.Rat).SetString(pct)
		if !okPct {
			pctNum = new(big.Rat)
		}
		for _, rl := range lines {
			l, ok := lineMap[rl.LineID]
			if !ok {
				return httpx.BadRequest("lines names a line that is not on this purchase order",
					httpx.FieldError{Field: "lines", Message: "line " + rl.LineID.String() + " is not on this purchase order"})
			}
			// ordered x (1 + pct/100), at scale 4: cap = ordered x (100+pct) / 100.
			hundred := big.NewRat(100, 1)
			factor := new(big.Rat).Add(hundred, pctNum)
			factor.Quo(factor, hundred)
			capQty := new(big.Rat).Mul(new(big.Rat).SetInt64(int64(l.Quantity)), factor)
			total := new(big.Rat).Add(new(big.Rat).SetInt64(int64(l.QtyReceived)), new(big.Rat).SetInt64(int64(rl.QtyReceived)))
			if total.Cmp(capQty) > 0 {
				return &httpx.Error{Status: 409, Code: httpx.CodeConflict,
					Message: fmt.Sprintf("line %s would be received beyond its ordered quantity", rl.LineID),
					Details: []httpx.FieldError{httpx.Blocker("over_receipt",
						"a line's total received is at most its ordered quantity plus the over receipt percent")}}
			}
		}

		// 3. The receipt's locations must be in the purchase order's branch
		//    (ADR 0008 section 11's same branch rule).
		for i, rl := range lines {
			branch, ok, err := s.repo.LocationBranch(txCtx, rl.LocationID)
			if err != nil {
				return err
			}
			if !ok || branch != po.BranchID {
				return &httpx.Error{Status: 409, Code: httpx.CodeConflict,
					Message: fmt.Sprintf("lines[%d].location_id is in another branch than the purchase order", i),
					Details: []httpx.FieldError{httpx.Blocker("cross_branch",
						"a receipt's location must be in the purchase order's branch")}}
			}
		}

		// 4. The stock rows, locked in (product, inventory id) order: the
		//    inventory service's locked adjustment creates and locks.
		type receiptWrite struct {
			line     *PurchaseOrderLine
			received httpx.Quantity
			location uuid.UUID
		}
		writes := make([]receiptWrite, 0, len(lines))
		for _, rl := range lines {
			l := lineMap[rl.LineID]
			writes = append(writes, receiptWrite{line: l, received: rl.QtyReceived, location: rl.LocationID})
		}
		sort.Slice(writes, func(a, b int) bool {
			pa, pb := "", ""
			if writes[a].line.ProductID != nil {
				pa = writes[a].line.ProductID.String()
			}
			if writes[b].line.ProductID != nil {
				pb = writes[b].line.ProductID.String()
			}
			if pa != pb {
				return pa < pb
			}
			return writes[a].location.String() < writes[b].location.String()
		})

		// 5. Per line: the average under the product lock, Q read once
		//    before the stock write; then the stock itself.
		for _, w := range writes {
			if w.line.ProductID == nil {
				continue
			}
			if err := s.moveAverage(txCtx, *w.line.ProductID, w.line, w.received); err != nil {
				return err
			}
		}
		for _, w := range writes {
			newQtyReceived := w.line.QtyReceived + w.received
			if err := s.repo.UpdateLineReceived(txCtx, w.line.ID, newQtyReceived); err != nil {
				return fmt.Errorf("failed to update received qty: %w", err)
			}
			if w.line.ProductID != nil && s.inventorySvc != nil {
				if err := s.inventorySvc.ReceiveIntoLocation(txCtx, *w.line.ProductID, w.location, w.received); err != nil {
					return fmt.Errorf("failed to receive line %s into stock: %w", w.line.ID, err)
				}
			}
		}

		// 6. Derive the status; move the revision.
		allFullyReceived := true
		lineReceived := map[uuid.UUID]httpx.Quantity{}
		for _, w := range writes {
			lineReceived[w.line.ID] = w.line.QtyReceived + w.received
		}
		for _, l := range poLines {
			got := l.QtyReceived
			if r, ok := lineReceived[l.ID]; ok {
				got = r
			}
			if got < l.Quantity {
				allFullyReceived = false
			}
		}
		next := StatusPartialReceive
		if allFullyReceived {
			next = StatusReceived
		}
		if err := s.repo.SetPOStatus(txCtx, poID, next); err != nil {
			return err
		}
		po.Status = next.Wire()
		po.Revision++

		// 7. The event is the last write of the receive's own work: what it
		//    received and WHERE it landed, for the order module's back order
		//    release (ADR 0005 5.4): the branch of each line's location, not
		//    the purchase order's own.
		byBranch := map[uuid.UUID][]uuid.UUID{}
		for _, w := range writes {
			if w.line.ProductID == nil {
				continue
			}
			branch, ok, err := s.repo.LocationBranch(txCtx, w.location)
			if err != nil {
				return err
			}
			if !ok {
				branch = po.BranchID
			}
			byBranch[branch] = append(byBranch[branch], *w.line.ProductID)
		}
		if err := s.recordReceived(txCtx, po, byBranch); err != nil {
			return err
		}

		// 8. Vendor stats (C4-2 C moves them to the reorder refresh).
		s.updateVendorStats(txCtx, po, poLines, lineReceived)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetPO(ctx, poID)
}

// moveAverage recomputes the product's moving weighted average for one
// received line, under the product row's FOR UPDATE lock, with Q read once
// BEFORE the stock write (the base commit read it after, counting the
// receipt twice), computed exactly and rounded once to scale 4 (ADR 0008
// section 3.5), and the cost update's error returned, never dropped.
func (s *Service) moveAverage(ctx context.Context, productID uuid.UUID, line *PurchaseOrderLine, received httpx.Quantity) error {
	if s.productSvc == nil {
		return nil
	}
	value, err := httpx.Extend(received, line.UOMQty, line.PriceUOMQty, line.UnitCostTenThousandths)
	if err != nil {
		return err
	}
	newAvg, err := s.repo.MoveAverageUnderLock(ctx, productID, received, value)
	if err != nil {
		return err
	}
	if err := s.productSvc.UpdateAverageCost(ctx, productID, newAvg); err != nil {
		return fmt.Errorf("failed to update average cost: %w", err)
	}
	return nil
}

// updateVendorStats keeps the base commit's vendor statistics (C4-2 C
// replaces the (old + new) / 2 averaging with the reorder refresh's
// measured cache).
func (s *Service) updateVendorStats(ctx context.Context, po *PurchaseOrder, poLines []PurchaseOrderLine, lineReceived map[uuid.UUID]httpx.Quantity) {
	if s.vendorSvc == nil || po.VendorID == nil {
		return
	}
	leadTime := time.Since(po.CreatedAt.Time).Hours() / 24.0
	var totalOrdered, totalReceived float64
	var spend float64
	for _, l := range poLines {
		qty := qtyFloat(l.Quantity)
		got := l.QtyReceived
		if r, ok := lineReceived[l.ID]; ok {
			got = r
		}
		gotF := qtyFloat(got)
		cost := priceFloat(l.UnitCostTenThousandths)
		totalOrdered += qty
		totalReceived += gotF
		spend += gotF * cost
	}
	fillRate := 0.0
	if totalOrdered > 0 {
		fillRate = (totalReceived / totalOrdered) * 100
	}
	v, err := s.vendorSvc.GetVendor(ctx, *po.VendorID)
	if err != nil || v == nil {
		return
	}
	newLeadTime := (v.AverageLeadTimeDays + leadTime) / 2
	if v.AverageLeadTimeDays == 0 {
		newLeadTime = leadTime
	}
	newFillRate := (v.FillRate + fillRate) / 2
	if v.FillRate == 0 {
		newFillRate = fillRate
	}
	newSpend := v.TotalSpendYTD + spend
	_ = s.vendorSvc.UpdatePerformance(ctx, *po.VendorID, newLeadTime, newFillRate, newSpend)
}

// UploadFreightInvoice processes a freight invoice file, extracts data via AI,
// computes cost-weighted allocation across PO lines, and returns a preview.
// extractFreight runs AI freight-invoice extraction, encapsulating the
// graceful-degradation contract so it can be unit-tested without the DB-backed
// PO lookup: when AI is unconfigured it returns a hard error telling the user to
// enter the freight total manually — it never silently succeeds with a zero total.
func (s *Service) extractFreight(ctx context.Context, fileBytes []byte, contentType string) (*ai.FreightInvoiceResult, string, error) {
	if s.aiClient == nil || !s.aiClient.IsConfigured(ctx) {
		return nil, "", fmt.Errorf("AI service not configured — please enter the freight total manually")
	}
	result, raw, err := s.aiClient.ExtractFreightInvoice(ctx, fileBytes, contentType)
	if err != nil {
		return nil, raw, fmt.Errorf("AI extraction failed: %w", err)
	}
	if result == nil {
		return nil, raw, fmt.Errorf("AI extraction returned no result")
	}
	return result, raw, nil
}

func (s *Service) UploadFreightInvoice(ctx context.Context, poID uuid.UUID, fileBytes []byte, contentType string, filename string) (*FreightUploadResponse, error) {
	po, err := s.repo.GetPO(ctx, poID)
	if errors.Is(err, ErrNotFound) {
		return nil, httpx.NotFound("no such purchase order")
	}
	if err != nil {
		return nil, err
	}
	if po.Status != StatusReceived.Wire() && po.Status != StatusPartialReceive.Wire() {
		return nil, &httpx.Error{Status: 409, Code: httpx.CodeConflict,
			Message: fmt.Sprintf("PO must be in received or partial status to upload freight (current: %s)", po.Status),
			Details: []httpx.FieldError{httpx.Blocker("purchase_order_not_received",
				"freight is uploaded against a received purchase order")}}
	}

	// Save the file to disk
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = ".bin"
	}
	fileID := uuid.New().String()
	dir := filepath.Join("uploads", "freight")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create freight upload dir: %w", err)
	}
	savedPath := filepath.Join(dir, fileID+ext)
	if err := os.WriteFile(savedPath, fileBytes, 0o644); err != nil {
		return nil, fmt.Errorf("failed to save freight file: %w", err)
	}

	// Detect content type for AI if needed
	aiContentType := contentType
	if aiContentType == "" || aiContentType == "application/octet-stream" {
		aiContentType = http.DetectContentType(fileBytes)
	}

	// Extract freight data via AI. The degradation contract — unconfigured AI is a
	// hard error so the user enters the total manually — lives in extractFreight,
	// which is unit-tested independently of the DB-backed PO lookup above.
	result, aiRaw, err := s.extractFreight(ctx, fileBytes, aiContentType)
	if err != nil {
		return nil, err
	}
	carrierName := result.CarrierName
	invoiceNumber := result.InvoiceNumber
	totalAmountCents := int64(math.Round(result.TotalAmount * 100))

	if totalAmountCents <= 0 {
		return nil, fmt.Errorf("could not extract a valid freight amount from the invoice")
	}

	// Compute cost-weighted allocation across PO lines
	var totalReceivedCost float64
	for _, line := range po.Lines {
		totalReceivedCost += priceFloat(line.UnitCostTenThousandths) * qtyFloat(line.QtyReceived)
	}

	if totalReceivedCost <= 0 {
		return nil, fmt.Errorf("no received line costs to allocate freight against")
	}

	freightChargeID := uuid.New()
	fc := &FreightCharge{
		ID:               freightChargeID,
		POID:             poID,
		FilePath:         savedPath,
		OriginalFilename: filename,
		CarrierName:      carrierName,
		InvoiceNumber:    invoiceNumber,
		TotalAmountCents: totalAmountCents,
		AllocationMethod: "cost_weighted",
		Status:           FreightStatusPending,
		AIRawResponse:    aiRaw,
	}

	if err := s.repo.SaveFreightCharge(ctx, fc); err != nil {
		return nil, fmt.Errorf("failed to save freight charge: %w", err)
	}

	var allocations []FreightAllocation
	var allocatedTotal int64

	for i, line := range po.Lines {
		got := qtyFloat(line.QtyReceived)
		if got <= 0 {
			continue
		}

		lineCost := priceFloat(line.UnitCostTenThousandths) * got
		lineShare := lineCost / totalReceivedCost
		allocCents := int64(math.Round(float64(totalAmountCents) * lineShare))

		// Ensure rounding doesn't lose cents — adjust last allocation
		if i == len(po.Lines)-1 && allocatedTotal+allocCents != totalAmountCents {
			allocCents = totalAmountCents - allocatedTotal
		}

		perUnitCents := int64(0)
		if got > 0 {
			perUnitCents = int64(math.Round(float64(allocCents) / got))
		}

		a := FreightAllocation{
			ID:              uuid.New(),
			FreightChargeID: freightChargeID,
			POLineID:        line.ID,
			ProductID:       line.ProductID,
			AllocatedCents:  allocCents,
			PerUnitCents:    perUnitCents,
			Description:     line.Description,
		}
		allocations = append(allocations, a)
		allocatedTotal += allocCents
	}

	if err := s.repo.SaveFreightAllocations(ctx, allocations); err != nil {
		return nil, fmt.Errorf("failed to save freight allocations: %w", err)
	}

	fc.Allocations = allocations
	return &FreightUploadResponse{
		FreightCharge: *fc,
		Allocations:   allocations,
	}, nil
}

// ApplyFreightCharge applies a pending freight charge to product average costs.
func (s *Service) ApplyFreightCharge(ctx context.Context, poID uuid.UUID, freightChargeID uuid.UUID) error {
	fc, err := s.repo.GetFreightCharge(ctx, freightChargeID)
	if err != nil {
		return httpx.NotFound("no such freight charge")
	}

	if fc.POID != poID {
		return httpx.BadRequest("freight charge does not belong to this PO",
			httpx.FieldError{Field: "freightId", Message: "does not belong to this purchase order"})
	}

	if fc.Status != FreightStatusPending {
		return &httpx.Error{Status: 409, Code: httpx.CodeConflict,
			Message: "freight charge already applied",
			Details: []httpx.FieldError{httpx.Blocker("freight_already_applied", "the freight charge has been applied")}}
	}

	allocations, err := s.repo.GetFreightAllocations(ctx, freightChargeID)
	if err != nil {
		return fmt.Errorf("failed to load allocations: %w", err)
	}

	// Load PO to get received quantities per line
	po, err := s.repo.GetPO(ctx, poID)
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound("no such purchase order")
	}
	if err != nil {
		return err
	}

	lineMap := make(map[uuid.UUID]*PurchaseOrderLine)
	for i := range po.Lines {
		lineMap[po.Lines[i].ID] = &po.Lines[i]
	}

	if s.productSvc != nil {
		for _, alloc := range allocations {
			if alloc.ProductID == nil {
				continue
			}

			poLine, ok := lineMap[alloc.POLineID]
			if !ok || poLine.QtyReceived <= 0 {
				continue
			}

			prod, err := s.productSvc.GetProduct(ctx, *alloc.ProductID)
			if err != nil || prod == nil {
				continue
			}

			freightPerUnit := float64(alloc.PerUnitCents) / 100.0
			currentAvg := prod.AverageUnitCost
			totalQty := prod.TotalQuantity
			qtyReceived := qtyFloat(poLine.QtyReceived)

			if totalQty <= 0 {
				continue
			}

			// Add the freight cost into the weighted average
			newAvg := currentAvg + (qtyReceived*freightPerUnit)/totalQty
			if err := s.productSvc.UpdateAverageCost(ctx, *alloc.ProductID, newAvg); err != nil {
				return fmt.Errorf("failed to update average cost: %w", err)
			}
		}
	}

	return s.repo.UpdateFreightStatus(ctx, freightChargeID, FreightStatusApplied)
}

// GetFreightCharges returns all freight charges for a PO with their allocations.
func (s *Service) GetFreightCharges(ctx context.Context, poID uuid.UUID) ([]FreightCharge, error) {
	charges, err := s.repo.GetFreightCharges(ctx, poID)
	if err != nil {
		return nil, err
	}

	for i := range charges {
		allocs, err := s.repo.GetFreightAllocations(ctx, charges[i].ID)
		if err != nil {
			return nil, err
		}
		charges[i].Allocations = allocs
	}

	return charges, nil
}

// ReorderTargetProposal records the before/after of a single product's
// recomputed reorder target, now per product and branch (ADR 0008 10.2).
// Surfaced in dry-run mode so an operator can inspect proposed changes
// before flipping reorder.dry_run to false.
type ReorderTargetProposal struct {
	ProductID uuid.UUID `json:"product_id"`
	BranchID  uuid.UUID `json:"branch_id"`
	SKU       string    `json:"sku"`
	OldPoint  float64   `json:"old_point"`
	NewPoint  float64   `json:"new_point"`
	OldQty    float64   `json:"old_qty"`
	NewQty    float64   `json:"new_qty"`
	AvgDaily  float64   `json:"avg_daily"`
}

// RefreshResult is the JSON shape returned by RefreshReorderTargets and the
// manual-trigger HTTP endpoint.
type RefreshResult struct {
	DryRun             bool                    `json:"dry_run"`
	ProductsUpdated    int                     `json:"products_updated"`
	ProductsSkipped    int                     `json:"products_skipped"`
	RecommendationsNew int                     `json:"recommendations_new"`
	Proposals          []ReorderTargetProposal `json:"proposals"`
}

// maxProposalsPreview caps the dry-run response so a large catalog doesn't
// produce a megabyte response body. The aggregate counts are still accurate.
const maxProposalsPreview = 100

// RefreshReorderTargets recomputes the reorder targets per product and
// branch (ADR 0008 10.2: from this item on the refresh job writes per
// branch targets into stock_levels) and stores one recommendation per due
// product and branch with its payload (10.3), writing one
// reorder.recommended event per recommendation at the end of the run's
// transaction. The velocity is units issued per day over the lookback from
// the sales history (actual issue moves arrive with the stock ledger in
// C4-2 A); the lead time is the vendor's measured lead time when it has
// one, else the reorder.default_lead_time_days setting.
func (s *Service) RefreshReorderTargets(ctx context.Context, dryRun bool, lookbackDays int) (*RefreshResult, error) {
	if s.velocityRepo == nil {
		return nil, fmt.Errorf("velocity repository not configured")
	}
	if s.productSvc == nil {
		return nil, fmt.Errorf("product service not configured")
	}
	if lookbackDays <= 0 {
		lookbackDays = DefaultRecommendationConfig().LookbackDays
	}
	defaultLead, err := s.repo.SettingFloat(ctx, "reorder.default_lead_time_days", DefaultRecommendationConfig().DefaultLeadTimeDays)
	if err != nil {
		return nil, err
	}

	rows, err := s.repo.ReorderScan(ctx, lookbackDays)
	if err != nil {
		return nil, fmt.Errorf("reorder scan: %w", err)
	}

	vendors, err := s.vendorMap(ctx)
	if err != nil {
		return nil, err
	}
	products, err := s.productSvc.ListProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	productByID := map[uuid.UUID]product.Product{}
	for _, p := range products {
		productByID[p.ID] = p
	}

	res := &RefreshResult{DryRun: dryRun}
	lookbackFloat := float64(lookbackDays)
	var stored []ReorderRecommendation

	for _, row := range rows {
		p, ok := productByID[row.ProductID]
		if !ok {
			continue
		}
		unitsF := float64(int64(row.UnitsSold)) / 10000
		velocity := unitsF / lookbackFloat
		if velocity <= 0 && row.Available+row.OnOrder <= 0 && row.Backordered <= 0 {
			res.ProductsSkipped++
			continue
		}
		newPoint := math.Ceil(velocity * defaultLead * 1.5)
		newQty := math.Ceil(velocity * 30)

		leadTime, leadSource := defaultLead, "default"
		if row.VendorID != nil {
			if v := vendors[*row.VendorID]; v != nil && v.AverageLeadTimeDays > 0 {
				leadTime, leadSource = v.AverageLeadTimeDays, "measured"
			}
		}

		suggested := math.Max(newQty, newPoint+row.BackorderedF-(row.AvailableF+row.OnOrderF))
		if suggested < 0 {
			suggested = 0
		}
		due := row.AvailableF+row.OnOrderF-row.BackorderedF <= row.OldPointF

		if due {
			res.RecommendationsNew++
			point := quantityFromFloat(row.OldPointF)
			qty := quantityFromFloat(newQty)
			velocityQ := quantityFromFloat(velocity)
			leadQ := quantityFromFloat(leadTime)
			stored = append(stored, ReorderRecommendation{
				ProductID: row.ProductID, BranchID: row.BranchID, VendorID: row.VendorID,
				OnHand: row.OnHand, Allocated: row.Allocated, Available: row.Available,
				OnOrder: row.OnOrder, Backordered: row.Backordered,
				Velocity: velocityQ, LookbackDays: lookbackDays,
				LeadTimeDays: leadQ, LeadTimeSource: leadSource,
				ReorderPoint: &point, ReorderQuantity: &qty,
				SuggestedQuantity: suggested, Unit: string(p.UOMPrimary),
				Status: "OPEN", SKU: p.SKU, Description: p.Description,
			})
		}

		if newPoint == row.OldPointF && newQty == row.OldQtyF {
			res.ProductsSkipped++
			continue
		}
		if len(res.Proposals) < maxProposalsPreview {
			res.Proposals = append(res.Proposals, ReorderTargetProposal{
				ProductID: row.ProductID, BranchID: row.BranchID, SKU: p.SKU,
				OldPoint: row.OldPointF, NewPoint: newPoint,
				OldQty: row.OldQtyF, NewQty: newQty,
				AvgDaily: math.Round(velocity*100) / 100,
			})
		}
		if !dryRun {
			if err := s.repo.WriteStockLevelTarget(ctx, row.ProductID, row.BranchID, newPoint, newQty); err != nil {
				return nil, fmt.Errorf("write stock level target: %w", err)
			}
		}
		res.ProductsUpdated++
	}

	if !dryRun && len(stored) > 0 {
		if err := s.storeRecommendations(ctx, stored); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// vendorMap reads the vendors once for the lead time source.
func (s *Service) vendorMap(ctx context.Context) (map[uuid.UUID]*vendor.Vendor, error) {
	out := map[uuid.UUID]*vendor.Vendor{}
	if s.vendorSvc == nil {
		return out, nil
	}
	vendors, err := s.vendorSvc.ListVendors(ctx)
	if err != nil {
		return nil, err
	}
	for i := range vendors {
		out[vendors[i].ID] = &vendors[i]
	}
	return out, nil
}

// storeRecommendations writes one run row per branch, its recommendations,
// and the reorder.recommended events last (ADR 0008 10.3: a bulk writer
// writes its events at the end of the run's transaction, one per branch).
func (s *Service) storeRecommendations(ctx context.Context, recs []ReorderRecommendation) error {
	byBranch := map[uuid.UUID][]*ReorderRecommendation{}
	for i := range recs {
		byBranch[recs[i].BranchID] = append(byBranch[recs[i].BranchID], &recs[i])
	}
	for branch, list := range byBranch {
		if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
			runID, err := s.repo.StartBranchReorderRun(txCtx, "refresh_targets", false, branch)
			if err != nil {
				return err
			}
			for _, rec := range list {
				if err := s.repo.InsertRecommendation(txCtx, runID, *rec); err != nil {
					return err
				}
			}
			if err := s.repo.FinishReorderRun(txCtx, runID, "SUCCESS", 0, len(list), 0, ""); err != nil {
				return err
			}
			for _, rec := range list {
				raw, err := json.Marshal(rec.EventData())
				if err != nil {
					return err
				}
				b := branch
				if err := s.events.Write(txCtx, outbox.Event{
					Type: EventReorderRecommended, EntityType: "reorder_recommendation",
					EntityID: rec.ProductID, BranchID: &b, Data: raw,
				}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// CreateReorders turns the open recommendations into draft purchase lines,
// grouped by vendor and branch, reusing the vendor's open draft, and marks
// them ORDERED (ADR 0008 10.3).
func (s *Service) CreateReorders(ctx context.Context) (int, error) {
	if s.vendorSvc == nil {
		return 0, fmt.Errorf("vendor service not configured")
	}
	open, err := s.repo.OpenRecommendations(ctx)
	if err != nil {
		return 0, err
	}
	createdCount := 0
	for _, rec := range open {
		if rec.VendorID == nil {
			continue
		}
		vid := *rec.VendorID
		po, _ := s.repo.GetDraftPOByVendorBranch(ctx, &vid, rec.BranchID)
		if po == nil {
			po = &PurchaseOrder{
				ID:       uuid.New(),
				VendorID: &vid,
				Status:   StatusDraft.Wire(),
				Source:   SourceReorder.Wire(),
				BranchID: rec.BranchID,
			}
			if err := s.repo.CreatePO(ctx, po); err != nil {
				return createdCount, fmt.Errorf("create PO for vendor %s: %w", vid, err)
			}
			createdCount++
		}

		qty := httpx.Quantity(math.Round(rec.SuggestedQuantity * 10000))
		if qty <= 0 {
			qty = one
		}
		existing, err := s.repo.GetPOLineByProduct(ctx, po.ID, rec.ProductID)
		if err != nil {
			return createdCount, fmt.Errorf("lookup existing PO line: %w", err)
		}
		if existing != nil {
			if existing.Quantity != qty {
				if err := s.repo.UpdatePOLineQuantity(ctx, existing.ID, qty, existing.UnitCostTenThousandths, existing.UOMQty, existing.PriceUOMQty); err != nil {
					return createdCount, fmt.Errorf("update PO line: %w", err)
				}
			}
		} else {
			// The unit cost keeps the base commit's placeholder until
			// pricing's ResolveCost lands (C3-2A-pricing); ADR 0008 8.4
			// defaults it there.
			line := &PurchaseOrderLine{
				ID:          uuid.New(),
				POID:        po.ID,
				ProductID:   &rec.ProductID,
				Description: rec.Description,
				Quantity:    qty,
				UOM:         rec.Unit, PriceUOM: rec.Unit,
				UOMQty: one, PriceUOMQty: one,
				StockUOM: &rec.Unit, StockQuantity: &qty,
				UnitCostTenThousandths: 0,
				Position:               99,
			}
			if err := s.repo.AddPOLine(ctx, line); err != nil {
				return createdCount, fmt.Errorf("add PO line: %w", err)
			}
			existing = line
		}
		if err := s.repo.MarkRecommendationOrdered(ctx, rec.ID, existing.ID); err != nil {
			return createdCount, fmt.Errorf("mark recommendation ordered: %w", err)
		}
	}
	return createdCount, nil
}

// ListReorderRuns surfaces the last N rows from reorder_runs for the
// operator dashboard.
func (s *Service) ListReorderRuns(ctx context.Context, limit int) ([]ReorderRun, error) {
	return s.repo.ListReorderRuns(ctx, limit)
}

// StartReorderRun and FinishReorderRun are exposed for the scheduler to
// instrument each cron tick. Direct repository pass-throughs.
func (s *Service) StartReorderRun(ctx context.Context, job string, dryRun bool) (uuid.UUID, error) {
	return s.repo.StartReorderRun(ctx, job, dryRun)
}

func (s *Service) FinishReorderRun(ctx context.Context, id uuid.UUID, status string, posCreated, productsUpdated, productsSkipped int, errMsg string) error {
	return s.repo.FinishReorderRun(ctx, id, status, posCreated, productsUpdated, productsSkipped, errMsg)
}

// groupAlertsByVendor partitions reorder alerts by canonical vendor_id. Any
// alert whose VendorID is nil is bucketed under a single sentinel vendor
// resolved lazily via resolveUnknown (typically vendor.EnsureVendorByName).
func groupAlertsByVendor(
	alerts []product.ReorderAlert,
	resolveUnknown func() (uuid.UUID, error),
) (map[uuid.UUID][]product.ReorderAlert, error) {
	byVendor := make(map[uuid.UUID][]product.ReorderAlert)
	var unknownID *uuid.UUID
	for _, a := range alerts {
		if a.VendorID != nil {
			byVendor[*a.VendorID] = append(byVendor[*a.VendorID], a)
			continue
		}
		if unknownID == nil {
			id, err := resolveUnknown()
			if err != nil {
				return nil, err
			}
			unknownID = &id
		}
		byVendor[*unknownID] = append(byVendor[*unknownID], a)
	}
	return byVendor, nil
}

// recordReceived writes purchase_order.received: the purchase order id, the
// branch each received location belongs to (the branch the stock landed in,
// which the order module's back order release queues, not necessarily the
// purchase order's own branch) and the distinct product ids that landed
// there. One event per branch the receipt touched; a receipt that put no
// product on hand (no product lines) keeps one event on the purchase order's
// branch, as before.
func (s *Service) recordReceived(ctx context.Context, po *PurchaseOrder, byBranch map[uuid.UUID][]uuid.UUID) error {
	if s.events == nil {
		return nil
	}
	dedupe := func(products []uuid.UUID) []uuid.UUID {
		seen := map[uuid.UUID]bool{}
		ids := make([]uuid.UUID, 0, len(products))
		for _, p := range products {
			if !seen[p] {
				seen[p] = true
				ids = append(ids, p)
			}
		}
		return ids
	}
	branches := make([]uuid.UUID, 0, len(byBranch))
	for b := range byBranch {
		branches = append(branches, b)
	}
	if len(branches) == 0 {
		byBranch = map[uuid.UUID][]uuid.UUID{po.BranchID: nil}
		branches = []uuid.UUID{po.BranchID}
	}
	sort.Slice(branches, func(i, j int) bool { return branches[i].String() < branches[j].String() })
	for _, b := range branches {
		raw, err := json.Marshal(map[string]any{
			"purchase_order_id": po.ID, "branch_id": b, "status": storageStatus(po.Status), "product_ids": dedupe(byBranch[b]),
		})
		if err != nil {
			return err
		}
		branch := b
		if err := s.events.Write(ctx, outbox.Event{
			Type: EventReceived, EntityType: "purchase_order", EntityID: po.ID, BranchID: &branch, Data: raw,
		}); err != nil {
			return err
		}
	}
	return nil
}
