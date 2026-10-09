// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// RecommendationConfig holds the reorder job's tunable parameters.
type RecommendationConfig struct {
	// DefaultLeadTimeDays used when the vendor's measured lead time is empty.
	DefaultLeadTimeDays float64
	// LookbackDays the velocity window.
	LookbackDays int
}

// DefaultRecommendationConfig returns the production defaults.
func DefaultRecommendationConfig() RecommendationConfig {
	return RecommendationConfig{
		DefaultLeadTimeDays: 7,
		LookbackDays:        90,
	}
}

// ReorderRecommendation is one due product and branch of ADR 0008 section
// 10.3's payload: the stock figures, the on order quantity the base
// commit's engine ignored, the velocity with its lookback, the lead time
// with its source, and the suggested quantity. Stored per run, served by
// GET /purchase-orders/recommendations, and carried whole as the
// reorder.recommended event's data.
type ReorderRecommendation struct {
	ID         uuid.UUID  `json:"id"`
	RunID      uuid.UUID  `json:"run_id"`
	ProductID  uuid.UUID  `json:"product_id"`
	BranchID   uuid.UUID  `json:"branch_id"`
	VendorID   *uuid.UUID `json:"vendor_id"`
	SKU        string     `json:"sku"`
	Description string    `json:"description"`

	OnHand     httpx.Quantity `json:"on_hand"`
	Allocated  httpx.Quantity `json:"allocated"`
	Available  httpx.Quantity `json:"available"`
	OnOrder    httpx.Quantity `json:"on_order"`
	Backordered httpx.Quantity `json:"backordered"`

	Velocity    httpx.Quantity `json:"velocity"`
	LookbackDays int            `json:"lookback_days"`
	LeadTimeDays httpx.Quantity `json:"lead_time_days"`
	LeadTimeSource string       `json:"lead_time_source"`

	ReorderPoint     *httpx.Quantity `json:"reorder_point"`
	ReorderQuantity  *httpx.Quantity `json:"reorder_quantity"`
	SuggestedQuantity float64         `json:"suggested_quantity"`

	VendorItemID *uuid.UUID `json:"vendor_item_id"`
	Unit         string     `json:"unit"`
	Status       string     `json:"status"`
	PurchaseOrderLineID *uuid.UUID `json:"purchase_order_line_id"`
	CreatedAt    httpx.Timestamp `json:"created_at"`

	// OldPointF and OldQtyF carry the target pair the run read; they are
	// internal, not on the wire.
	OldPointF float64 `json:"-"`
	OldQtyF   float64 `json:"-"`
}

// EventData is the recommendation's whole payload as the event carries it.
func (r ReorderRecommendation) EventData() map[string]any {
	return map[string]any{
		"product_id": r.ProductID, "branch_id": r.BranchID, "vendor_id": r.VendorID,
		"on_hand": r.OnHand.WireString(), "allocated": r.Allocated.WireString(),
		"available": r.Available.WireString(), "on_order": r.OnOrder.WireString(),
		"backordered": r.Backordered.WireString(), "velocity": r.Velocity.WireString(),
		"lookback_days": r.LookbackDays, "lead_time_days": r.LeadTimeDays.WireString(),
		"lead_time_source": r.LeadTimeSource, "reorder_point": r.ReorderPoint,
		"reorder_quantity": r.ReorderQuantity, "suggested_quantity": r.SuggestedQuantity,
		"vendor_item_id": r.VendorItemID, "unit": r.Unit,
	}
}

// reorderScanRow is one product and branch of the reorder scan.
type reorderScanRow struct {
	ProductID, BranchID uuid.UUID
	OnHand, Allocated, Available,
	OnOrder, Backordered, UnitsSold httpx.Quantity
	OldPointF, OldQtyF float64
	AvailableF, OnOrderF, BackorderedF float64
	VendorID    *uuid.UUID
	Unit, SKU, Description string
}

