// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package edi

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"log/slog"
)

// ErrNotX12 reports that an upload is not an X12 interchange at all.
//
// It is deliberately NOT the same answer as "this is X12 and it contained no
// catalog items". An 832 that carries only envelope segments parses to zero
// entries and that is an honest, successful "nothing to import". A CSV posted
// without ?format=csv, a PDF, or an empty body are files this parser cannot
// read at all, and reporting those as a successful import of nothing tells an
// operator the upload worked when the partner's catalog was left untouched.
var ErrNotX12 = errors.New("not an X12 document: no recognisable segment identifier")

// x12SegmentID matches an X12 segment identifier: two or three upper-case
// alphanumerics beginning with a letter (X12.6 §3.3). Every segment a real
// interchange can open with matches it — ISA, GS, ST, N1, LIN, PID, CTP, DTM,
// SE, GE, IEA — while a CSV header row, a line of prose or the first bytes of a
// PDF do not.
var x12SegmentID = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,2}$`)

// ValidateX12 reports whether data can be read as an X12 interchange at all,
// returning ErrNotX12 when it cannot.
//
// This is a recognition check, not a validation of the transaction set: it asks
// only whether the bytes are segmented the way X12 segments are, which is the
// question the parsers cannot answer for themselves. Parse832Catalog and
// Parse846Inquiry are deliberately lenient — they skip every segment they do
// not recognise, because a supplier's 832 routinely carries segments we have no
// use for — so they cannot tell an unfamiliar document from an unfamiliar
// segment. Callers that accept an upload from a human run this first.
func ValidateX12(data string) error {
	for _, seg := range strings.Split(data, "~") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		// The segment ID is everything before the first element separator; a
		// segment with no elements at all (a bare "SE") is still a segment.
		id, _, _ := strings.Cut(seg, "*")
		if x12SegmentID.MatchString(id) {
			return nil
		}
	}
	return ErrNotX12
}

// BuyingGroupService handles EDI 832 (Price/Sales Catalog) and 846 (Inventory Inquiry)
// document processing for buying group integrations.
type BuyingGroupService struct {
	logger  *slog.Logger
	catalog []SupplierCatalogEntry
}

// SupplierCatalogEntry represents a single item from a supplier's price catalog.
type SupplierCatalogEntry struct {
	VendorName    string    `json:"vendor_name"`
	SKU           string    `json:"sku"`
	VendorSKU     string    `json:"vendor_sku"`
	Description   string    `json:"description"`
	UnitPrice     float64   `json:"unit_price"`
	UOM           string    `json:"uom"`
	EffectiveDate time.Time `json:"effective_date"`
	ExpiryDate    time.Time `json:"expiry_date,omitempty"`
	MinOrderQty   float64   `json:"min_order_qty"`
	PackSize      int       `json:"pack_size"`
}

// PriceComparison compares current cost with supplier catalog pricing.
type PriceComparison struct {
	SKU           string  `json:"sku"`
	ProductName   string  `json:"product_name"`
	CurrentCost   float64 `json:"current_cost"`
	CatalogPrice  float64 `json:"catalog_price"`
	Savings       float64 `json:"savings"`
	SavingsPct    float64 `json:"savings_pct"`
	VendorName    string  `json:"vendor_name"`
	CatalogDate   string  `json:"catalog_date"`
	IsBetterPrice bool    `json:"is_better_price"`
}

// InventoryInquiryResult represents parsed EDI 846 inventory data.
type InventoryInquiryResult struct {
	VendorName   string  `json:"vendor_name"`
	VendorSKU    string  `json:"vendor_sku"`
	Description  string  `json:"description"`
	QtyAvailable float64 `json:"qty_available"`
	UOM          string  `json:"uom"`
	LeadTimeDays int     `json:"lead_time_days"`
	AsOfDate     string  `json:"as_of_date"`
}

// CatalogSyncResult summarizes a catalog import operation.
type CatalogSyncResult struct {
	VendorName    string `json:"vendor_name"`
	ItemsImported int    `json:"items_imported"`
	ItemsUpdated  int    `json:"items_updated"`
	ItemsSkipped  int    `json:"items_skipped"`
	Errors        int    `json:"errors"`
	SyncedAt      string `json:"synced_at"`
}

// NewBuyingGroupService creates a new buying group EDI service.
func NewBuyingGroupService(logger *slog.Logger) *BuyingGroupService {
	return &BuyingGroupService{
		logger:  logger,
		catalog: make([]SupplierCatalogEntry, 0),
	}
}

// Parse832Catalog parses an EDI 832 Price/Sales Catalog document.
// EDI 832 contains supplier pricing information in X12 segment format.
func (s *BuyingGroupService) Parse832Catalog(data string) ([]SupplierCatalogEntry, error) {
	segments := strings.Split(data, "~")
	var entries []SupplierCatalogEntry
	var currentEntry *SupplierCatalogEntry
	vendorName := ""

	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		elements := strings.Split(seg, "*")
		if len(elements) == 0 {
			continue
		}

		switch elements[0] {
		case "N1":
			// N1*SU*VendorName — Supplier name segment
			if len(elements) >= 3 && elements[1] == "SU" {
				vendorName = elements[2]
			}

		case "LIN":
			// LIN*1*VP*VENDOR-SKU*SK*OUR-SKU — Line item identification
			if currentEntry != nil {
				entries = append(entries, *currentEntry)
			}
			currentEntry = &SupplierCatalogEntry{
				VendorName:    vendorName,
				EffectiveDate: time.Now(),
				MinOrderQty:   1,
				PackSize:      1,
			}
			// LIN01 (elements[1]) is the line number; the identifier pairs
			// start at LIN02 (elements[2]) and repeat as (qualifier, value).
			for i := 2; i < len(elements)-1; i += 2 {
				qualifier := elements[i]
				value := elements[i+1]
				switch qualifier {
				case "VP":
					currentEntry.VendorSKU = value
				case "SK":
					currentEntry.SKU = value
				}
			}

		case "PID":
			// PID*F****Description — the free-text description is PID05,
			// which is elements[5] (PID02..PID04 are the empty placeholders).
			if currentEntry != nil && len(elements) >= 6 {
				currentEntry.Description = elements[5]
			}

		case "CTP":
			// CTP*RS*RES*12.50*1*EA — Pricing information
			if currentEntry != nil && len(elements) >= 4 {
				price, err := strconv.ParseFloat(elements[3], 64)
				if err == nil {
					currentEntry.UnitPrice = price
				}
				if len(elements) >= 6 {
					currentEntry.UOM = elements[5]
				}
			}

		case "DTM":
			// DTM*196*20260101 — Effective date
			if currentEntry != nil && len(elements) >= 3 {
				if t, err := time.Parse("20060102", elements[2]); err == nil {
					switch elements[1] {
					case "196":
						currentEntry.EffectiveDate = t
					case "197":
						currentEntry.ExpiryDate = t
					}
				}
			}
		}
	}

	// Don't forget the last entry
	if currentEntry != nil {
		entries = append(entries, *currentEntry)
	}

	s.logger.Info("Parsed EDI 832 catalog", "vendor", vendorName, "item_count", len(entries))
	return entries, nil
}

// ParseCSVCatalog parses a CSV supplier price list.
// Expected columns: vendor_sku, sku, description, unit_price, uom, min_order_qty
func (s *BuyingGroupService) ParseCSVCatalog(data string, vendorName string) ([]SupplierCatalogEntry, error) {
	reader := csv.NewReader(strings.NewReader(data))
	var entries []SupplierCatalogEntry

	// Read header
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV header: %w", err)
	}

	// Build column index map
	colIdx := make(map[string]int)
	for i, col := range header {
		colIdx[strings.ToLower(strings.TrimSpace(col))] = i
	}

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue // Skip malformed rows
		}

		entry := SupplierCatalogEntry{
			VendorName:    vendorName,
			EffectiveDate: time.Now(),
			MinOrderQty:   1,
			PackSize:      1,
		}

		if idx, ok := colIdx["vendor_sku"]; ok && idx < len(record) {
			entry.VendorSKU = strings.TrimSpace(record[idx])
		}
		if idx, ok := colIdx["sku"]; ok && idx < len(record) {
			entry.SKU = strings.TrimSpace(record[idx])
		}
		if idx, ok := colIdx["description"]; ok && idx < len(record) {
			entry.Description = strings.TrimSpace(record[idx])
		}
		if idx, ok := colIdx["unit_price"]; ok && idx < len(record) {
			if price, err := strconv.ParseFloat(strings.TrimSpace(record[idx]), 64); err == nil {
				entry.UnitPrice = price
			}
		}
		if idx, ok := colIdx["uom"]; ok && idx < len(record) {
			entry.UOM = strings.TrimSpace(record[idx])
		}
		if idx, ok := colIdx["min_order_qty"]; ok && idx < len(record) {
			if qty, err := strconv.ParseFloat(strings.TrimSpace(record[idx]), 64); err == nil {
				entry.MinOrderQty = qty
			}
		}

		entries = append(entries, entry)
	}

	s.logger.Info("Parsed CSV catalog", "vendor", vendorName, "item_count", len(entries))
	return entries, nil
}

// Parse846Inquiry parses an EDI 846 Inventory Inquiry/Advice document.
func (s *BuyingGroupService) Parse846Inquiry(data string) ([]InventoryInquiryResult, error) {
	segments := strings.Split(data, "~")
	var results []InventoryInquiryResult
	var current *InventoryInquiryResult
	vendorName := ""

	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		elements := strings.Split(seg, "*")
		if len(elements) == 0 {
			continue
		}

		switch elements[0] {
		case "N1":
			if len(elements) >= 3 && elements[1] == "SU" {
				vendorName = elements[2]
			}

		case "LIN":
			if current != nil {
				results = append(results, *current)
			}
			current = &InventoryInquiryResult{
				VendorName: vendorName,
				AsOfDate:   time.Now().Format("2006-01-02"),
			}
			// LIN01 is the line number; (qualifier, value) pairs start at LIN02.
			for i := 2; i < len(elements)-1; i += 2 {
				if elements[i] == "VP" {
					current.VendorSKU = elements[i+1]
				}
			}

		case "QTY":
			// QTY*33*500*EA — Quantity available
			if current != nil && len(elements) >= 3 {
				if qty, err := strconv.ParseFloat(elements[2], 64); err == nil {
					current.QtyAvailable = qty
				}
				if len(elements) >= 4 {
					current.UOM = elements[3]
				}
			}

		case "PID":
			// PID05 (elements[5]) carries the free-text description.
			if current != nil && len(elements) >= 6 {
				current.Description = elements[5]
			}

		case "LDT":
			// LDT*AF*7*DA — Lead time
			if current != nil && len(elements) >= 3 {
				if days, err := strconv.Atoi(elements[2]); err == nil {
					current.LeadTimeDays = days
				}
			}
		}
	}

	if current != nil {
		results = append(results, *current)
	}

	s.logger.Info("Parsed EDI 846 inquiry", "vendor", vendorName, "item_count", len(results))
	return results, nil
}

// catalogKey is the identity of a catalog entry for deduplication: a vendor
// plus whichever identifier actually identifies the product. The vendor's own
// SKU wins when present; otherwise our internal SKU is used, so two distinct
// products from the same vendor that carry no vendor SKU do not collapse onto
// each other. The qualifier prefix stops a vendor SKU colliding with an
// internal SKU that happens to share its text.
func catalogKey(e SupplierCatalogEntry) string {
	if e.VendorSKU != "" {
		return e.VendorName + "\x00VP\x00" + e.VendorSKU
	}
	return e.VendorName + "\x00SK\x00" + e.SKU
}

// ImportCatalog stores parsed catalog entries in the in-memory catalog.
// In production, this would persist to a database table.
func (s *BuyingGroupService) ImportCatalog(entries []SupplierCatalogEntry) *CatalogSyncResult {
	result := &CatalogSyncResult{
		SyncedAt: time.Now().Format(time.RFC3339),
	}

	if len(entries) > 0 {
		result.VendorName = entries[0].VendorName
	}

	for _, entry := range entries {
		if entry.SKU == "" && entry.VendorSKU == "" {
			result.ItemsSkipped++
			continue
		}

		// Check for existing entry and update or add
		found := false
		key := catalogKey(entry)
		for i, existing := range s.catalog {
			if catalogKey(existing) == key {
				s.catalog[i] = entry
				result.ItemsUpdated++
				found = true
				break
			}
		}
		if !found {
			s.catalog = append(s.catalog, entry)
			result.ItemsImported++
		}
	}

	s.logger.Info("Catalog imported",
		"vendor", result.VendorName,
		"imported", result.ItemsImported,
		"updated", result.ItemsUpdated,
		"skipped", result.ItemsSkipped,
	)

	return result
}

// ComparePrices compares current product costs against the supplier catalog.
func (s *BuyingGroupService) ComparePrices(sku string, currentCost float64, productName string) []PriceComparison {
	var comparisons []PriceComparison

	for _, entry := range s.catalog {
		if entry.SKU != sku && entry.VendorSKU != sku {
			continue
		}

		savings := currentCost - entry.UnitPrice
		savingsPct := 0.0
		if currentCost > 0 {
			savingsPct = (savings / currentCost) * 100
		}

		comparisons = append(comparisons, PriceComparison{
			SKU:           sku,
			ProductName:   productName,
			CurrentCost:   currentCost,
			CatalogPrice:  entry.UnitPrice,
			Savings:       savings,
			SavingsPct:    savingsPct,
			VendorName:    entry.VendorName,
			CatalogDate:   entry.EffectiveDate.Format("2006-01-02"),
			IsBetterPrice: savings > 0,
		})
	}

	return comparisons
}

// GetCatalog returns all loaded catalog entries.
func (s *BuyingGroupService) GetCatalog() []SupplierCatalogEntry {
	return s.catalog
}

// SyncSupplierPricing is a placeholder for scheduled supplier pricing sync.
// In production, this would fetch from configured FTP/API endpoints.
func (s *BuyingGroupService) SyncSupplierPricing() error {
	s.logger.Info("Supplier pricing sync triggered (stub - configure FTP/API endpoints for production)")
	return nil
}
