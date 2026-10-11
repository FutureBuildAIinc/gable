// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"time"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/units"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// AutoPOService is an optional interface for triggering purchase orders from accepted quotes.
type AutoPOService interface {
	// productID is nil for a line that carries no product on its purchase
	// order line (the plain accept path, whose lines are inert on receipt).
	CreatePOFromSpecialOrderLine(ctx context.Context, productID *uuid.UUID, vendorID *uuid.UUID, quantity float64, unitCost float64, linkedSOLineID uuid.UUID) error
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
	auditor     AuditSink    // optional; nil writes no audit row (unit tests)
	now         func() time.Time
}

// AuditSink writes the module's audit rows through the caller's executor.
// *audit.Logger satisfies it.
type AuditSink interface {
	Log(ctx context.Context, entry audit.Entry) error
}

// WithAudit wires the audit sink.
func (s *Service) WithAudit(a AuditSink) *Service {
	if a != nil {
		s.auditor = a
	}
	return s
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
// product defaults filled, each line's units resolved against the product's
// unit set and the catalogue (ADR 0006 sections 3.3, 3.4 and 4), each line
// extended once by the platform rule, totals summed, and freight cleared on a
// pickup BEFORE it is rolled into the total (a pickup must not be billed for
// delivery). Every field problem found is collected into one 400.
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
	productUnits, err := s.repo.LookupProductUnits(ctx, productIDs)
	if err != nil {
		return nil, err
	}
	// Default the units before the catalogue read so it covers the defaulted
	// codes too: the line's uom takes the product's sale unit and its
	// price_uom the product's price unit (ADR 0006 section 3.3).
	var unitCodes []string
	for i := range d.Lines {
		dl := &d.Lines[i]
		if dl.ProductID != nil {
			set := productUnits[*dl.ProductID]
			if dl.UOM == "" {
				dl.UOM = productUOM(set.SaleUOM)
			}
			if dl.PriceUOM == "" {
				dl.PriceUOM = set.PriceUOM
			}
		}
		if dl.PriceUOM == "" {
			dl.PriceUOM = string(dl.UOM)
		}
		if dl.UOM != "" {
			unitCodes = append(unitCodes, string(dl.UOM))
		}
		if dl.PriceUOM != "" {
			unitCodes = append(unitCodes, dl.PriceUOM)
		}
	}
	catalogue, err := s.repo.LookupCatalogue(ctx, unitCodes)
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
		var set ProductUnitSet
		if dl.ProductID != nil {
			p, ok := products[*dl.ProductID]
			if !ok {
				v.Check(false, path+".product_id", "no such product")
				continue // an unknown product has no set to resolve against
			}
			if line.SKU == "" {
				line.SKU = p.SKU
			}
			if line.Description == "" {
				line.Description = p.Description
			}
			set = productUnits[*dl.ProductID]
		}
		if line.UOM == "" || line.PriceUOM == "" {
			continue // the product was unknown (reported above): nothing to price
		}
		if !s.resolveLineUnits(v, path, &line, dl, set, catalogue) {
			continue
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

// resolveLineUnits settles one line's units (ADR 0006 sections 3.3, 3.4 and
// 4): the catalogue check, the pair (resolved from the product's set on a
// product line, the client's or a standard size derivation on the others),
// the stocking quantity, exact or refused with the nearest quantities, and
// the tally of a random length line. It answers false when the line is
// unusable; every problem is already collected in v.
func (s *Service) resolveLineUnits(v *httpx.Validator, path string, line *QuoteLine, dl DraftLine, set ProductUnitSet, catalogue map[string]units.CatalogueUnit) bool {
	before := errorCount(v)
	uom := string(line.UOM)

	// The catalogue: a line's units are active catalogue units (2.1).
	for _, unit := range []struct {
		field string
		code  string
		sent  bool
	}{
		{"uom", uom, true},
		{"price_uom", line.PriceUOM, dl.ProductID == nil || line.PriceUOM != set.PriceUOM},
	} {
		if !unit.sent {
			continue // defaulted from the product: the set check below covers it
		}
		u, known := catalogue[unit.code]
		if !known {
			v.Check(false, path+"."+unit.field, unit.code+" is not a unit of the catalogue")
		} else if !u.IsActive {
			v.Check(false, path+"."+unit.field, unit.code+" is inactive: an inactive unit cannot enter a new line")
		}
	}

	if dl.ProductID != nil {
		// The units must be rows of the product's set, sell on the sale unit
		// and price on the price unit (3.3).
		uRow, hasU := set.Row(uom)
		if !hasU || !uRow.Sell {
			v.Check(false, path+".uom", uom+" is not a sale unit of the product")
		}
		pRow, hasP := set.Row(line.PriceUOM)
		if !hasP || !pRow.Price {
			v.Check(false, path+".price_uom", line.PriceUOM+" is not a price unit of the product")
		}
		if v.Err() != nil && errorCount(v) > before {
			return false
		}
		// The pair resolves from the two rows and is stored on the line; a
		// sent pair must equal it as a ratio (3.3), and the resolved pair
		// is what the line stores, canonical whatever equivalent pair the
		// client sent (R2: one conversion, one byte form).
		resolved, err := units.ResolveLinePair(uRow.rowPair(), pRow.rowPair())
		if err != nil {
			v.Check(false, path+".uom_qty", "the conversion between "+uom+" and "+line.PriceUOM+" does not fit the pair's bound")
			return false
		}
		if dl.UOMQty != 0 || dl.PriceUOMQty != 0 {
			sent := units.Pair{A: dl.UOMQty, B: dl.PriceUOMQty}
			if !sent.SameRatio(resolved) {
				v.Check(false, path+".uom_qty",
					"does not match the product's unit set; omit the pair or send "+
						resolved.A.WireString()+" and "+resolved.B.WireString())
			}
		}
		line.UOMQty, line.PriceUOMQty = resolved.A, resolved.B
	} else {
		// A line without a product keeps R1-15's rule: the pair is the
		// client's, except that two units with standard sizes in one
		// dimension derive it the same way (rule 2 of 3.2) and a sent pair
		// must agree.
		if line.UOMQty == 0 && line.PriceUOMQty == 0 {
			if line.PriceUOM == uom {
				line.UOMQty, line.PriceUOMQty = one, one
			} else if derived, ok := units.StandardPair(uom, line.PriceUOM, catalogue); ok {
				line.UOMQty, line.PriceUOMQty = derived.A, derived.B
			} else {
				v.Check(false, path+".uom_qty", "is required when price_uom differs from uom: send uom_qty and price_uom_qty")
			}
		} else if derived, ok := units.StandardPair(uom, line.PriceUOM, catalogue); ok {
			if !(units.Pair{A: line.UOMQty, B: line.PriceUOMQty}).SameRatio(derived) {
				v.Check(false, path+".uom_qty",
					"does not match the units' standard sizes; the derived pair is "+
						derived.A.WireString()+" and "+derived.B.WireString())
			}
			// The derived pair is canonical (R2): an agreeing sent pair
			// stores it, not the form the client chose.
			line.UOMQty, line.PriceUOMQty = derived.A, derived.B
		}
	}
	if errorCount(v) > before {
		return false
	}

	// The tally (section 4): allowed on a random length product line only,
	// its sale unit LF, its linear feet the line's quantity.
	if dl.Tally != nil {
		if dl.ProductID == nil || !set.RandomLength {
			v.Check(false, path+".tally", "a tally is allowed on a random length product line only")
			return false
		}
		if uom != "LF" {
			v.Check(false, path+".uom", "a tallied line's unit is LF: the tally carries the lengths")
			return false
		}
		rows := make([]units.TallyRow, 0, len(dl.Tally.Rows))
		for _, r := range dl.Tally.Rows {
			rows = append(rows, units.TallyRow{Pieces: int64(r.Pieces), LengthFT: r.LengthFT})
		}
		linearFeet, err := units.LinearFeet(rows)
		if err != nil {
			v.Check(false, path+".tally",
				"the tally's linear feet are beyond the quantity bound of 99999999.9999 linear feet")
			return false
		}
		if dl.HasQuantity && dl.Quantity != linearFeet {
			v.Check(false, path+".quantity", "must equal the tally's linear feet, "+linearFeet.WireString())
			return false
		}
		line.Quantity = linearFeet
		line.Tally = &Tally{Rows: make([]TallyRow, 0, len(rows))}
		for _, r := range rows {
			line.Tally.Rows = append(line.Tally.Rows, TallyRow{Pieces: int(r.Pieces), LengthFT: r.LengthFT})
		}
		line.Tally.LinearFeet = linearFeet
		if set.BoardThick != nil && set.BoardWidth != nil {
			line.Tally.ThicknessIn, line.Tally.WidthIn = set.BoardThick, set.BoardWidth
			if bf, err := units.BoardFeet(linearFeet, *set.BoardThick, *set.BoardWidth); err == nil {
				display := units.DisplayBoardFeet(bf)
				line.Tally.BoardFeet = &display
			}
		}
	}

	// The stocking unit and quantity (3.4): exact at scale 4 or the line is
	// refused with the nearest quantities that are exact (R5).
	if dl.ProductID != nil {
		stockUOM := set.StockUOM
		var stockQty httpx.Quantity
		var err error
		if uom == stockUOM {
			stockQty = line.Quantity
		} else if uRow, ok := set.Row(uom); ok {
			stockQty, err = units.ConvertStock(line.Quantity, uom, stockUOM, uRow.rowPair())
		} else {
			return false // the set check above already refused the unit
		}
		if err != nil {
			var inexact *units.InexactError
			switch {
			case errors.As(err, &inexact):
				v.Check(false, path+".quantity", inexact.Error())
			case errors.Is(err, units.ErrOutOfBound):
				v.Check(false, path+".quantity",
					"is past the quantity bound of 99999999.9999 when converted into the stocking unit "+stockUOM)
			default:
				v.Check(false, path+".quantity", "does not convert exactly into the stocking unit "+stockUOM)
			}
			return false
		}
		line.StockUOM, line.StockQuantity = &stockUOM, &stockQty
	}
	return true
}

// rowPair is the set row as a Pair, its left side the row's unit.
func (r ProductUnitRow) rowPair() units.Pair {
	return units.Pair{A: r.UnitQty, B: r.StockQty}
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
		q, ev, err := s.createCore(ctx, d)
		if err != nil {
			return err
		}
		out = q
		return s.writeEvent(ctx, ev)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// createCore prices and stores a new quote INSIDE the caller's transaction
// and returns the quote.created event instead of writing it, so a caller
// that owns a larger transaction (a draft's promotion, ADR 0007 section
// 4.3) writes the event as that transaction's last statement. It assumes
// the payload branch rule already ran (Create applies it; the promotion
// runs at the draft's branch context).
func (s *Service) createCore(ctx context.Context, d *Draft) (*Quote, *outbox.Event, error) {
	q, err := s.priceDraft(ctx, d)
	if err != nil {
		return nil, nil, err
	}
	q.ID = uuid.New()
	for i := range q.Lines {
		q.Lines[i].QuoteID = q.ID
	}
	if q.Number, err = s.repo.NextNumber(ctx); err != nil {
		return nil, nil, err
	}
	if err := s.repo.InsertQuote(ctx, q); err != nil {
		return nil, nil, err
	}
	out, err := s.repo.GetQuote(ctx, q.ID)
	if err != nil {
		return nil, nil, err
	}
	ev := s.buildEvent(out, EventCreated, "")
	return out, ev, nil
}

// Update replaces a draft quote's header and lines on the client's revision.
func (s *Service) Update(ctx context.Context, id uuid.UUID, d *Draft, pre Precondition) (*Quote, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Quote
	err := s.inTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.updateCore(ctx, id, d, pre)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// updateCore replaces a draft quote's header and lines inside the caller's
// transaction, on the precondition the caller holds: the quote row is
// locked first and the revision checked after the lock, so the check and
// the write are one database act. The update writes no event (the quote's
// update writes none today, ADR 0007 section 4.4), so the caller's
// transaction ends with its own statements.
func (s *Service) updateCore(ctx context.Context, id uuid.UUID, d *Draft, pre Precondition) (*Quote, error) {
	if err := s.repo.LockQuote(ctx, id); err != nil {
		return nil, notFound(err)
	}
	cur, err := s.repo.GetQuote(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	if err := s.checkQuoteBranch(ctx, cur); err != nil {
		return nil, err
	}
	if err := pre.check(cur.Revision); err != nil {
		return nil, err
	}
	if cur.Status != QuoteStateDraft {
		return nil, &httpx.Error{Status: 409, Code: httpx.CodeConflict, Message: "only draft quotes can be edited",
			Details: []httpx.FieldError{httpx.Blocker("quote_not_draft", "the quote is "+cur.Status.Status())}}
	}
	q, err := s.priceDraft(ctx, d)
	if err != nil {
		return nil, err
	}
	q.ID, q.Number, q.BranchID = cur.ID, cur.Number, cur.BranchID
	for i := range q.Lines {
		q.Lines[i].QuoteID = id
	}
	if err := s.repo.ReplaceDraft(ctx, q); err != nil {
		return nil, err
	}
	return s.repo.GetQuote(ctx, id)
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

// NumberPattern is the quote's document number pattern, from the entity's
// prefix and pad (ADR 0007 section 7): reads accept it in the {id} slot.
var NumberPattern = regexp.MustCompile(`^Q-[0-9]{6,}$`)

// ResolveRecordID parses a record URL's {id} slot: a UUID first, then the
// entity's number pattern. A well formed number of another entity, or
// anything else, is a 400 naming id; a value that names no visible row is
// the caller's 404.
func ResolveRecordID(raw string) (uuid.UUID, string, error) {
	if id, err := uuid.Parse(raw); err == nil {
		return id, "", nil
	}
	if NumberPattern.MatchString(raw) {
		return uuid.Nil, raw, nil
	}
	return uuid.Nil, "", httpx.BadRequest("invalid quote id",
		httpx.FieldError{Field: "id", Message: "must be a UUID or a quote number such as Q-000123"})
}

// GetQuoteByIDOrNumber reads a quote by its UUID or its document number
// (section 7): both spellings answer exactly the same body, no redirect.
func (s *Service) GetQuoteByIDOrNumber(ctx context.Context, raw string) (*Quote, error) {
	id, number, err := ResolveRecordID(raw)
	if err != nil {
		return nil, err
	}
	if number != "" {
		q, err := s.repo.GetQuoteByNumber(ctx, number)
		if err != nil {
			return nil, notFound(err)
		}
		if err := s.checkQuoteBranch(ctx, q); err != nil {
			return nil, err
		}
		return q, nil
	}
	return s.GetQuote(ctx, id)
}

// maxAttachFile bounds the quote file route's body: the same 5 MiB bound
// the create applies to a decoded original upload.
const maxAttachFile = 5 << 20

// AttachFile replaces the quote's stored upload (ADR 0007 section 10): raw
// body with its content type, on the revision precondition, only while the
// quote is in status draft, by the promotion's committer. It moves the
// revision by one, writes the quote.file_attached audit row and no outbox
// event (the quote's update writes none today).
func (s *Service) AttachFile(ctx context.Context, id uuid.UUID, data []byte, filename, contentType string, pre Precondition) (*Quote, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	if len(data) > maxAttachFile {
		return nil, httpx.PayloadTooLarge("the file is past the 5 MiB bound")
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
			return httpx.InvalidStateTransition("a file attaches only to a draft quote",
				httpx.Blocker("quote_not_draft", "the quote is "+cur.Status.Status()))
		}
		if err := s.repo.StoreOriginalFile(ctx, id, data, filename, contentType); err != nil {
			return err
		}
		if out, err = s.repo.GetQuote(ctx, id); err != nil {
			return err
		}
		if s.auditor != nil {
			sum := sha256.Sum256(data)
			return s.auditor.Log(ctx, audit.Entry{
				Action:     "quote.file_attached",
				EntityType: "quote",
				EntityID:   id,
				Changes: map[string]any{
					"filename": filename, "content_type": contentType,
					"bytes": len(data), "sha256": hex.EncodeToString(sum[:]),
				},
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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
		s.triggerAutoPO(ctx, out, nil)
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
	var accepted *Quote
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
		accepted = cur
		created, err = s.orders.CreateFromQuote(ctx, src, priced)
		return err
	})
	if err != nil {
		return nil, err
	}
	// Auto-PO, as the accept it replaced did: after the commit and best
	// effort (a failure is logged and never blocks the convert). It stays
	// outside the transaction because the purchase order service writes
	// through its own pool connection, and a purchase order the convert's
	// rollback could not take back would be orphaned.
	if s.poSvc != nil {
		s.triggerAutoPO(ctx, accepted, created)
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

// buildEvent shapes the quote's event without writing it; fromStatus is set
// on a transition. A nil service recorder builds the event anyway, so a
// caller inside a larger transaction (a draft's promotion) can write it
// through its own recorder.
func (s *Service) buildEvent(q *Quote, eventType, fromStatus string) *outbox.Event {
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
		return nil
	}
	branch := q.BranchID
	return &outbox.Event{
		Type: eventType, EntityType: "quote", EntityID: q.ID, BranchID: &branch, Data: raw,
	}
}

// writeEvent writes one shaped event through the transaction's executor,
// the caller's last statement. A nil recorder or event writes nothing.
func (s *Service) writeEvent(ctx context.Context, ev *outbox.Event) error {
	if s.events == nil || ev == nil {
		return nil
	}
	return s.events.Write(ctx, *ev)
}

// record writes the quote's event into the outbox through the transaction's
// executor. fromStatus is set on a transition.
func (s *Service) record(ctx context.Context, q *Quote, eventType, fromStatus string) error {
	return s.writeEvent(ctx, s.buildEvent(q, eventType, fromStatus))
}

// triggerAutoPO creates purchase order lines for the SPECIAL ORDER lines of
// an accepted quote, and only those: an ordinary stocked line is served from
// the stock it allocated, and a product carrying purchase order line for it
// would put its receipt on hand and re-average the cost of stock the dealer
// already holds (PR 40 round 2 P3-2, PR 43 review round 1 P3-3). On the
// convert the gate is the ORDER line the quote line became
// (order_lines.is_special_order); a converted quote line is never a special
// order line, so the convert creates none. This is fire-and-forget: failures
// are logged but don't block acceptance. The purchase order line links to the
// ORDER line (order_lines.quote_line_id; its linked_so_line_id is a foreign
// key to order_lines), so a line that has no order line (no order was
// created, as on the plain transition) links the quote line itself and
// carries no product, the base's inert line only its own tests use.
func (s *Service) triggerAutoPO(ctx context.Context, q *Quote, created *order.Order) {
	orderLine := map[uuid.UUID]*order.OrderLine{}
	if created != nil {
		for i := range created.Lines {
			if created.Lines[i].QuoteLineID != nil {
				orderLine[*created.Lines[i].QuoteLineID] = &created.Lines[i]
			}
		}
	}
	for _, line := range q.Lines {
		// Only create POs for lines that have a unit cost (special order indicator)
		if line.UnitCost > 0 && line.ProductID != nil {
			linked := line.ID
			product := (*uuid.UUID)(nil)
			if ol, ok := orderLine[line.ID]; ok {
				if !ol.IsSpecialOrder {
					continue
				}
				linked = ol.ID
				pid := *line.ProductID
				product = &pid
			} else if created != nil {
				s.logger.Info("auto-PO skipped: the quote line has no order line", "quote_id", q.ID, "line_id", line.ID)
				continue
			}
			err := s.poSvc.CreatePOFromSpecialOrderLine(
				ctx, product, nil, float64(line.Quantity)/10000, float64(line.UnitCost)/10000, linked,
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
