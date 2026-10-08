// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

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

// AuditLogger writes an audit row for a product write; *audit.Logger
// satisfies it.
type AuditLogger interface {
	Log(ctx context.Context, entry audit.Entry) error
}

// RevisionPrecondition carries the If-Match header and the body revision a
// write states; httpx.CheckRevision resolves them against the locked row.
type RevisionPrecondition struct {
	IfMatch  string
	Revision *int64
}

// Event types the module writes to the outbox.
const (
	EventProductCreated = "product.created"
	EventProductUpdated = "product.updated"
)

// Service defines the business logic for products
type Service struct {
	repo      Repository
	vendorSvc *vendor.Service // Optional: when set, CreateProduct auto-resolves vendor name -> vendor_id
	kits      KitStore        // the kit component routes' store, when the repo can serve it
	events    EventRecorder   // optional; nil records nothing (unit tests)
	tx        TxRunner        // optional; nil runs each write unwrapped (unit tests)
	audits    AuditLogger     // optional; nil writes no audit rows (unit tests)
}

// NewService creates a new Product Service
func NewService(repo Repository) *Service {
	s := &Service{repo: repo}
	if ks, ok := any(repo).(KitStore); ok {
		s.kits = ks
	}
	return s
}

// WithOutbox wires the recorder of the product events.
func (s *Service) WithOutbox(events EventRecorder) *Service {
	s.events = events
	return s
}

// WithTxRunner wires the transaction wrapper every write uses, so the write,
// its revision move, its audit row and its event are one transactional fact.
func (s *Service) WithTxRunner(tx TxRunner) *Service {
	s.tx = tx
	return s
}

// WithAudit wires the audit rows of the product writes.
func (s *Service) WithAudit(a AuditLogger) *Service {
	s.audits = a
	return s
}

// inTx runs fn in one transaction when a runner is wired.
func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

// recordEvent writes the module's event into the outbox through the
// transaction's executor; parts names what the write moved.
func (s *Service) recordEvent(ctx context.Context, id uuid.UUID, sku string, revision int64, eventType string, parts ...string) error {
	if s.events == nil {
		return nil
	}
	data := map[string]any{"sku": sku, "revision": revision}
	if len(parts) > 0 {
		data["parts"] = parts
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: "product", EntityID: id, Data: raw,
	})
}

// auditChange writes the mutation's audit row through the transaction's
// executor; a failed audit write fails the mutation (the recipe's rule).
func (s *Service) auditChange(ctx context.Context, id uuid.UUID, sku string, revision int64, action string, extra map[string]any) error {
	if s.audits == nil {
		return nil
	}
	changes := map[string]any{"sku": sku, "revision": revision}
	for k, v := range extra {
		changes[k] = v
	}
	return s.audits.Log(ctx, audit.Entry{
		Action: action, EntityType: "product", EntityID: id, Changes: changes,
	})
}

// WithVendorService attaches the vendor service so CreateProduct can resolve
// a free-text vendor name to a canonical vendor_id via EnsureVendorByName.
func (s *Service) WithVendorService(v *vendor.Service) *Service {
	s.vendorSvc = v
	return s
}

// CreateProduct creates a new product. If a vendor name is supplied without a
// vendor_id and the vendor service is wired, the vendor row is upserted and
// the resulting UUID is stamped onto the product so the two columns can never
// drift out of sync.
func (s *Service) CreateProduct(ctx context.Context, p *Product) error {
	if p.SKU == "" {
		return httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "sku", Message: "is required"})
	}
	if p.Description == "" {
		return httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "description", Message: "is required"})
	}
	if !ValidUOM(p.UOMPrimary) {
		return httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "stock_uom", Message: "must be one of the unit codes the catalogue holds"})
	}
	if p.BasePriceScaled < 0 {
		return httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "base_price_ten_thousandths", Message: "a unit price is never negative"})
	}

	if p.VendorID == nil && p.Vendor != nil && *p.Vendor != "" && s.vendorSvc != nil {
		v, err := s.vendorSvc.EnsureVendorByName(ctx, *p.Vendor)
		if err != nil {
			return fmt.Errorf("resolve vendor: %w", err)
		}
		p.VendorID = &v.ID
	}

	// If vendor_id was supplied but no display name (e.g. dropdown selection),
	// hydrate the display name so the legacy column stays consistent.
	if p.VendorID != nil && (p.Vendor == nil || *p.Vendor == "") && s.vendorSvc != nil {
		v, err := s.vendorSvc.GetVendor(ctx, *p.VendorID)
		if err == nil && v != nil {
			name := v.Name
			p.Vendor = &name
		}
	}

	// The create, its audit row and its event are one transactional fact
	// (the recipe's rule): a failed audit or event write fails the create.
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.CreateProduct(ctx, p); err != nil {
			return err
		}
		if err := s.auditChange(ctx, p.ID, p.SKU, p.Revision, EventProductCreated, nil); err != nil {
			return err
		}
		return s.recordEvent(ctx, p.ID, p.SKU, p.Revision, EventProductCreated)
	})
}

