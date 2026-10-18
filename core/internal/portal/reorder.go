// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ReorderPreviewLineDTO shows how one historical order line's price and stock
// compare against the customer's current pricing today. All money fields are
// integer cents.
type ReorderPreviewLineDTO struct {
	LineID                 uuid.UUID  `json:"line_id"`
	ProductID              *uuid.UUID `json:"product_id,omitempty"`
	SKU                    string     `json:"sku"`
	Description            string     `json:"description"`
	Quantity               float64    `json:"quantity"`
	UOM                    string     `json:"uom"`
	OriginalUnitPriceCents int64      `json:"original_unit_price_cents"`
	CurrentUnitPriceCents  int64      `json:"current_unit_price_cents"`
	PriceDiffCents         int64      `json:"price_diff_cents"`
	OriginalLineTotalCents int64      `json:"original_line_total_cents"`
	CurrentLineTotalCents  int64      `json:"current_line_total_cents"`
	LineDiffCents          int64      `json:"line_diff_cents"`
	PriceSource            string     `json:"price_source"`
	AvailableQty           float64    `json:"available_qty"`
	InStock                bool       `json:"in_stock"`
}

// ReorderPreviewDTO is the price-difference preview for reordering a historical
// order at the account's current prices.
type ReorderPreviewDTO struct {
	SourceOrderID      uuid.UUID               `json:"source_order_id"`
	ProjectID          *uuid.UUID              `json:"project_id,omitempty"`
	Lines              []ReorderPreviewLineDTO `json:"lines"`
	OriginalTotalCents int64                   `json:"original_total_cents"`
	CurrentTotalCents  int64                   `json:"current_total_cents"`
	TotalDiffCents     int64                   `json:"total_diff_cents"`
}

// getYardAvailability returns per-yard stock and lead time for a product across
// active dealer branches, plus the headline shortest lead time.
func (s *Service) getYardAvailability(ctx context.Context, productID uuid.UUID, productLeadTime *int) ([]YardAvailabilityDTO, *int) {
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil || pgRepo.db == nil {
		return nil, productLeadTime
	}

	rows, err := pgRepo.db.GetExecutor(ctx).Query(ctx, `
		SELECT
			l.id,
			l.code,
			l.name,
			COALESCE(SUM(i.quantity), 0)::float8 AS on_hand,
			GREATEST(COALESCE(SUM(i.quantity - i.allocated), 0), 0)::float8 AS available
		FROM locations l
		LEFT JOIN inventory i ON i.location_id = l.id AND i.product_id = $1
		WHERE l.type = 'BRANCH'
		GROUP BY l.id, l.code, l.name
		ORDER BY available DESC, l.code ASC
	`, productID)
	if err != nil {
		return nil, productLeadTime
	}
	defer rows.Close()

	var (
		yards    []YardAvailabilityDTO
		anyStock bool
		shortest *int
	)
	for rows.Next() {
		var y YardAvailabilityDTO
		if err := rows.Scan(&y.YardID, &y.YardCode, &y.YardName, &y.OnHand, &y.Available); err != nil {
			continue
		}
		y.InStock = y.Available > 0
		if productLeadTime != nil {
			if y.InStock {
				anyStock = true
				zero := 0
				y.LeadTimeDays = &zero
			} else {
				lt := *productLeadTime
				y.LeadTimeDays = &lt
				if shortest == nil || lt < *shortest {
					shortest = &lt
				}
			}
		}
		yards = append(yards, y)
	}

	if productLeadTime != nil {
		if anyStock {
			zero := 0
			return yards, &zero
		}
		if shortest != nil {
			return yards, shortest
		}
	}
	return yards, productLeadTime
}

