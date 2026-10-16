// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/google/uuid"
)

// RecommendationConfig holds tunable parameters for the recommendation engine.
type RecommendationConfig struct {
	// ZScore for safety stock calculation (1.65 = 95% service level)
	ZScore float64
	// DefaultLeadTimeDays used when vendor lead time is unknown
	DefaultLeadTimeDays float64
	// MinOrderQty minimum quantity per PO line
	MinOrderQty float64
	// LookbackDays number of days to analyze sales velocity
	LookbackDays int
}

// DefaultRecommendationConfig returns production-ready defaults.
func DefaultRecommendationConfig() RecommendationConfig {
	return RecommendationConfig{
		ZScore:              1.65,
		DefaultLeadTimeDays: 7,
		MinOrderQty:         1,
		LookbackDays:        90,
	}
}

// UrgencyLevel indicates how urgently a product needs reordering.
type UrgencyLevel string

const (
	UrgencyCritical UrgencyLevel = "CRITICAL" // Below reorder point, stock may run out before delivery
	UrgencyHigh     UrgencyLevel = "HIGH"     // At or near reorder point
	UrgencyMedium   UrgencyLevel = "MEDIUM"   // Will hit reorder point within lead time
	UrgencyLow      UrgencyLevel = "LOW"      // Approaching reorder point
)

// PurchaseRecommendation represents a suggested purchase order for a product.
type PurchaseRecommendation struct {
	// A recommendation is one product at one yard (item 135 b): the stock is
	// that yard's, the sales are that yard's, and the suggestion is for that
	// yard. A company wide figure never decides one.
	BranchID    uuid.UUID `json:"branch_id"`
	BranchName  string    `json:"branch_name"`
	ProductID   uuid.UUID `json:"product_id"`
	ProductSKU  string    `json:"product_sku"`
	ProductName string    `json:"product_name"`
	// VendorID is the product's vendor, by id, so a new purchase order from
	// this row is created for that vendor; absent when the product has none.
	VendorID      *uuid.UUID   `json:"vendor_id,omitempty"`
	VendorName    string       `json:"vendor_name,omitempty"`
	CurrentStock  float64      `json:"current_stock"`
	AvgDailySales float64      `json:"avg_daily_sales"`
	StdDevSales   float64      `json:"std_dev_sales"`
	LeadTimeDays  float64      `json:"lead_time_days"`
	ReorderPoint  float64      `json:"reorder_point"`
	SafetyStock   float64      `json:"safety_stock"`
	SuggestedQty  float64      `json:"suggested_qty"`
	EstimatedCost float64      `json:"estimated_cost"`
	Urgency       UrgencyLevel `json:"urgency"`
	DaysUntilOut  float64      `json:"days_until_out"`
	CatalogPrice  *float64     `json:"catalog_price,omitempty"`
}

// RecommendationSummary provides aggregate stats for the dashboard.
type RecommendationSummary struct {
	TotalItems    int                      `json:"total_items"`
	CriticalCount int                      `json:"critical_count"`
	HighCount     int                      `json:"high_count"`
	MediumCount   int                      `json:"medium_count"`
	LowCount      int                      `json:"low_count"`
	TotalEstCost  float64                  `json:"total_estimated_cost"`
	Items         []PurchaseRecommendation `json:"items"`
}

// RecommendationService generates purchasing recommendations based on
// sales velocity, current stock levels, and vendor lead times.
type RecommendationService struct {
	repo         *Repository
	inventorySvc *inventory.Service
	productSvc   *product.Service
	vendorSvc    *vendor.Service
	velocityRepo salesVelocityLister
	yardVelocity yardVelocityLister
	config       RecommendationConfig
}

// NewRecommendationService creates a new recommendation engine.
func NewRecommendationService(
	repo *Repository,
	inventorySvc *inventory.Service,
	productSvc *product.Service,
	vendorSvc *vendor.Service,
) *RecommendationService {
	return &RecommendationService{
		repo:         repo,
		inventorySvc: inventorySvc,
		productSvc:   productSvc,
		vendorSvc:    vendorSvc,
		config:       DefaultRecommendationConfig(),
	}
}

// WithConfig overrides the default recommendation configuration.
func (rs *RecommendationService) WithConfig(cfg RecommendationConfig) *RecommendationService {
	rs.config = cfg
	return rs
}