// ListProducts returns all products (the reorder scheduler's read)
func (s *Service) ListProducts(ctx context.Context) ([]Product, error) {
	return s.repo.ListProducts(ctx)
}

// ListProductsPage is the list's keyset page: after is nil for the first
// page, and limit+1 rows are read so the handler knows whether another page
// exists.
func (s *Service) ListProductsPage(ctx context.Context, after *time.Time, afterID *uuid.UUID, limit int) ([]Product, bool, error) {
	rows, err := s.repo.ListProductsPage(ctx, after, afterID, limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	return rows, more, nil
}

// CountProducts is the include=total count.
func (s *Service) CountProducts(ctx context.Context) (int64, error) {
	return s.repo.CountProducts(ctx)
}

// GetProduct retrieves a product by its ID
func (s *Service) GetProduct(ctx context.Context, id uuid.UUID) (*Product, error) {
	return s.repo.GetProduct(ctx, id)
}

// ListBelowReorder returns products below their reorder point
func (s *Service) ListBelowReorder(ctx context.Context) ([]ReorderAlert, error) {
	return s.repo.ListBelowReorder(ctx)
}

// UpdateAverageCost updates the average unit cost for a product (a receipt
// or a return moves it), with its audit row and event in the same
// transaction. The caller may already hold the transaction (the purchase
// order receive does); the nested runner joins it.
func (s *Service) UpdateAverageCost(ctx context.Context, id uuid.UUID, avgCost float64) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.UpdateAverageCost(ctx, id, avgCost); err != nil {
			return err
		}
		p, err := s.repo.GetProduct(ctx, id)
		if err != nil {
			return err
		}
		if err := s.auditChange(ctx, id, p.SKU, p.Revision, EventProductUpdated, map[string]any{"parts": []string{"average_cost"}}); err != nil {
			return err
		}
		return s.recordEvent(ctx, id, p.SKU, p.Revision, EventProductUpdated, "average_cost")
	})
}

// resolveRevision turns a repository write refusal into the boundary error:
// a missing row is 404, a stale revision is 409 (ADR 0001 section 11).
func resolveRevision(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound("no such product")
	case errors.Is(err, ErrStaleRevision):
		return httpx.StaleRevision("the product was changed after this revision was read; reload and retry")
	default:
		return err
	}
}

// UpdateMarginRules updates the target margin and commission rate for a
// product, with its audit row and event in the same transaction.
func (s *Service) UpdateMarginRules(ctx context.Context, id uuid.UUID, targetMargin float64, commissionRate float64, revision int64) (int64, error) {
	var newRevision int64
	err := s.inTx(ctx, func(ctx context.Context) error {
		p, err := s.repo.GetProduct(ctx, id)
		if err != nil {
			return resolveRevision(err)
		}
		newRevision, err = s.repo.UpdateMarginRules(ctx, id, targetMargin, commissionRate, revision)
		if err != nil {
			return resolveRevision(err)
		}
		if err := s.auditChange(ctx, id, p.SKU, newRevision, EventProductUpdated, map[string]any{"parts": []string{"margins"}}); err != nil {
			return err
		}
		return s.recordEvent(ctx, id, p.SKU, newRevision, EventProductUpdated, "margins")
	})
	return newRevision, err
}