// reorderScanQuery computes, per product and branch, the stock figures, the
// on order quantity (purchase lines of sent or partial purchase orders,
// ordered less received), the backordered quantity, and the velocity over
// the lookback from the sales history (actual issue moves arrive with the
// stock ledger in C4-2 A). It reads as a system caller: the refresh runs
// per branch over every branch, the way the cron path always did.
const reorderScanQuery = `
WITH stock AS (
  SELECT i.product_id, l.branch_id,
         SUM(i.quantity) AS on_hand, SUM(i.allocated) AS allocated,
         SUM(i.quantity - i.allocated) AS available
  FROM inventory i JOIN locations l ON l.id = i.location_id
  WHERE l.branch_id IS NOT NULL
  GROUP BY i.product_id, l.branch_id
),
sales AS (
  SELECT ol.product_id, o.branch_id, SUM(ol.quantity) AS units
  FROM order_lines ol JOIN orders o ON o.id = ol.order_id
  WHERE ol.product_id IS NOT NULL AND o.status <> 'CANCELLED'
    AND ol.created_at >= now() - ($1::int * INTERVAL '1 day')
  GROUP BY ol.product_id, o.branch_id
),
onorder AS (
  SELECT po.branch_id, pol.product_id,
         SUM(COALESCE(pol.stock_quantity, pol.quantity) - COALESCE(pol.qty_received, 0)) AS qty
  FROM purchase_order_lines pol JOIN purchase_orders po ON po.id = pol.po_id
  WHERE pol.product_id IS NOT NULL AND po.status IN ('SENT', 'PARTIAL')
  GROUP BY po.branch_id, pol.product_id
),
backorder AS (
  SELECT o.branch_id, ol.product_id, SUM(COALESCE(ol.quantity_backordered, 0)) AS qty
  FROM order_lines ol JOIN orders o ON o.id = ol.order_id
  WHERE ol.product_id IS NOT NULL AND o.status <> 'CANCELLED'
  GROUP BY o.branch_id, ol.product_id
),
combos AS (
  SELECT product_id, branch_id FROM stock
  UNION SELECT product_id, branch_id FROM sales
  UNION SELECT product_id, branch_id FROM onorder
  UNION SELECT product_id, branch_id FROM backorder
)
SELECT c.product_id, c.branch_id,
       COALESCE(st.on_hand, 0)::text, COALESCE(st.allocated, 0)::text, COALESCE(st.available, 0)::text,
       COALESCE(oo.qty, 0)::text, COALESCE(bo.qty, 0)::text, COALESCE(sa.units, 0)::text,
       COALESCE(sl.reorder_point, p.reorder_point)::float8, COALESCE(sl.reorder_quantity, p.reorder_qty)::float8,
       p.vendor_id, p.uom_primary::text, p.sku, p.description
FROM combos c
LEFT JOIN stock st ON st.product_id = c.product_id AND st.branch_id = c.branch_id
LEFT JOIN sales sa ON sa.product_id = c.product_id AND sa.branch_id = c.branch_id
LEFT JOIN onorder oo ON oo.product_id = c.product_id AND oo.branch_id = c.branch_id
LEFT JOIN backorder bo ON bo.product_id = c.product_id AND bo.branch_id = c.branch_id
LEFT JOIN stock_levels sl ON sl.product_id = c.product_id AND sl.branch_id = c.branch_id
JOIN products p ON p.id = c.product_id
ORDER BY c.product_id, c.branch_id`

// ReorderScan runs the reorder scan.
func (r *Repository) ReorderScan(ctx context.Context, lookbackDays int) ([]reorderScanRow, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, reorderScanQuery, lookbackDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []reorderScanRow
	for rows.Next() {
		var row reorderScanRow
		var onHand, allocated, available, onOrder, backordered, units string
		if err := rows.Scan(&row.ProductID, &row.BranchID,
			&onHand, &allocated, &available, &onOrder, &backordered, &units,
			&row.OldPointF, &row.OldQtyF,
			&row.VendorID, &row.Unit, &row.SKU, &row.Description); err != nil {
			return nil, err
		}
		q := func(s string) httpx.Quantity {
			v, err := httpx.ParseQuantity(s)
			if err != nil {
				return 0
			}
			return v
		}
		row.OnHand, row.Allocated, row.Available = q(onHand), q(allocated), q(available)
		row.OnOrder, row.Backordered, row.UnitsSold = q(onOrder), q(backordered), q(units)
		row.AvailableF = float64(int64(row.Available)) / 10000
		row.OnOrderF = float64(int64(row.OnOrder)) / 10000
		row.BackorderedF = float64(int64(row.Backordered)) / 10000
		out = append(out, row)
	}
	return out, rows.Err()
}

