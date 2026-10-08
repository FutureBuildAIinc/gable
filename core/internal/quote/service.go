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
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// AutoPOService is an optional interface for triggering purchase orders from accepted quotes.
type AutoPOService interface {
	CreatePOFromSpecialOrderLine(ctx context.Context, productID uuid.UUID, vendorID *uuid.UUID, quantity float64, unitCost float64, linkedSOLineID uuid.UUID) error
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
			}
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
	return s.transition(ctx, id, to, &pre)
}

// UpdateState is the transition for in process callers (the portal's accept
// and decline, the integration seam): the same lifecycle rules, the same
// event, no client revision to precondition on.
func (s *Service) UpdateState(ctx context.Context, id uuid.UUID, to QuoteState) error {
	_, err := s.transition(ctx, id, to, nil)
	return err
}

func (s *Service) transition(ctx context.Context, id uuid.UUID, to QuoteState, pre *Precondition) (*Quote, error) {
	var out *Quote
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockQuote(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetQuote(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if pre != nil {
			if err := pre.check(cur.Revision); err != nil {
				return err
			}
		}
		if err := validateStateTransition(cur.Status, to); err != nil {
			return err
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

// Convert accepts the quote on the client's revision and returns the order
// payload for the client to POST to /orders.
func (s *Service) Convert(ctx context.Context, id uuid.UUID, pre Precondition) (*OrderPayload, error) {
	q, err := s.Transition(ctx, id, QuoteStateAccepted, pre)
	if err != nil {
		return nil, err
	}
	payload := &OrderPayload{CustomerID: q.CustomerID, QuoteID: q.ID, Lines: make([]OrderPayloadLine, 0, len(q.Lines))}
	for _, l := range q.Lines {
		// The price per sale unit: the unit price converted through the
		// line's pair, in cents.
		each, err := httpx.Extend(10000, l.UOMQty, l.PriceUOMQty, l.UnitPrice)
		if err != nil {
			return nil, fmt.Errorf("price quote line %s for the order: %w", l.ID, err)
		}
		payload.Lines = append(payload.Lines, OrderPayloadLine{
			ProductID: l.ProductID, Quantity: l.Quantity, UOM: l.UOM, PriceEachCents: each,
		})
	}
	return payload, nil
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