// PreviewReorder computes the line-by-line price difference between a historical
// order and the customer's current resolved pricing.
func (s *Service) PreviewReorder(ctx context.Context, customerID, sourceOrderID uuid.UUID) (*ReorderPreviewDTO, error) {
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return nil, ErrOrderNotFound
	}
	exec := pgRepo.db.GetExecutor(ctx)

	var (
		projectID *uuid.UUID
		branchID  *uuid.UUID
	)
	err := exec.QueryRow(ctx, `
		SELECT project_id, branch_id FROM orders WHERE id = $1 AND customer_id = $2
	`, sourceOrderID, customerID).Scan(&projectID, &branchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrOrderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to verify source order: %w", err)
	}
	if branchID == nil {
		branchID = s.customerBranchID(ctx, customerID)
	}

	rows, err := exec.Query(ctx, `
		SELECT
			ol.id,
			ol.product_id,
			COALESCE(ol.sku, p.sku, ''),
			COALESCE(ol.description, p.description, ''),
			ol.quantity::float8,
			COALESCE(ol.uom::text, p.uom_primary::text, 'EA'),
			COALESCE(ol.price_uom, ol.uom::text, p.uom_primary::text, 'EA'),
			COALESCE(ol.uom_qty, 1)::float8,
			COALESCE(ol.price_uom_qty, 1)::float8,
			ol.unit_price::float8,
			ol.line_total::float8,
			COALESCE(p.base_price, ol.unit_price)::float8
		FROM order_lines ol
		LEFT JOIN products p ON p.id = ol.product_id
		WHERE ol.order_id = $1 AND COALESCE(ol.line_type, 'PRODUCT') <> 'TEXT'
		ORDER BY ol.position ASC, ol.id ASC
	`, sourceOrderID)
	if err != nil {
		return nil, fmt.Errorf("failed to read source order lines: %w", err)
	}
	defer rows.Close()

	type rawLine struct {
		id          uuid.UUID
		productID   *uuid.UUID
		sku         string
		description string
		qty         float64
		uom         string
		priceUOM    string
		uomQty      float64
		priceUOMQty float64
		origUnit    float64
		origTotal   float64
		basePrice   float64
	}
	var rawLines []rawLine
	for rows.Next() {
		var rl rawLine
		if err := rows.Scan(
			&rl.id, &rl.productID, &rl.sku, &rl.description,
			&rl.qty, &rl.uom, &rl.priceUOM, &rl.uomQty, &rl.priceUOMQty,
			&rl.origUnit, &rl.origTotal, &rl.basePrice,
		); err != nil {
			return nil, fmt.Errorf("failed to scan source order line: %w", err)
		}
		rawLines = append(rawLines, rl)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	preview := &ReorderPreviewDTO{
		SourceOrderID: sourceOrderID,
		ProjectID:     projectID,
		Lines:         make([]ReorderPreviewLineDTO, 0, len(rawLines)),
	}

	for _, rl := range rawLines {
		uq := rl.uomQty
		pq := rl.priceUOMQty
		if uq <= 0 {
			uq = 1
		}
		if pq <= 0 {
			pq = 1
		}

		curUnit := rl.basePrice
		priceSource := "retail"
		var avail float64

		if rl.productID != nil {
			saleUOM := rl.uom
			if saleUOM == "" {
				saleUOM = rl.priceUOM
			}
			if pricePerSaleUOM, src, ok := s.resolveCustomerPriceAtBranch(ctx, customerID, branchID, *rl.productID, saleUOM, rl.qty); ok {
				curUnit = pricePerSaleUOM * (uq / pq)
				priceSource = src
			}
			avail = s.getAvailableQty(ctx, *rl.productID)
		} else {
			curUnit = rl.origUnit
			priceSource = "historical"
		}

		origUnitCents := int64(math.Round(rl.origUnit * 100))
		curUnitCents := int64(math.Round(curUnit * 100))
		origTotalCents := int64(math.Round(rl.origTotal * 100))
		curTotalCents := int64(math.Round((rl.qty * curUnit * pq / uq) * 100))

		lineDTO := ReorderPreviewLineDTO{
			LineID:                 rl.id,
			ProductID:              rl.productID,
			SKU:                    rl.sku,
			Description:            rl.description,
			Quantity:               rl.qty,
			UOM:                    rl.uom,
			OriginalUnitPriceCents: origUnitCents,
			CurrentUnitPriceCents:  curUnitCents,
			PriceDiffCents:         curUnitCents - origUnitCents,
			OriginalLineTotalCents: origTotalCents,
			CurrentLineTotalCents:  curTotalCents,
			LineDiffCents:          curTotalCents - origTotalCents,
			PriceSource:            priceSource,
			AvailableQty:           avail,
			InStock:                avail >= rl.qty && avail > 0,
		}
		preview.Lines = append(preview.Lines, lineDTO)
		preview.OriginalTotalCents += origTotalCents
		preview.CurrentTotalCents += curTotalCents
	}
	preview.TotalDiffCents = preview.CurrentTotalCents - preview.OriginalTotalCents

	return preview, nil
}

