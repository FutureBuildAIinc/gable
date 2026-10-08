// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/google/uuid"
)

// Service defines the business logic for products
type Service struct {
	repo      Repository
	vendorSvc *vendor.Service // Optional: when set, CreateProduct auto-resolves vendor name -> vendor_id
	kits      KitStore        // the kit component routes' store, when the repo can serve it
}

// NewService creates a new Product Service
func NewService(repo Repository) *Service {
	s := &Service{repo: repo}
	if ks, ok := any(repo).(KitStore); ok {
		s.kits = ks
	}
	return s
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

	return s.repo.CreateProduct(ctx, p)
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

// UpdateAverageCost updates the average unit cost for a product
func (s *Service) UpdateAverageCost(ctx context.Context, id uuid.UUID, avgCost float64) error {
	return s.repo.UpdateAverageCost(ctx, id, avgCost)
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

// UpdateMarginRules updates the target margin and commission rate for a product
func (s *Service) UpdateMarginRules(ctx context.Context, id uuid.UUID, targetMargin float64, commissionRate float64, revision int64) (int64, error) {
	newRevision, err := s.repo.UpdateMarginRules(ctx, id, targetMargin, commissionRate, revision)
	return newRevision, resolveRevision(err)
}

// UpdateReorderTargets writes new reorder_point and reorder_qty for a product.
// Used by the purchase_order package's RefreshReorderTargets job.
func (s *Service) UpdateReorderTargets(ctx context.Context, id uuid.UUID, reorderPoint, reorderQty float64) error {
	return s.repo.UpdateReorderTargets(ctx, id, reorderPoint, reorderQty)
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
	newRevision, err := s.repo.UpdateLeadTime(ctx, id, leadTimeDays, revision)
	return newRevision, resolveRevision(err)
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
	newRevision, err := s.repo.UpdateDimensions(ctx, id, g, revision)
	return newRevision, resolveRevision(err)
}