// WithVelocityRepo wires the real sales-velocity reader; when set,
// GenerateRecommendations pulls demand from order_lines instead of using the
// synthetic reorder-point proxy. Falls back to the synthetic estimate per-SKU
// when a product has zero sales history in the lookback window.
func (rs *RecommendationService) WithVelocityRepo(v salesVelocityLister) *RecommendationService {
	rs.velocityRepo = v
	// The real repository also answers per yard; a fake that does not keeps
	// the product wide demand for every yard.
	if yv, ok := v.(yardVelocityLister); ok {
		rs.yardVelocity = yv
	}
	return rs
}

// yardVelocityLister is the optional per yard demand read.
type yardVelocityLister interface {
	ListSalesVelocityByYard(ctx context.Context, lookbackDays int) ([]YardVelocity, error)
}

// CalculateReorderPoint computes: (Avg Daily Sales x Lead Time Days) + Safety Stock
func CalculateReorderPoint(avgDailySales, leadTimeDays, safetyStock float64) float64 {
	return (avgDailySales * leadTimeDays) + safetyStock
}

// CalculateSafetyStock computes: Z-score x StdDev(Daily Sales) x sqrt(Lead Time Days)
func CalculateSafetyStock(zScore, stdDevDailySales, leadTimeDays float64) float64 {
	if leadTimeDays <= 0 {
		return 0
	}
	return zScore * stdDevDailySales * math.Sqrt(leadTimeDays)
}

// CalculateEOQ computes the Economic Order Quantity.
// EOQ = sqrt((2 x Annual Demand x Order Cost) / Holding Cost per unit)
// Uses simplified defaults: order cost = $50, holding cost = 20% of unit cost.
func CalculateEOQ(annualDemand, unitCost float64) float64 {
	if annualDemand <= 0 || unitCost <= 0 {
		return 0
	}
	orderCost := 50.0
	holdingCost := unitCost * 0.20
	if holdingCost <= 0 {
		holdingCost = 1.0
	}
	eoq := math.Sqrt((2 * annualDemand * orderCost) / holdingCost)
	return math.Ceil(eoq)
}

// ClassifyUrgency determines the urgency level based on stock vs reorder point.
func ClassifyUrgency(currentStock, reorderPoint, avgDailySales, leadTimeDays float64) UrgencyLevel {
	if avgDailySales <= 0 {
		return UrgencyLow
	}

	daysOfStock := currentStock / avgDailySales

	if currentStock <= 0 || daysOfStock < leadTimeDays*0.5 {
		return UrgencyCritical
	}
	if currentStock <= reorderPoint {
		return UrgencyHigh
	}
	if currentStock <= reorderPoint*1.5 {
		return UrgencyMedium
	}
	return UrgencyLow
}

// DaysUntilStockout estimates days until stock runs out at current velocity.
func DaysUntilStockout(currentStock, avgDailySales float64) float64 {
	if avgDailySales <= 0 {
		return 999
	}
	days := currentStock / avgDailySales
	if days < 0 {
		return 0
	}
	return math.Round(days*10) / 10
}