// UpdateReorderTargets writes new reorder_point and reorder_qty for a product
// (the purchase_order package's RefreshReorderTargets job), with its audit
// row and event in the same transaction. The scheduler is a system writer
// and carries no revision.
func (s *Service) UpdateReorderTargets(ctx context.Context, id uuid.UUID, reorderPoint, reorderQty float64) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.UpdateReorderTargets(ctx, id, reorderPoint, reorderQty); err != nil {
			return err
		}
		p, err := s.repo.GetProduct(ctx, id)
		if err != nil {
			return err
		}
		if err := s.auditChange(ctx, id, p.SKU, p.Revision, EventProductUpdated, map[string]any{"parts": []string{"reorder_targets"}}); err != nil {
			return err
		}
		return s.recordEvent(ctx, id, p.SKU, p.Revision, EventProductUpdated, "reorder_targets")
	})
}

// UpdateLeadTime publishes (or clears) the dealer's lead time for a product.
//
// The only rule is that a published lead time cannot be negative. nil is
// passed through untouched: it is the "unpublished" state the portal catalog
// renders as JSON null, and it must stay distinguishable from a published 0.
// Guessing a plausible number here would be worse than publishing nothing —
// a crew gets scheduled around a lead time.
func (s *Service) UpdateLeadTime(ctx context.Context, id uuid.UUID, leadTimeDays *int, revision int64) (int64, error) {
	if leadTimeDays != nil && *leadTimeDays < 0 {
		return 0, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "lead_time_days", Message: "must be zero or positive"})
	}
	var newRevision int64
	err := s.inTx(ctx, func(ctx context.Context) error {
		p, err := s.repo.GetProduct(ctx, id)
		if err != nil {
			return resolveRevision(err)
		}
		newRevision, err = s.repo.UpdateLeadTime(ctx, id, leadTimeDays, revision)
		if err != nil {
			return resolveRevision(err)
		}
		if err := s.auditChange(ctx, id, p.SKU, newRevision, EventProductUpdated, map[string]any{"parts": []string{"lead_time"}}); err != nil {
			return err
		}
		return s.recordEvent(ctx, id, p.SKU, newRevision, EventProductUpdated, "lead_time")
	})
	return newRevision, err
}

// UpdateDimensions writes the parametric 3D geometry (inches) for a product.
// The PIM is the canonical digital-twin source AI_LM's Load Builder consumes.
//
// The only business rule is the provenance label, and it exists to keep
// geometry_source from lying:
//
//   - An explicit non-empty geometry_source from the caller always wins. This
//     is the forward-compat seam for a future 'mesh' source.
//   - Otherwise, a triple with at least one recorded dimension is 'parametric'
//     — the convention the seed data and the integration layer's
//     resolveGeometrySource() already use for operator-entered geometry.
//   - Otherwise (the operator cleared every dimension) the source is cleared to
//     NULL too. Leaving 'parametric' behind on a SKU with no dimensions would
//     claim a provenance for geometry that does not exist, which is precisely
//     the lie resolveGeometrySource() refuses to tell on the read path.
//
// Nothing else is defaulted. In particular a nil dimension is passed through as
// nil rather than coerced to 0 — see the Geometry doc comment.
func (s *Service) UpdateDimensions(ctx context.Context, id uuid.UUID, g Geometry, revision int64) (int64, error) {
	if g.GeometrySource != nil && strings.TrimSpace(*g.GeometrySource) == "" {
		g.GeometrySource = nil
	}
	switch {
	case !g.HasDimensions():
		g.GeometrySource = nil
	case g.GeometrySource == nil:
		src := GeometrySourceParametric
		g.GeometrySource = &src
	}
	var newRevision int64
	err := s.inTx(ctx, func(ctx context.Context) error {
		p, err := s.repo.GetProduct(ctx, id)
		if err != nil {
			return resolveRevision(err)
		}
		newRevision, err = s.repo.UpdateDimensions(ctx, id, g, revision)
		if err != nil {
			return resolveRevision(err)
		}
		if err := s.auditChange(ctx, id, p.SKU, newRevision, EventProductUpdated, map[string]any{"parts": []string{"dimensions"}}); err != nil {
			return err
		}
		return s.recordEvent(ctx, id, p.SKU, newRevision, EventProductUpdated, "dimensions")
	})
	return newRevision, err
}
