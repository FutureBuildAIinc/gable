// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// AutoPOService is an optional interface for triggering purchase orders from accepted quotes.
type AutoPOService interface {
	CreatePOFromSpecialOrderLine(ctx context.Context, productID uuid.UUID, vendorID *uuid.UUID, quantity float64, unitCost float64, linkedSOLineID uuid.UUID) error
}

// OrderCreator is the order module's conversion seam (ADR 0005 section 5.8):
// PrepareQuoteTax prices the conversion's tax before its transaction opens
// (nil when the rate resolver will do), CreateFromQuote creates the order
// inside the caller's transaction, and QuoteHasOrder is the
// already_converted guard.
type OrderCreator interface {
	PrepareQuoteTax(ctx context.Context, src *order.QuoteSource) (*order.ProviderTax, error)
	CreateFromQuote(ctx context.Context, src *order.QuoteSource, priced *order.ProviderTax) (*order.Order, error)
	QuoteHasOrder(ctx context.Context, quoteID uuid.UUID) (bool, error)
}

// EventRecorder writes a domain event into the transactional outbox. The
// service calls it as the LAST statement of the mutation's transaction
// (ADR 0003 section 2), so the event commits or rolls back with the mutation
// it describes. *outbox.Writer satisfies it.
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// BranchGuard applies the payload branch rule (ADR 0007 section 2.3).
// *middleware.BranchGuard satisfies it.
type BranchGuard interface {
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
}

// Event types the module writes to the outbox.
const (
	EventCreated  = "quote.created"
	EventSent     = "quote.sent"
	EventAccepted = "quote.accepted"
	EventRejected = "quote.rejected"
	EventExpired  = "quote.expired"
	EventReopened = "quote.reopened"
)

var transitionEvents = map[QuoteState]string{
	QuoteStateSent:     EventSent,
	QuoteStateAccepted: EventAccepted,
	QuoteStateRejected: EventRejected,
	QuoteStateExpired:  EventExpired,
	QuoteStateDraft:    EventReopened,
}

type Service struct {
	repo        Repository
	poSvc       AutoPOService
	snapshotSvc SnapshotService
	events      EventRecorder // optional; nil records nothing (unit tests)
	tx          TxRunner      // optional; nil runs each method unwrapped (unit tests)
	logger      *slog.Logger
	branches    BranchGuard  // optional; nil leaves a payload branch unchecked (unit tests)
	orders      OrderCreator // optional; nil refuses the convert (unit tests)
	now         func() time.Time
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo, logger: slog.Default(), now: time.Now}
}

// WithAutoPO injects the purchase order service for auto-PO on quote accept.
func (s *Service) WithAutoPO(poSvc AutoPOService) {
	s.poSvc = poSvc
}

// WithSnapshotService injects the pricing exposure snapshot service, fired
// best-effort when a quote transitions DRAFT to SENT. Optional: nil disables
// price-protection snapshotting.
func (s *Service) WithSnapshotService(snapshotSvc SnapshotService) {
	s.snapshotSvc = snapshotSvc
}

// WithOutbox wires the recorder of quote.created and the transition events.
func (s *Service) WithOutbox(events EventRecorder) *Service {
	s.events = events
	return s
}

// WithBranchGuard makes Create refuse a payload branch the caller may not
// target. Without it a payload branch is not checked, so serve always sets it.
func (s *Service) WithBranchGuard(g BranchGuard) *Service {
	s.branches = g
	return s
}

// WithTxRunner wires the transaction wrapper every write uses, so the
// mutation, its revision move and its event are one transactional fact.
func (s *Service) WithTxRunner(tx TxRunner) *Service {
	s.tx = tx
	return s
}

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

// Precondition is the client's revision for an update or a transition: the
// If-Match header and/or the body's revision (ADR 0001 section 11).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

func (p Precondition) missing() bool { return p.IfMatch == "" && p.Revision == nil }