// WriteStockLevelTarget upserts the per branch target pair (ADR 0008 10.2).
func (r *Repository) WriteStockLevelTarget(ctx context.Context, productID, branchID uuid.UUID, point, qty float64) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO stock_levels (product_id, branch_id, reorder_point, reorder_quantity, revision, updated_at)
		VALUES ($1, $2, $3, $4, 1, NOW())
		ON CONFLICT (product_id, branch_id) DO UPDATE
		SET reorder_point = EXCLUDED.reorder_point, reorder_quantity = EXCLUDED.reorder_quantity,
		    revision = stock_levels.revision + 1, updated_at = NOW()`,
		productID, branchID, point, qty)
	return err
}

// StartBranchReorderRun opens a reorder run row stamped with its branch.
func (r *Repository) StartBranchReorderRun(ctx context.Context, job string, dryRun bool, branch uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO reorder_runs (job, dry_run, status, branch_id)
		VALUES ($1, $2, 'RUNNING', $3) RETURNING id`, job, dryRun, branch).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert reorder_runs: %w", err)
	}
	return id, nil
}

// InsertRecommendation stores one recommendation of a run.
func (r *Repository) InsertRecommendation(ctx context.Context, runID uuid.UUID, rec ReorderRecommendation) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO reorder_recommendations (run_id, product_id, branch_id, vendor_id,
			on_hand, allocated, available, on_order, backordered,
			velocity, lookback_days, lead_time_days, lead_time_source,
			reorder_point, reorder_quantity, suggested_quantity, unit, status)
		VALUES ($1, $2, $3, $4,
			$5::numeric, $6::numeric, $7::numeric, $8::numeric, $9::numeric,
			$10::numeric, $11, $12::numeric, $13,
			$14::numeric, $15::numeric, $16, $17, 'OPEN')`,
		runID, rec.ProductID, rec.BranchID, rec.VendorID,
		rec.OnHand.DecimalString(), rec.Allocated.DecimalString(), rec.Available.DecimalString(),
		rec.OnOrder.DecimalString(), rec.Backordered.DecimalString(),
		rec.Velocity.DecimalString(), rec.LookbackDays, rec.LeadTimeDays.DecimalString(), rec.LeadTimeSource,
		recPoint(rec.ReorderPoint), recPoint(rec.ReorderQuantity), rec.SuggestedQuantity, rec.Unit)
	return err
}

func recPoint(q *httpx.Quantity) any {
	if q == nil {
		return nil
	}
	return q.DecimalString()
}

// RecommendationFilter is the stored recommendations list's filters.
type RecommendationFilter struct {
	BranchID  *uuid.UUID
	VendorID  *uuid.UUID
	Status    string
	AfterTime *time.Time
	AfterID   *uuid.UUID
	Limit     int
}

// recListFilters is the list's shared predicate: the three arm branch wall
// on the recommendation's own branch, the vendor and status filters.
const recListFilters = `
	WHERE (
	    ($1::uuid IS NOT NULL AND rr.branch_id = $1)
	    OR ($1::uuid IS NULL AND $%d::text IS NOT NULL AND rr.branch_id IN
	        (SELECT branch_id FROM user_locations WHERE user_sub = $%d))
	    OR ($1::uuid IS NULL AND $%d::text IS NULL)
	  )
	  AND ($2::uuid IS NULL OR rr.vendor_id = $2)
	  AND ($3::text IS NULL OR rr.status = $3)`

// ListRecommendationsPage reads one page of the stored recommendations.
func (r *Repository) ListRecommendationsPage(ctx context.Context, f RecommendationFilter) ([]ReorderRecommendation, bool, error) {
	q := `
		SELECT rr.id, rr.run_id, rr.product_id, rr.branch_id, rr.vendor_id,
		       rr.on_hand::text, rr.allocated::text, rr.available::text, rr.on_order::text, rr.backordered::text,
		       rr.velocity::text, rr.lookback_days, rr.lead_time_days::text, rr.lead_time_source,
		       rr.reorder_point::text, rr.reorder_quantity::text, rr.suggested_quantity::text,
		       rr.vendor_item_id, rr.unit, rr.status, rr.purchase_order_line_id, rr.created_at,
		       p.sku, p.description
		FROM reorder_recommendations rr
		JOIN products p ON p.id = rr.product_id` +
		fmt.Sprintf(recListFilters, 7, 7, 7) + `
		  AND ($4::timestamptz IS NULL OR (rr.created_at, rr.id) < ($4, $5::uuid))
		ORDER BY rr.created_at DESC, rr.id DESC
		LIMIT $6`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q,
		middleware.BranchIDForQuery(ctx), f.VendorID, statusPtr(f.Status), f.AfterTime, f.AfterID, f.Limit+1,
		middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []ReorderRecommendation
	for rows.Next() {
		rec, err := scanRecommendation(rows.Scan)
		if err != nil {
			return nil, false, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > f.Limit
	if more {
		out = out[:f.Limit]
	}
	return out, more, nil
}

// CountRecommendations counts the filtered set.
func (r *Repository) CountRecommendations(ctx context.Context, f RecommendationFilter) (int64, error) {
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT count(*) FROM reorder_recommendations rr`+fmt.Sprintf(recListFilters, 4, 4, 4),
		middleware.BranchIDForQuery(ctx), f.VendorID, statusPtr(f.Status),
		middleware.GrantsSubForQuery(ctx)).Scan(&n)
	return n, err
}

