// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/google/uuid"
)

// catalogRow is an internal struct for raw DB product rows before enrichment.
type catalogRow struct {
	ID        uuid.UUID
	SKU       string
	Name      string
	Category  string
	Species   string
	Grade     string
	ImageURL  string
	UOM       string
	BasePrice float64
	WeightLbs float64
	UPC       string
	Vendor    string

	// LeadTimeDays is a POINTER so an unpublished lead time stays NULL all the
	// way to the JSON. Collapsing it to 0 here would publish "available today"
	// for every product the dealer has never entered a lead time for.
	LeadTimeDays *int
	CategoryID   *uuid.UUID
	CategorySlug string
	CategoryPath string
}

// toDTO projects a raw row onto the customer-facing DTO, minus the pricing and
// availability enrichment the caller adds. Having one place that does this is
// what keeps the list and the detail views from drifting apart — the detail
// view used to be a hand-copied duplicate of the list's field assignments.
func (row catalogRow) toDTO() CatalogProductDTO {
	return CatalogProductDTO{
		ID:           row.ID,
		SKU:          row.SKU,
		Name:         row.Name,
		Category:     row.Category,
		Species:      row.Species,
		Grade:        row.Grade,
		ImageURL:     row.ImageURL,
		UOM:          row.UOM,
		BasePrice:    row.BasePrice,
		LeadTimeDays: row.LeadTimeDays,
		CategoryID:   row.CategoryID,
		CategorySlug: row.CategorySlug,
		CategoryPath: row.CategoryPath,
	}
}

// ListCatalog returns catalog products enriched with customer-specific pricing and availability.
func (s *Service) ListCatalog(ctx context.Context, customerID uuid.UUID, filter CatalogFilter) ([]CatalogProductDTO, error) {
	rows, err := s.repo.ListCatalogProducts(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list catalog: %w", err)
	}

	cust, err := s.customerSvc.GetCustomer(ctx, customerID)
	if err != nil {
		s.logger.Warn("Catalog: could not load customer for pricing, using base prices", "customer_id", customerID, "error", err)
		cust = &customer.Customer{ID: customerID}
	}

	products := make([]CatalogProductDTO, 0, len(rows))
	for _, row := range rows {
		dto := row.toDTO()

		// Pricing waterfall
		if s.pricingSvc != nil && cust != nil {
			cp, pErr := s.pricingSvc.CalculatePrice(ctx, cust, row.ID, row.BasePrice)
			if pErr == nil {
				dto.CustomerPrice = cp.FinalPrice
				dto.PriceSource = string(cp.Source)
			} else {
				dto.CustomerPrice = row.BasePrice
				dto.PriceSource = "retail"
			}
		} else {
			dto.CustomerPrice = row.BasePrice
			dto.PriceSource = "retail"
		}

		// Inventory availability
		dto.Available = s.getAvailableQty(ctx, row.ID)
		dto.InStock = dto.Available > 0

		products = append(products, dto)
	}

	return products, nil
}

// GetCatalogProduct returns a single product detail with customer pricing and availability.
func (s *Service) GetCatalogProduct(ctx context.Context, customerID, productID uuid.UUID) (*CatalogDetailDTO, error) {
	row, err := s.repo.GetCatalogProduct(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("product not found: %w", err)
	}

	cust, err := s.customerSvc.GetCustomer(ctx, customerID)
	if err != nil {
		cust = &customer.Customer{ID: customerID}
	}

	dto := &CatalogDetailDTO{
		CatalogProductDTO: row.toDTO(),
		WeightLbs:         row.WeightLbs,
		UPC:               row.UPC,
		Vendor:            row.Vendor,
		VolumeBreaks:      []VolumeBreakDTO{},
	}

	// Pricing waterfall
	if s.pricingSvc != nil && cust != nil {
		cp, pErr := s.pricingSvc.CalculatePrice(ctx, cust, row.ID, row.BasePrice)
		if pErr == nil {
			dto.CustomerPrice = cp.FinalPrice
			dto.PriceSource = string(cp.Source)
		} else {
			dto.CustomerPrice = row.BasePrice
			dto.PriceSource = "retail"
		}
	} else {
		dto.CustomerPrice = row.BasePrice
		dto.PriceSource = "retail"
	}

	// Volume breaks. A failure here degrades to an empty ladder rather than
	// failing the whole product page: the price the customer pays today is
	// already resolved above, and "no breaks shown" is a safe under-promise
	// where "product unavailable" is not.
	if breaks, bErr := s.VolumeBreaks(ctx, customerID, row.ID, row.BasePrice); bErr == nil {
		dto.VolumeBreaks = breaks
	} else {
		s.logger.Warn("Catalog: volume breaks unavailable",
			"product_id", row.ID, "customer_id", customerID, "error", bErr)
	}

	// Inventory
	dto.Available = s.getAvailableQty(ctx, row.ID)
	dto.InStock = dto.Available > 0

	return dto, nil
}

// VolumeBreaks returns this customer's quantity ladder for a product.
//
// It delegates the arithmetic entirely to pricing.Service.VolumeBreaks, which
// derives every rung by asking the real waterfall what it would charge at that
// quantity. The portal's job here is tenancy (the customer comes from the
// session, never from the request) and DTO shape — not pricing.
func (s *Service) VolumeBreaks(ctx context.Context, customerID, productID uuid.UUID, basePrice float64) ([]VolumeBreakDTO, error) {
	out := make([]VolumeBreakDTO, 0)
	if s.pricingSvc == nil {
		return out, nil
	}

	cust, err := s.customerSvc.GetCustomer(ctx, customerID)
	if err != nil {
		// Without the customer there is no tier, no contract and no
		// account-scoped rule, so any ladder computed here would be a
		// different customer's. Return nothing rather than a retail ladder
		// dressed up as theirs.
		return out, nil
	}

	breaks, err := s.pricingSvc.VolumeBreaks(ctx, cust, productID, basePrice)
	if err != nil {
		return nil, err
	}
	for _, b := range breaks {
		out = append(out, VolumeBreakDTO{
			MinQuantity:  b.MinQuantity,
			UnitPrice:    b.UnitPrice,
			PriceSource:  string(b.Source),
			Details:      b.Details,
			SavesPerUnit: b.SavesPerUnit,
		})
	}
	return out, nil
}

// VolumeBreaksForProduct is the standalone read behind
// GET /catalog/{id}/volume-breaks, for a consumer that wants the ladder
// without re-fetching the whole product.
func (s *Service) VolumeBreaksForProduct(ctx context.Context, customerID, productID uuid.UUID) ([]VolumeBreakDTO, error) {
	row, err := s.repo.GetCatalogProduct(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("product not found: %w", err)
	}
	return s.VolumeBreaks(ctx, customerID, row.ID, row.BasePrice)
}

// getAvailableQty computes total available quantity across all locations.
func (s *Service) getAvailableQty(ctx context.Context, productID uuid.UUID) float64 {
	if s.inventorySvc == nil {
		return 0
	}
	items, err := s.inventorySvc.ListByProduct(ctx, productID.String())
	if err != nil {
		return 0
	}
	var total float64
	for _, item := range items {
		total += item.Quantity - item.Allocated
	}
	if total < 0 {
		return 0
	}
	return total
}

// Ensure imported packages are used.
var (
	_ = (*pricing.Service)(nil)
	_ = (*product.Service)(nil)
	_ = (*inventory.Service)(nil)
)