func (p Precondition) check(current int64) error {
	return httpx.CheckRevision(current, p.IfMatch, p.Revision)
}

// notFound maps the repository's sentinel to the wire's 404.
func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound(ErrNotFound.Error())
	}
	return err
}

// priceDraft turns a Draft into a priced Quote body (no number, no id yet):
// product defaults filled, each line extended once by the platform rule,
// totals summed, and freight cleared on a pickup BEFORE it is rolled into the
// total (a pickup must not be billed for delivery). Every field problem found
// is collected into one 400.
func (s *Service) priceDraft(ctx context.Context, d *Draft) (*Quote, error) {
	v := &httpx.Validator{}

	var productIDs []uuid.UUID
	for _, l := range d.Lines {
		if l.ProductID != nil {
			productIDs = append(productIDs, *l.ProductID)
		}
	}
	products, err := s.repo.LookupProducts(ctx, productIDs)
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	q := &Quote{}
	q.CustomerID, q.JobID, q.ExpiresAt = d.CustomerID, d.JobID, d.ExpiresAt
	q.Status, q.Source, q.MarginTotalCents = QuoteStateDraft, d.Source, d.MarginTotalCents
	q.DeliveryType, q.FreightCents, q.VehicleID = d.DeliveryType, d.FreightCents, d.VehicleID
	if d.BranchID != nil {
		q.BranchID = *d.BranchID
	}
	if d.DeliveryType == DeliveryPickup {
		q.VehicleID = nil
		q.FreightCents = 0
	}
	q.CreatedAt = httpx.TimestampOf(now)
	q.UpdatedAt = q.CreatedAt
	q.Revision = 1
	if len(d.OriginalFile) > 0 {
		q.OriginalFile = d.OriginalFile
	}
	if d.OriginalFilename != "" {
		q.OriginalFilename = &d.OriginalFilename
	}
	if d.OriginalContentType != "" {
		q.OriginalContentType = &d.OriginalContentType
	}
	q.ParseMap = d.ParseMap
	q.ExposureState = "OK"

	q.Lines = make([]QuoteLine, 0, len(d.Lines))
	total := int64(q.FreightCents)
	for i, dl := range d.Lines {
		path := fmt.Sprintf("lines[%d]", i)
		line := QuoteLine{
			ID: dl.ID, ProductID: dl.ProductID, SKU: dl.SKU, Description: dl.Description,
			Quantity: dl.Quantity, UOM: dl.UOM, PriceUOM: dl.PriceUOM,
			UOMQty: dl.UOMQty, PriceUOMQty: dl.PriceUOMQty, UnitPrice: dl.UnitPrice,
			CreatedAt: q.CreatedAt,
		}
		if dl.CustomerNote != "" {
			note := dl.CustomerNote
			line.CustomerNote = &note
		}
		if line.ID == uuid.Nil {
			line.ID = uuid.New()
		}
		if dl.ProductID != nil {
			p, ok := products[*dl.ProductID]
			if !ok {
				v.Check(false, path+".product_id", "no such product")
			} else {
				if line.SKU == "" {
					line.SKU = p.SKU
				}
				if line.Description == "" {
					line.Description = p.Description
				}
				// The unit defaults from the product, then the price unit
				// from the unit, then the pair from the two.
				if line.UOM == "" {
					line.UOM = productUOM(p.UOMPrimary)
				}
			}
		}
		if line.PriceUOM == "" {
			line.PriceUOM = string(line.UOM)
		}
		if line.UOM == "" {
			continue // the product was unknown (reported above): nothing to price
		}
		if line.UOMQty == 0 && line.PriceUOMQty == 0 {
			if line.PriceUOM != string(line.UOM) {
				v.Check(false, path+".uom_qty", "is required when price_uom differs from uom: send uom_qty and price_uom_qty")
				continue
			}
			line.UOMQty, line.PriceUOMQty = one, one
		}
		ext, err := httpx.Extend(line.Quantity, line.UOMQty, line.PriceUOMQty, line.UnitPrice)
		if err != nil || total > math.MaxInt64-int64(ext) {
			v.Check(false, path, "the line's extension is beyond what the document can hold")
			continue
		}
		line.LineTotal = ext
		total += int64(ext)
		q.Lines = append(q.Lines, line)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	q.TotalCents = httpx.Cents(total)
	return q, nil
}

// Create validates the references, prices the document, mints its number from
// the sequence and stores it, writing quote.created as the transaction's last
// statement. A create that fails anywhere leaves no quote and no event.
func (s *Service) Create(ctx context.Context, d *Draft) (*Quote, error) {
	if err := s.checkPayloadBranch(ctx, d); err != nil {
		return nil, err
	}
	var out *Quote
	err := s.inTx(ctx, func(ctx context.Context) error {
		q, err := s.priceDraft(ctx, d)
		if err != nil {
			return err
		}
		q.ID = uuid.New()
		for i := range q.Lines {
			q.Lines[i].QuoteID = q.ID
		}
		if q.Number, err = s.repo.NextNumber(ctx); err != nil {
			return err
		}
		if err := s.repo.InsertQuote(ctx, q); err != nil {
			return err
		}
		if out, err = s.repo.GetQuote(ctx, q.ID); err != nil {
			return err
		}
		return s.record(ctx, out, EventCreated, "")
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Update replaces a draft quote's header and lines on the client's revision.
func (s *Service) Update(ctx context.Context, id uuid.UUID, d *Draft, pre Precondition) (*Quote, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Quote
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockQuote(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetQuote(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkQuoteBranch(ctx, cur); err != nil {
			return err
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		if cur.Status != QuoteStateDraft {
			return &httpx.Error{Status: 409, Code: httpx.CodeConflict, Message: "only draft quotes can be edited",
				Details: []httpx.FieldError{httpx.Blocker("quote_not_draft", "the quote is "+cur.Status.Status())}}
		}
		q, err := s.priceDraft(ctx, d)
		if err != nil {
			return err
		}
		q.ID, q.Number, q.BranchID = cur.ID, cur.Number, cur.BranchID
		for i := range q.Lines {
			q.Lines[i].QuoteID = id
		}
		if err := s.repo.ReplaceDraft(ctx, q); err != nil {
			return err
		}
		out, err = s.repo.GetQuote(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) GetQuote(ctx context.Context, id uuid.UUID) (*Quote, error) {
	q, err := s.repo.GetQuote(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	if err := s.checkQuoteBranch(ctx, q); err != nil {
		return nil, err
	}
	return q, nil
}

// ListQuotes returns one page: up to f.Limit rows, and whether more follow
// (the repository is asked for one extra row to know). total is set only
// when the caller asks, the opt in count of ADR 0001 section 1.
func (s *Service) ListQuotes(ctx context.Context, f ListFilter, wantTotal bool) (items []QuoteSummary, hasMore bool, total *int64, err error) {
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.ListQuotes(ctx, f)
	if err != nil {
		return nil, false, nil, err
	}
	if len(rows) > limit {
		rows, hasMore = rows[:limit], true
	}
	if wantTotal {
		n, err := s.repo.CountQuotes(ctx, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return rows, hasMore, total, nil
}

// ListByCustomer is every quote of one customer, for the partner surface.
func (s *Service) ListByCustomer(ctx context.Context, customerID uuid.UUID) ([]QuoteSummary, error) {
	return s.repo.ListQuotesByCustomer(ctx, customerID)
}

// Transition moves a quote along its lifecycle on the client's revision,
// writes the transition's event as the transaction's last statement, and
// after the commit runs the best effort side effects (auto purchase orders on
// accept, the exposure snapshot on send).
func (s *Service) Transition(ctx context.Context, id uuid.UUID, to QuoteState, pre Precondition) (*Quote, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	return s.transition(ctx, id, to, &pre, nil)
}

// UpdateState is the transition for in process callers (the portal's accept
// and decline, the integration seam): the same lifecycle rules, the same
// event, no client revision to precondition on.
func (s *Service) UpdateState(ctx context.Context, id uuid.UUID, to QuoteState) error {
	_, err := s.transition(ctx, id, to, nil, nil)
	return err
}

// transition runs the lifecycle change in one transaction. check, when set,
// runs on the locked quote after the lifecycle rule and BEFORE the status
// moves, so a refusal it returns leaves the quote exactly as it was.
func (s *Service) transition(ctx context.Context, id uuid.UUID, to QuoteState, pre *Precondition, check func(cur *Quote) error) (*Quote, error) {
	var out *Quote
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockQuote(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetQuote(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkQuoteBranch(ctx, cur); err != nil {
			return err
		}
		if pre != nil {
			if err := pre.check(cur.Revision); err != nil {
				return err
			}
		}
		if err := validateStateTransition(cur.Status, to); err != nil {
			return err
		}
		if check != nil {
			if err := check(cur); err != nil {
				return err
			}
		}
		from := cur.Status
		now := httpx.TimestampOf(s.now().UTC())
		cur.Status = to
		switch to {
		case QuoteStateSent:
			cur.SentAt = &now
		case QuoteStateAccepted:
			cur.AcceptedAt = &now
		case QuoteStateRejected:
			cur.RejectedAt = &now
		}
		if err := s.repo.SetStatus(ctx, cur); err != nil {
			return err
		}
		if out, err = s.repo.GetQuote(ctx, id); err != nil {
			return err
		}
		return s.record(ctx, out, transitionEvents[to], from.Status())
	})
	if err != nil {
		return nil, err
	}

	// Auto-PO: when accepted, trigger POs for special-order items.
	if to == QuoteStateAccepted && s.poSvc != nil {
		s.triggerAutoPO(ctx, out)
	}
	// Price-protection: when sent, snapshot index baselines for commodity
	// lines. Best-effort: a pricing-module failure must never block a send.
	if to == QuoteStateSent && s.snapshotSvc != nil {
		if err := s.snapshotSvc.SnapshotQuoteLines(ctx, out.ID); err != nil {
			s.logger.Warn("exposure snapshot failed for quote", "quote_id", out.ID, "error", err)
		} else {
			s.logger.Info("exposure snapshot written for quote", "quote_id", out.ID)
		}
	}
	return out, nil
}

// Convert accepts the quote and creates the order in ONE transaction
// (ADR 0005 section 5.8), answering the created order. The quote row is
// locked first; a quote that already has an order not cancelled is 409
// already_converted. The lines carry the conversion pair without loss (the
// R1-15 refusal is lifted); a stocked line sold in another unit than its
// stocking unit is still refused until cycle 3. Events: quote.accepted, then
// order.created.
func (s *Service) Convert(ctx context.Context, id uuid.UUID, pre Precondition) (*order.Order, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	return s.convert(ctx, id, &pre)
}

// ConvertInProcess is the convert for in process callers (the frozen
// integration seam): the same rules and events, no client revision to
// precondition on. The caller marks itself branchctx.WithSystem, as the
// seam does (ADR 0007 section 5.5): the record branch rule fails closed for
// a context with no branch.
func (s *Service) ConvertInProcess(ctx context.Context, id uuid.UUID) (*order.Order, error) {
	return s.convert(ctx, id, nil)
}

func (s *Service) convert(ctx context.Context, id uuid.UUID, pre *Precondition) (*order.Order, error) {
	if s.orders == nil {
		return nil, fmt.Errorf("the order service is not wired")
	}
	// The provider, when one is configured, prices the conversion's tax
	// BEFORE the transaction opens: it is an HTTP call (ADR 0005 section 3).
	// The record branch rule runs first, so a quote the caller may not
	// target never reaches the provider.
	current, err := s.repo.GetQuote(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	if err := s.checkQuoteBranch(ctx, current); err != nil {
		return nil, err
	}
	src := quoteSourceFor(current)
	priced, err := s.orders.PrepareQuoteTax(ctx, src)
	if err != nil {
		return nil, err
	}

	var created *order.Order
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockQuote(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetQuote(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkQuoteBranch(ctx, cur); err != nil {
			return err
		}
		if pre != nil {
			if err := pre.check(cur.Revision); err != nil {
				return err
			}
		}
		if err := validateStateTransition(cur.Status, QuoteStateAccepted); err != nil {
			return err
		}
		if has, err := s.orders.QuoteHasOrder(ctx, id); err != nil {
			return err
		} else if has {
			return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
				Message: "this quote already has an order",
				Details: []httpx.FieldError{httpx.Blocker("already_converted",
					"the quote already has an order that is not cancelled")}}
		}
		src := quoteSourceFor(cur)
		from := cur.Status
		now := httpx.TimestampOf(s.now().UTC())
		cur.Status = QuoteStateAccepted
		cur.AcceptedAt = &now
		if err := s.repo.SetStatus(ctx, cur); err != nil {
			return err
		}
		if cur, err = s.repo.GetQuote(ctx, id); err != nil {
			return err
		}
		if err := s.record(ctx, cur, EventAccepted, from.Status()); err != nil {
			return err
		}
		created, err = s.orders.CreateFromQuote(ctx, src, priced)
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// WithOrderCreator wires the conversion seam.
func (s *Service) WithOrderCreator(orders OrderCreator) *Service {
	s.orders = orders
	return s
}

// quoteSourceFor maps a quote onto what the order copies (ADR 0005 5.8's
// table): the lines with their pair and price exactly, the header's job,
// delivery type and freight.
func quoteSourceFor(q *Quote) *order.QuoteSource {
	src := &order.QuoteSource{
		QuoteID:      q.ID,
		BranchID:     q.BranchID,
		CustomerID:   q.CustomerID,
		JobID:        q.JobID,
		FreightCents: q.FreightCents,
	}
	switch q.DeliveryType {
	case DeliveryDelivery:
		src.DeliveryType = order.DeliveryDelivery
	default:
		src.DeliveryType = order.DeliveryPickup
	}
	for _, l := range q.Lines {
		src.Lines = append(src.Lines, order.QuoteSourceLine{
			QuoteLineID: l.ID, ProductID: l.ProductID, SKU: l.SKU, Description: l.Description,
			Quantity: l.Quantity, UOM: string(l.UOM), PriceUOM: l.PriceUOM,
			UOMQty: l.UOMQty, PriceUOMQty: l.PriceUOMQty, UnitPrice: l.UnitPrice,
		})
	}
	return src
}

// record writes the quote's event into the outbox through the transaction's
// executor. fromStatus is set on a transition.
func (s *Service) record(ctx context.Context, q *Quote, eventType, fromStatus string) error {
	if s.events == nil {
		return nil
	}
	data := map[string]any{
		"number":      q.Number,
		"customer_id": q.CustomerID,
		"status":      q.Status.Status(),
		"revision":    q.Revision,
		"total_cents": int64(q.TotalCents),
	}
	if fromStatus != "" {
		data["from_status"] = fromStatus
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	branch := q.BranchID
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: "quote", EntityID: q.ID, BranchID: &branch, Data: raw,
	})
}

// triggerAutoPO creates purchase orders for special-order quote lines.
// This is fire-and-forget: failures are logged but don't block acceptance.
func (s *Service) triggerAutoPO(ctx context.Context, q *Quote) {
	for _, line := range q.Lines {
		// Only create POs for lines that have a unit cost (special order indicator)
		if line.UnitCost > 0 && line.ProductID != nil {
			err := s.poSvc.CreatePOFromSpecialOrderLine(
				ctx, *line.ProductID, nil, float64(line.Quantity)/10000, float64(line.UnitCost)/10000, line.ID,
			)
			if err != nil {
				s.logger.Warn("auto-PO failed for quote line",
					"quote_id", q.ID, "line_id", line.ID, "product_id", line.ProductID, "error", err)
			} else {
				s.logger.Info("auto-PO created for quote line",
					"quote_id", q.ID, "line_id", line.ID, "product_id", line.ProductID)
			}
		}
	}
}

func (s *Service) GetAnalytics(ctx context.Context) (*QuoteAnalytics, error) {
	return s.repo.GetQuoteAnalytics(ctx)
}

// GetOriginalFile returns the stored upload; a missing quote is a 404.
func (s *Service) GetOriginalFile(ctx context.Context, id uuid.UUID) ([]byte, string, string, error) {
	// The record branch rule rides on the read: a quote the caller may not
	// target is refused before any byte of its file leaves.
	if _, err := s.GetQuote(ctx, id); err != nil {
		return nil, "", "", err
	}
	data, name, ctype, err := s.repo.GetOriginalFile(ctx, id)
	return data, name, ctype, notFound(err)
}

// validateStateTransition ensures the status change is allowed by the
// lifecycle; a refusal is the wire's 409 invalid_state_transition.
func validateStateTransition(from, to QuoteState) error {
	allowed := map[QuoteState][]QuoteState{
		QuoteStateDraft:    {QuoteStateSent, QuoteStateAccepted, QuoteStateRejected, QuoteStateExpired},
		QuoteStateSent:     {QuoteStateAccepted, QuoteStateRejected, QuoteStateExpired},
		QuoteStateAccepted: {},                // terminal
		QuoteStateRejected: {QuoteStateDraft}, // allow re-opening
		QuoteStateExpired:  {QuoteStateDraft}, // allow re-opening
	}

	targets, ok := allowed[from]
	if !ok {
		return fmt.Errorf("unknown current status: %s", from.Status())
	}
	for _, t := range targets {
		if t == to {
			return nil
		}
	}
	return httpx.InvalidStateTransition(fmt.Sprintf("cannot transition from %s to %s", from.Status(), to.Status()))
}

// checkPayloadBranch is the payload branch rule: a branch the body names must
// be one the caller may target, else 403 forbidden naming branch_id.
func (s *Service) checkPayloadBranch(ctx context.Context, d *Draft) error {
	if s.branches == nil || d.BranchID == nil {
		return nil
	}
	err := s.branches.CheckPayloadBranch(ctx, *d.BranchID)
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		return &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
			Message: "branch_id is outside the branches this caller may target",
			Details: []httpx.FieldError{{Field: "branch_id", Code: httpx.CodeForbidden, Message: "not a branch this caller may target"}}}
	}
	return err
}

// checkQuoteBranch is the record branch rule (ADR 0007 section 2.3) for a
// quote a path id addresses: the quote's branch must be one the caller may
// target, the same rule the create applies to the body's branch_id, else 403
// forbidden naming id. The check fails closed: a caller with no branch
// context is refused unless it marked itself a system caller with
// branchctx.WithSystem. The two context-free callers are marked: the portal's
// quote decision and the integration seam (ADR 0007 section 5.5 admits an
// unbound key to any branch), so a route mounted without the branch
// middleware can no longer reach these methods unwalled.
func (s *Service) checkQuoteBranch(ctx context.Context, q *Quote) error {
	if s.branches == nil {
		return nil
	}
	err := s.branches.CheckPayloadBranch(ctx, q.BranchID)
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		return &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
			Message: "quote is outside the branches this caller may target",
			Details: []httpx.FieldError{{Field: "id", Code: httpx.CodeForbidden, Message: "not a branch this caller may target"}}}
	}
	return err
}