func statusPtr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func scanRecommendation(scan func(dest ...any) error) (ReorderRecommendation, error) {
	var rec ReorderRecommendation
	var onHand, allocated, available, onOrder, backordered, velocity, lead, suggested string
	var reorderPoint, reorderQty *string
	var created time.Time
	err := scan(&rec.ID, &rec.RunID, &rec.ProductID, &rec.BranchID, &rec.VendorID,
		&onHand, &allocated, &available, &onOrder, &backordered,
		&velocity, &rec.LookbackDays, &lead, &rec.LeadTimeSource,
		&reorderPoint, &reorderQty, &suggested,
		&rec.VendorItemID, &rec.Unit, &rec.Status, &rec.PurchaseOrderLineID, &created,
		&rec.SKU, &rec.Description)
	if err != nil {
		return rec, err
	}
	q := func(s string) httpx.Quantity {
		v, err := httpx.ParseQuantity(s)
		if err != nil {
			return 0
		}
		return v
	}
	rec.OnHand, rec.Allocated, rec.Available = q(onHand), q(allocated), q(available)
	rec.OnOrder, rec.Backordered, rec.Velocity = q(onOrder), q(backordered), q(velocity)
	rec.LeadTimeDays = q(lead)
	if reorderPoint != nil {
		v := q(*reorderPoint)
		rec.ReorderPoint = &v
	}
	if reorderQty != nil {
		v := q(*reorderQty)
		rec.ReorderQuantity = &v
	}
	if f, err := strconv.ParseFloat(suggested, 64); err != nil {
		return rec, err
	} else {
		rec.SuggestedQuantity = f
	}
	rec.CreatedAt = httpx.TimestampOf(created)
	return rec, nil
}

// OpenRecommendations lists the recommendations CreateReorders turns into
// purchase lines, oldest first.
func (r *Repository) OpenRecommendations(ctx context.Context) ([]ReorderRecommendation, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT rr.id, rr.run_id, rr.product_id, rr.branch_id, rr.vendor_id,
		       rr.on_hand::text, rr.allocated::text, rr.available::text, rr.on_order::text, rr.backordered::text,
		       rr.velocity::text, rr.lookback_days, rr.lead_time_days::text, rr.lead_time_source,
		       rr.reorder_point::text, rr.reorder_quantity::text, rr.suggested_quantity::text,
		       rr.vendor_item_id, rr.unit, rr.status, rr.purchase_order_line_id, rr.created_at,
		       p.sku, p.description
		FROM reorder_recommendations rr
		JOIN products p ON p.id = rr.product_id
		WHERE rr.status = 'OPEN'
		ORDER BY rr.created_at, rr.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReorderRecommendation
	for rows.Next() {
		rec, err := scanRecommendation(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// MarkRecommendationOrdered records the purchase line a recommendation
// became and closes it.
func (r *Repository) MarkRecommendationOrdered(ctx context.Context, id, lineID uuid.UUID) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE reorder_recommendations SET status = 'ORDERED', purchase_order_line_id = $2 WHERE id = $1 AND status = 'OPEN'`,
		id, lineID)
	return err
}

// GetDraftPOByVendorBranch is CreateReorders' draft lookup: the vendor's
// open draft at the recommendation's branch.
func (r *Repository) GetDraftPOByVendorBranch(ctx context.Context, vendorID *uuid.UUID, branch uuid.UUID) (*PurchaseOrder, error) {
	po, err := scanPOHeader(r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT id, number, vendor_id, status, source, currency, revision, branch_id, created_at, updated_at, sent_at
		FROM purchase_orders
		WHERE vendor_id = $1 AND status = 'DRAFT' AND branch_id = $2
		ORDER BY created_at, id LIMIT 1`, vendorID, branch).Scan)
	if err != nil {
		return nil, nil
	}
	return po, nil
}