// repriceReorderedDraft updates product lines on a newly duplicated reorder draft
// with the customer's resolved pricing from pricing.Resolve (M5).
func (s *Service) repriceReorderedDraft(ctx context.Context, customerID, orderID uuid.UUID) error {
	if s.pricingSvc == nil {
		return nil
	}
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return nil
	}
	exec := pgRepo.db.GetExecutor(ctx)

	var branchID *uuid.UUID
	_ = exec.QueryRow(ctx, `SELECT branch_id FROM orders WHERE id = $1 AND customer_id = $2`, orderID, customerID).Scan(&branchID)
	if branchID == nil {
		branchID = s.customerBranchID(ctx, customerID)
	}

	rows, err := exec.Query(ctx, `
		SELECT id, product_id, quantity::float8,
		       COALESCE(uom::text, price_uom, 'EA'),
		       COALESCE(price_uom, uom::text, 'EA'),
		       COALESCE(uom_qty, 1)::float8,
		       COALESCE(price_uom_qty, 1)::float8
		FROM order_lines
		WHERE order_id = $1 AND product_id IS NOT NULL AND COALESCE(line_type, 'PRODUCT') = 'PRODUCT'
	`, orderID)
	if err != nil {
		return err
	}
	type lineToPrice struct {
		id          uuid.UUID
		productID   uuid.UUID
		qty         float64
		uom         string
		priceUOM    string
		uomQty      float64
		priceUOMQty float64
	}
	var lines []lineToPrice
	for rows.Next() {
		var l lineToPrice
		if err := rows.Scan(&l.id, &l.productID, &l.qty, &l.uom, &l.priceUOM, &l.uomQty, &l.priceUOMQty); err != nil {
			rows.Close()
			return err
		}
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	updatedAny := false
	for _, l := range lines {
		saleUOM := l.uom
		if saleUOM == "" {
			saleUOM = l.priceUOM
		}
		pricePerSaleUOM, _, ok := s.resolveCustomerPriceAtBranch(ctx, customerID, branchID, l.productID, saleUOM, l.qty)
		if !ok || pricePerSaleUOM <= 0 {
			continue
		}
		uq := l.uomQty
		pq := l.priceUOMQty
		if uq <= 0 {
			uq = 1
		}
		if pq <= 0 {
			pq = 1
		}
		unitPrice := pricePerSaleUOM * (uq / pq)
		lineTotal := math.Round((l.qty*pricePerSaleUOM)*100) / 100
		if _, err := exec.Exec(ctx, `
			UPDATE order_lines
			SET unit_price = $2,
			    priced_unit_price = $2,
			    price_source = 'PRICE_LIST',
			    line_total = $3
			WHERE id = $1
		`, l.id, unitPrice, lineTotal); err != nil {
			return err
		}
		updatedAny = true
	}

	if updatedAny {
		_, err = exec.Exec(ctx, `
			UPDATE orders SET
				total_amount = (SELECT COALESCE(SUM(line_total), 0) FROM order_lines WHERE order_id = $1),
				subtotal = (SELECT COALESCE(SUM(line_total), 0) FROM order_lines WHERE order_id = $1),
				updated_at = NOW()
			WHERE id = $1
		`, orderID)
		return err
	}
	return nil
}