// GenerateRecommendations analyzes all products and returns purchase recommendations
// for items that are at or approaching their reorder point.
func (rs *RecommendationService) GenerateRecommendations(ctx context.Context) (*RecommendationSummary, error) {
	// 1. Get all products
	products, err := rs.productSvc.ListProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load products: %w", err)
	}

	// 2. Build vendor lookup by UUID (primary) and name (legacy fallback) for lead times
	vendorByID := make(map[uuid.UUID]*vendor.Vendor)
	vendorByName := make(map[string]*vendor.Vendor)
	vendors, err := rs.vendorSvc.ListVendors(ctx)
	if err == nil {
		for i := range vendors {
			vendorByID[vendors[i].ID] = &vendors[i]
			vendorByName[vendors[i].Name] = &vendors[i]
		}
	}

	// 2.5. Pull real sales velocity from order_lines once for the whole
	// catalog. Indexed by product UUID for an O(1) per-product lookup below.
	// If the velocity repo isn't wired (older test harness), the map is
	// empty and every product falls through to the synthetic estimate.
	velByProduct := make(map[uuid.UUID]float64)
	if rs.velocityRepo != nil {
		velocity, vErr := rs.velocityRepo.ListSalesVelocity(ctx, rs.config.LookbackDays)
		if vErr == nil {
			for _, v := range velocity {
				velByProduct[v.ProductID] = v.UnitsSold
			}
		}
	}

	// Stock per yard: one read for the caller's reach (the context yard, the
	// granted yards, or every yard). A product held at no yard in reach is no
	// yard's signal and carries no recommendation.
	yardStock, err := rs.repo.ListYardStock(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load yard stock: %w", err)
	}
	stockByProduct := make(map[uuid.UUID][]YardStock)
	for _, ys := range yardStock {
		stockByProduct[ys.ProductID] = append(stockByProduct[ys.ProductID], ys)
	}
	// Demand per yard when the reader answers it; otherwise the product wide
	// figure stands in for every yard.
	velByYard := make(map[uuid.UUID]map[uuid.UUID]float64)
	if rs.yardVelocity != nil {
		if yv, vErr := rs.yardVelocity.ListSalesVelocityByYard(ctx, rs.config.LookbackDays); vErr == nil {
			for _, v := range yv {
				if velByYard[v.BranchID] == nil {
					velByYard[v.BranchID] = map[uuid.UUID]float64{}
				}
				velByYard[v.BranchID][v.ProductID] = v.UnitsSold
			}
		}
	}

	var recommendations []PurchaseRecommendation
	totalEstCost := 0.0

	for _, p := range products {
		for _, ys := range stockByProduct[p.ID] {
			currentStock := ys.Available
			vel := velByProduct
			if rs.yardVelocity != nil {
				vel = velByYard[ys.BranchID]
			}

			// 4. Compute sales velocity. Prefer real order_lines velocity; fall
			// back to the synthetic reorder-point proxy when a new SKU has no
			// sales history yet so brand-new products still surface in the list.
			avgDailySales := rs.dailySalesFor(p, vel)
			stdDevSales := avgDailySales * 0.3 // 30% coefficient of variation

			if avgDailySales <= 0 {
				continue // No sales velocity, skip
			}

			// 5. Get lead time from vendor or use default. Prefer canonical
			// vendor_id; fall back to display-name lookup for legacy rows.
			leadTime := rs.config.DefaultLeadTimeDays
			vendorName := ""
			var vendorID *uuid.UUID
			var matchedVendor *vendor.Vendor
			if p.VendorID != nil {
				if v, ok := vendorByID[*p.VendorID]; ok {
					matchedVendor = v
				}
			}
			if matchedVendor == nil && p.Vendor != nil && *p.Vendor != "" {
				if v, ok := vendorByName[*p.Vendor]; ok {
					matchedVendor = v
				}
			}
			if matchedVendor != nil {
				vendorName = matchedVendor.Name
				id := matchedVendor.ID
				vendorID = &id
				if matchedVendor.AverageLeadTimeDays > 0 {
					leadTime = matchedVendor.AverageLeadTimeDays
				}
			} else if p.Vendor != nil {
				vendorName = *p.Vendor
			}

			// 6. Calculate reorder metrics
			safetyStock := CalculateSafetyStock(rs.config.ZScore, stdDevSales, leadTime)
			reorderPoint := CalculateReorderPoint(avgDailySales, leadTime, safetyStock)

			// 7. Only recommend if stock is at or below 2x reorder point
			if currentStock > reorderPoint*2 {
				continue
			}

			// 8. Calculate suggested quantity
			annualDemand := avgDailySales * 365
			vendorCostEstimate := p.BasePrice * 0.6 // Estimate vendor cost at 60% of base price
			eoq := CalculateEOQ(annualDemand, vendorCostEstimate)
			suggestedQty := math.Max(eoq, rs.config.MinOrderQty)

			// Ensure we order enough to get back above reorder point + buffer
			minNeeded := reorderPoint*1.5 - currentStock
			if minNeeded > suggestedQty {
				suggestedQty = math.Ceil(minNeeded)
			}

			estimatedCost := suggestedQty * vendorCostEstimate

			// 9. Classify urgency
			urgency := ClassifyUrgency(currentStock, reorderPoint, avgDailySales, leadTime)
			daysOut := DaysUntilStockout(currentStock, avgDailySales)

			rec := PurchaseRecommendation{
				BranchID:      ys.BranchID,
				BranchName:    ys.YardName,
				ProductID:     p.ID,
				ProductSKU:    p.SKU,
				ProductName:   p.Description,
				VendorID:      vendorID,
				VendorName:    vendorName,
				CurrentStock:  math.Round(currentStock*100) / 100,
				AvgDailySales: math.Round(avgDailySales*100) / 100,
				StdDevSales:   math.Round(stdDevSales*100) / 100,
				LeadTimeDays:  leadTime,
				ReorderPoint:  math.Round(reorderPoint*100) / 100,
				SafetyStock:   math.Round(safetyStock*100) / 100,
				SuggestedQty:  suggestedQty,
				EstimatedCost: math.Round(estimatedCost*100) / 100,
				Urgency:       urgency,
				DaysUntilOut:  daysOut,
			}

			recommendations = append(recommendations, rec)
			totalEstCost += estimatedCost
		}
	}

	// Sort by urgency (critical first) then by days until stockout
	sort.Slice(recommendations, func(i, j int) bool {
		ui := urgencyRank(recommendations[i].Urgency)
		uj := urgencyRank(recommendations[j].Urgency)
		if ui != uj {
			return ui < uj
		}
		if recommendations[i].DaysUntilOut != recommendations[j].DaysUntilOut {
			return recommendations[i].DaysUntilOut < recommendations[j].DaysUntilOut
		}
		// A stable order for ties: one product's yards sit together, by name.
		if recommendations[i].ProductSKU != recommendations[j].ProductSKU {
			return recommendations[i].ProductSKU < recommendations[j].ProductSKU
		}
		if recommendations[i].BranchName != recommendations[j].BranchName {
			return recommendations[i].BranchName < recommendations[j].BranchName
		}
		return recommendations[i].BranchID.String() < recommendations[j].BranchID.String()
	})

	// Build summary
	summary := &RecommendationSummary{
		TotalItems:   len(recommendations),
		TotalEstCost: math.Round(totalEstCost*100) / 100,
		Items:        recommendations,
	}

	for _, r := range recommendations {
		switch r.Urgency {
		case UrgencyCritical:
			summary.CriticalCount++
		case UrgencyHigh:
			summary.HighCount++
		case UrgencyMedium:
			summary.MediumCount++
		case UrgencyLow:
			summary.LowCount++
		}
	}

	return summary, nil
}