// SettingFloat reads a numeric system setting with a default.
func (r *Repository) SettingFloat(ctx context.Context, key string, fallback float64) (float64, error) {
	var value *string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT value FROM system_settings WHERE key = $1`, key).Scan(&value)
	if err != nil {
		return fallback, nil
	}
	if value == nil || *value == "" {
		return fallback, nil
	}
	var f float64
	if _, err := fmt.Sscanf(*value, "%g", &f); err != nil || f <= 0 {
		return fallback, nil
	}
	return f, nil
}

// MoveAverageUnderLock takes the product row FOR UPDATE, reads the current
// average and Q (the sum of the product's inventory quantities, BEFORE the
// receipt's stock write, so the received quantity is never counted twice),
// and computes the new average exactly, rounded once to scale 4 (ADR 0008
// section 3.5): avg' = (Q x avg + v) / (Q + q); avg' = v / q when Q <= 0 or
// avg is null.
func (r *Repository) MoveAverageUnderLock(ctx context.Context, productID uuid.UUID, received httpx.Quantity, value httpx.Cents) (float64, error) {
	var avgText *string
	var qText string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT p.average_unit_cost::text,
		       COALESCE((SELECT SUM(i.quantity)::text FROM inventory i WHERE i.product_id = p.id), '0')
		FROM products p WHERE p.id = $1 FOR UPDATE OF p`, productID).Scan(&avgText, &qText)
	if err != nil {
		return 0, err
	}
	avg4 := new(big.Rat)
	hasAvg := false
	if avgText != nil {
		if v, err := httpx.ParsePrice(*avgText); err == nil && v > 0 {
			avg4.SetInt64(int64(v))
			hasAvg = true
		}
	}
	q4 := new(big.Rat)
	if v, err := httpx.ParseQuantity(qText); err == nil {
		q4.SetInt64(int64(v))
	}
	received4 := new(big.Rat).SetInt64(int64(received))
	// The received value in dollars at scale 4: cents x 100.
	v4 := new(big.Rat).SetInt64(int64(value) * 100)

	zero := new(big.Rat)
	num := new(big.Rat).Mul(q4, avg4)
	num.Add(num, v4)
	den := new(big.Rat).Add(q4, received4)
	var next *big.Rat
	if !hasAvg || q4.Cmp(zero) <= 0 {
		next = new(big.Rat).Quo(v4, received4)
	} else {
		next = new(big.Rat).Quo(num, den)
	}
	// Round once, half away from zero, to scale 4.
	scaled := new(big.Rat).Mul(next, new(big.Rat).SetInt64(10000))
	units := roundRatHalfAway(scaled)
	f := new(big.Rat).SetFrac(units, big.NewInt(10000))
	out, _ := f.Float64()
	return out, nil
}

// roundRatHalfAway rounds a rational to the nearest integer, half away from
// zero, in exact integer arithmetic.
func roundRatHalfAway(r *big.Rat) *big.Int {
	neg := r.Sign() < 0
	abs := new(big.Rat).Abs(r)
	// floor(abs + 1/2) = (2 x num + den) / (2 x den), den > 0.
	num := new(big.Int).Set(abs.Num())
	den := new(big.Int).Set(abs.Denom())
	q := new(big.Int).Lsh(num, 1)
	q.Add(q, den)
	q.Div(q, new(big.Int).Lsh(den, 1))
	if neg {
		q.Neg(q)
	}
	return q
}