// dailySalesFor returns avg daily sales for a product. Prefers the real
// velocity map (units sold / lookback days); falls back to the synthetic
// estimator for products with no sales history. The synthetic fallback is
// preserved so a brand-new SKU still gets a usable estimate before the
// first sale, and so the recommendation engine works in test harnesses
// that don't wire the velocity repo.
func (rs *RecommendationService) dailySalesFor(p product.Product, velByProduct map[uuid.UUID]float64) float64 {
	if units, ok := velByProduct[p.ID]; ok && units > 0 {
		lookback := float64(rs.config.LookbackDays)
		if lookback <= 0 {
			lookback = 90
		}
		return units / lookback
	}
	return rs.estimateDailySales(p)
}

// estimateDailySales provides a deterministic synthetic sales velocity for
// products with no sales history in the lookback window. Used by
// dailySalesFor as a fallback so new SKUs don't drop out of the
// recommendation list before they've had their first sale.
func (rs *RecommendationService) estimateDailySales(p product.Product) float64 {
	// Use reorder point as a proxy for sales velocity
	if p.ReorderPoint > 0 {
		// Approximate: daily sales ~ reorder_point / (lead_time + safety_buffer)
		return p.ReorderPoint / (rs.config.DefaultLeadTimeDays + 3)
	}
	// Fallback: derive from base price (cheaper items sell more)
	if p.BasePrice > 0 {
		return 100.0 / p.BasePrice // Inverse relationship as proxy
	}
	return 0
}

func urgencyRank(u UrgencyLevel) int {
	switch u {
	case UrgencyCritical:
		return 0
	case UrgencyHigh:
		return 1
	case UrgencyMedium:
		return 2
	case UrgencyLow:
		return 3
	default:
		return 4
	}
}
