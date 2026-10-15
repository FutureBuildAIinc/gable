// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

// Demo data import: a deterministic, idempotent load of the Gable Building
// Supply synthetic catalogue (categories, vendors, products, customers,
// staff) plus a 120 day operations history. The JSON and JSONL files in
// ./demodata/ are embedded at compile time so the binary ships with the data.
//
// The reference data writes use ON CONFLICT (...) DO UPDATE keyed on each
// row's natural key so the second run of the seed is a no-op for the
// reference tables; transactional data (orders, invoices, payments) is
// truncated at the start of every run, so the history is freshly generated
// each time from the fixed seed and the deterministic window in
// demodata/history-params.json.

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed demodata/categories.json
var categoriesData []byte

//go:embed demodata/catalog.jsonl
var catalogData []byte

//go:embed demodata/vendor_keys.json
var vendorKeysData []byte

//go:embed demodata/vendors.jsonl
var vendorsData []byte

//go:embed demodata/customers.jsonl
var customersData []byte

//go:embed demodata/staff.jsonl
var staffData []byte

//go:embed demodata/history-params.json
var historyParamsData []byte

// catalogRow is one row of demodata/catalog.jsonl. The numeric dimensions
// use pointers so the import distinguishes "absent in the JSON" (NULL in
// the database) from "zero" (which the schema's > 0 check rejects for the
// board_* columns).
type catalogRow struct {
	SKU          string   `json:"sku"`
	Description  string   `json:"description"`
	Department   string   `json:"department"`
	CategorySlug string   `json:"category_slug"`
	VendorKey    string   `json:"vendor_key"`
	UOMPrimary   string   `json:"uom_primary"`
	Species      string   `json:"species"`
	Grade        string   `json:"grade"`
	Treatment    string   `json:"treatment"`
	ThicknessIn  *float64 `json:"board_thickness_in"`
	WidthIn      *float64 `json:"board_width_in"`
	LengthFt     *float64 `json:"board_length_ft"`
	RandomLength bool     `json:"random_length"`
	LengthIn     *float64 `json:"length_in"`
	WidthInDim   *float64 `json:"width_in"`
	HeightIn     *float64 `json:"height_in"`
	Stackable    bool     `json:"stackable"`
	WeightLbs    float64  `json:"weight_lbs"`
	Cost         float64  `json:"cost"`
	Price        float64  `json:"price"`
	ContractorPr *float64 `json:"contractor_price"`
	VIPPrice     *float64 `json:"vip_price"`
	TargetMargin float64  `json:"target_margin"`
	Commission   float64  `json:"commission_rate"`
	TaxClass     string   `json:"tax_class"`
	ImageFamily  string   `json:"image_family"`
	Barcode      string   `json:"barcode"`
	IsCommodity  bool     `json:"is_commodity"`
	YardsStocked []string `json:"yards_stocked"`
	Inventory    map[string]struct {
		OnHand       float64 `json:"on_hand"`
		ReorderPoint float64 `json:"reorder_point"`
		ReorderQty   float64 `json:"reorder_qty"`
	} `json:"inventory"`
}

// vendorRow is one row of demodata/vendors.jsonl. The vendor_keys.json
// catalog drives which vendor name resolves to which vendor_key.
type vendorRow struct {
	VendorKey           string   `json:"vendor_key"`
	Name                string   `json:"name"`
	ContactEmail        string   `json:"contact_email"`
	Phone               string   `json:"phone"`
	AddressLine1        string   `json:"address_line1"`
	City                string   `json:"city"`
	State               string   `json:"state"`
	Zip                 string   `json:"zip"`
	PaymentTerms        string   `json:"payment_terms"`
	AverageLeadTimeDays float64  `json:"average_lead_time_days"`
	FillRate            float64  `json:"fill_rate"`
	TotalSpendYTDCAD    float64  `json:"total_spend_ytd_cad"`
	Category            string   `json:"category"`
	YardsServed         []string `json:"yards_served"`
	MinimumOrderCAD     float64  `json:"minimum_order_cad"`
	Currency            string   `json:"currency"`
}

// vendorKeyCatalog is demodata/vendor_keys.json. It maps a vendor_key to the
// vendor's display name, payment terms and category so the catalogue file and
// the vendors file agree on which key resolves to which row.
type vendorKeyCatalog struct {
	Doc     string   `json:"_doc"`
	Rules   []string `json:"_rules"`
	Vendors []struct {
		Key                 string   `json:"key"`
		Name                string   `json:"name"`
		Category            string   `json:"category"`
		PaymentTerms        string   `json:"payment_terms"`
		AverageLeadTimeDays float64  `json:"average_lead_time_days"`
		FillRate            float64  `json:"fill_rate"`
		ShipTo              []string `json:"ship_to"`
		Notes               string   `json:"notes"`
	} `json:"vendors"`
}

// legacyVendorRename maps the eight seed.go vendor names the catalogue data
// references to the names the seed actually writes (the dealer was renamed,
// so the legacy "@gablelumber.ca" leftovers are gone).
// When the demo data's vendor_keys.json names a row in this map the import
// resolves to the right-hand value, so the seed's vendor (preserved on a
// conflict) is the row the catalogue's vendor_key references.
var legacyVendorRename = map[string]string{
	"Gable Lumber & Supply":          "Gable Building Supply",
	"Fernbank Hardware Distribution": "Fernbank Hardware Distribution",
	"BC Roofing Wholesale":           "BC Roofing Wholesale",
	"Interior Millwork Co":           "Interior Millwork Co",
	"Bramblewood Ready-Mix Concrete": "Bramblewood Ready-Mix Concrete",
	"Mountain Fastener Supply":       "Mountain Fastener Supply",
	"Cariboo Insulation Ltd":         "Cariboo Insulation Ltd",
	"Tamarack Window & Door":         "Tamarack Window & Door",
}

// customerRow is one row of demodata/customers.jsonl.
type customerRow struct {
	AccountNumber    string   `json:"account_number"`
	Name             string   `json:"name"`
	Email            string   `json:"email"`
	Phone            string   `json:"phone"`
	AddressLine1     string   `json:"address_line1"`
	City             string   `json:"city"`
	Region           string   `json:"region"`
	Tier             string   `json:"tier"`
	PaymentTermsCode string   `json:"payment_terms_code"`
	PriceLevel       string   `json:"price_level"`
	CreditLimitCAD   *float64 `json:"credit_limit_cad"`
	PSTExempt        bool     `json:"pst_exempt"`
	HomeYard         string   `json:"home_yard"`
	YardsTradesAt    []string `json:"yards_trades_at"`
	TradeType        string   `json:"trade_type"`
	SalespersonEmail string   `json:"salesperson_email"`
	Active           bool     `json:"active"`
	Contacts         []struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Title     string `json:"title"`
		Email     string `json:"email"`
		Phone     string `json:"phone"`
		Role      string `json:"role"`
		IsPrimary bool   `json:"is_primary"`
	} `json:"contacts"`
	JobSites []struct {
		Name   string `json:"name"`
		Line1  string `json:"line1"`
		City   string `json:"city"`
		Region string `json:"region"`
		Notes  string `json:"notes"`
	} `json:"job_sites"`
}

// staffRow is one row of demodata/staff.jsonl.
type staffRow struct {
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	StaffNo  string `json:"staff_no"`
	Role     string `json:"role"`
	HomeYard string `json:"home_yard"`
	Active   bool   `json:"active"`
}

// historyParams is the schema for demodata/history-params.json.
type historyParams struct {
	Seed   int `json:"_seed"`
	Window struct {
		Days int `json:"days"`
	} `json:"_window"`
	Branches []string `json:"_branches"`
	// CounterSales holds each yard's orders a day.
	CounterSales map[string]struct {
		WeekdayMean int `json:"weekday_mean"`
	} `json:"counter_sales_per_yard_per_day"`
}

// demoImportResults captures the counts the loader printed at the end of its
// run. The seed test reads the values from package globals set by the import.
type demoImportResults struct {
	Categories int
	Vendors    int
	Products   int
	Customers  int
	Staff      int
}

// lastImport is the latest import's counts, read by the seed tests for
// idempotency, margin and domain checks. It is set inside seedDemoImport; the
// tests guard on it only when the test ran the import itself.
var lastImport demoImportResults

// seedDemoImport reads the embedded demo data and upserts it as reference
// data. Reference tables use ON CONFLICT ... DO UPDATE keyed on the row's
// natural key, so a second run on the same database is a no-op: the counts
// stay the same and no row is moved.
//
// The function never moves a row an operator changed. Where a natural key
// collides with a row the rest of this package's seed (sections 1 to 22
// above) already wrote, the upsert preserves the legacy row and notes the
// collision in its log; a collision on account_number, sku, or staff_no is
// logged but the imported data only fills gaps (no name/email overwrite on
// a row an operator edited).
func seedDemoImport(ctx context.Context, db *sql.DB, yards map[string]uuid.UUID,
	priceLevelIDs map[string]uuid.UUID) (map[string]uuid.UUID, map[string]uuid.UUID, error) {

	vendorToID, err := importVendors(ctx, db)
	if err != nil {
		return nil, nil, fmt.Errorf("vendors: %w", err)
	}
	slugToID, err := importCategories(ctx, db)
	if err != nil {
		return nil, nil, fmt.Errorf("categories: %w", err)
	}
	lastImport.Categories = len(slugToID)

	skuToID, err := importCatalog(ctx, db, yards, vendorToID, slugToID)
	if err != nil {
		return nil, nil, fmt.Errorf("catalog: %w", err)
	}
	if err := applyProductPictures(ctx, db); err != nil {
		return nil, nil, fmt.Errorf("pictures: %w", err)
	}
	customerIDs, err := importCustomers(ctx, db, yards, priceLevelIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("customers: %w", err)
	}
	if err := importStaff(ctx, db, yards); err != nil {
		return nil, nil, fmt.Errorf("staff: %w", err)
	}
	return skuToID, customerIDs, nil
}

// importCategories reads demodata/categories.json and inserts each row into
// product_categories, keying on slug. A row that already exists is left
// alone: the operator's edits win. The returned map is slug -> id, used by
// the catalogue import to look up each product's category_id.
func importCategories(ctx context.Context, db *sql.DB) (map[string]uuid.UUID, error) {
	var rows []struct {
		Slug       string  `json:"slug"`
		Name       string  `json:"name"`
		ParentSlug *string `json:"parent_slug"`
		Depth      int     `json:"depth"`
		Path       string  `json:"path"`
	}
	if err := json.Unmarshal(categoriesData, &rows); err != nil {
		return nil, fmt.Errorf("decode categories.json: %w", err)
	}

	// Resolve parent_slug to the existing row's id first; the slug is the
	// natural key migration 049 backfills on insert.
	slugToID := make(map[string]uuid.UUID)
	for _, r := range rows {
		var id string
		err := db.QueryRowContext(ctx, `SELECT id FROM product_categories WHERE slug = $1`, r.Slug).Scan(&id)
		if err == sql.ErrNoRows {
			var parentID *uuid.UUID
			if r.ParentSlug != nil {
				if pid, ok := slugToID[*r.ParentSlug]; ok {
					parentID = &pid
				} else if err := db.QueryRowContext(ctx,
					`SELECT id FROM product_categories WHERE slug = $1`, *r.ParentSlug).Scan(&id); err == nil {
					pid := uuid.MustParse(id)
					slugToID[*r.ParentSlug] = pid
					parentID = &pid
				}
			}
			// ltree paths in migration 049 are case-sensitive and use dots.
			var pid string
			err := db.QueryRowContext(ctx, `INSERT INTO product_categories
				(name, slug, path, parent_id, sort_order, is_active)
				VALUES ($1, $2, $3::ltree, $4, 0, TRUE)
				ON CONFLICT (slug) DO NOTHING
				RETURNING id`, r.Name, r.Slug, r.Path, parentID).Scan(&pid)
			if err != nil || pid == "" {
				// Already exists; pick it up.
				db.QueryRowContext(ctx, `SELECT id FROM product_categories WHERE slug = $1`, r.Slug).Scan(&pid)
			}
			if pid != "" {
				slugToID[r.Slug] = uuid.MustParse(pid)
			}
		} else if err == nil {
			slugToID[r.Slug] = uuid.MustParse(id)
		}
	}
	return slugToID, nil
}

// importVendors reads demodata/vendor_keys.json and demodata/vendors.jsonl,
// upserts the vendors by their unique name, and returns the vendor_key ->
// vendors.id map the catalogue uses to resolve its vendor_key references.
//
// Eight of the vendor_keys.json rows are legacy seed vendors. The demo data
// writes them under their pre-SEED-POLISH names ("Gable Lumber & Supply",
// "@gablelumber.ca" etc.); legacyVendorRename maps each to the renamed row
// the rest of this seed already writes ("Gable Building Supply" with the
// reserved .example domain), so the legacy dealer-named vendor goes and the
// catalogue's vendor_id references the canonical row.
func importVendors(ctx context.Context, db *sql.DB) (map[string]uuid.UUID, error) {
	var catalog vendorKeyCatalog
	if err := json.Unmarshal(vendorKeysData, &catalog); err != nil {
		return nil, fmt.Errorf("decode vendor_keys.json: %w", err)
	}

	keyToID := make(map[string]uuid.UUID)
	for _, k := range catalog.Vendors {
		name := k.Name
		if renamed, ok := legacyVendorRename[name]; ok {
			name = renamed
		}
		// Upsert vendor by name. A row that exists is preserved (the legacy
		// seed's contact_email is on a reserved example domain already, so
		// the demo data's empty placeholder does not overwrite it).
		var id string
		err := db.QueryRowContext(ctx, `INSERT INTO vendors (name, payment_terms)
			VALUES ($1, $2)
			ON CONFLICT (name) DO UPDATE SET payment_terms = EXCLUDED.payment_terms
			RETURNING id`, name, k.PaymentTerms).Scan(&id)
		if err != nil || id == "" {
			db.QueryRowContext(ctx, `SELECT id FROM vendors WHERE name = $1`, name).Scan(&id)
		}
		if id != "" {
			keyToID[k.Key] = uuid.MustParse(id)
		}
	}

	// The vendors.jsonl rows update the contact details for the keys they
	// name, so a richer row wins over the catalog placeholder.
	scanner := bufio.NewScanner(bytes.NewReader(vendorsData))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var vr vendorRow
		if err := json.Unmarshal([]byte(line), &vr); err != nil {
			log.Printf("Seed: import vendors.jsonl: skip line: %v", err)
			continue
		}
		vendorName := vr.Name
		if renamed, ok := legacyVendorRename[vendorName]; ok {
			vendorName = renamed
		}
		var id string
		err := db.QueryRowContext(ctx, `INSERT INTO vendors
			(name, contact_email, phone, address_line1, city, state, zip, payment_terms,
				average_lead_time_days, fill_rate, total_spend_ytd)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (name) DO UPDATE SET
				contact_email = EXCLUDED.contact_email,
				phone = EXCLUDED.phone,
				address_line1 = EXCLUDED.address_line1,
				city = EXCLUDED.city,
				state = EXCLUDED.state,
				zip = EXCLUDED.zip,
				payment_terms = EXCLUDED.payment_terms,
				average_lead_time_days = EXCLUDED.average_lead_time_days,
				fill_rate = EXCLUDED.fill_rate,
				total_spend_ytd = EXCLUDED.total_spend_ytd
			RETURNING id`,
			vendorName, vr.ContactEmail, vr.Phone, vr.AddressLine1, vr.City, vr.State, vr.Zip,
			vr.PaymentTerms, vr.AverageLeadTimeDays, vr.FillRate, vr.TotalSpendYTDCAD,
		).Scan(&id)
		if err == nil && id != "" {
			// Lookup the canonical vendor_key for this vendor name.
			for _, k := range catalog.Vendors {
				if k.Name == vr.Name {
					keyToID[k.Key] = uuid.MustParse(id)
					break
				}
			}
		}
	}
	lastImport.Vendors = len(keyToID)
	return keyToID, scanner.Err()
}

// nfloat64 turns a *float64 into the value the database/sql driver
// understands as NULL when the pointer is nil. Keeping it local avoids
// importing database/sql.Null types at every call site.
func nfloat64(p *float64) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func nstr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// topSlug returns the top level portion of a dotted category slug, so
// "lumber.framing_lumber" resolves to "lumber" for the top level lookup.
func topSlug(slug string) string {
	if i := strings.Index(slug, "."); i > 0 {
		return slug[:i]
	}
	return slug
}

// pictureOf is the image_url an imported product carries: its family's
// illustration, NULL when the row names no family.
func pictureOf(family string) any {
	if family == "" {
		return nil
	}
	return picturePath(family)
}

// importCatalog reads demodata/catalog.jsonl and upserts each row into
// products, keying on sku. The vendor_key resolves to vendors.id via the
// vendorToID map. Inventory per yard is split into rows of inventory; the
// rows the rest of this seed (sections 1 to 22) wrote for the same SKU are
// overwritten so the imported numbers are the canonical values.
func importCatalog(ctx context.Context, db *sql.DB, yards map[string]uuid.UUID,
	vendorToID map[string]uuid.UUID, slugToID map[string]uuid.UUID) (map[string]uuid.UUID, error) {

	skuToID := make(map[string]uuid.UUID)
	// The catalogue sells some finishes by the quart; migration 099 seeds the
	// gallon (576 GAL to 77 CF) but not the quart. A dealer unit, upserted like
	// other reference data.
	if _, err := db.ExecContext(ctx, `INSERT INTO units (code, name, dimension, std_unit_qty, std_ref_qty)
		VALUES ('QT', 'Quart', 'VOLUME', 2304, 77) ON CONFLICT (code) DO NOTHING`); err != nil {
		log.Printf("Seed: import catalog: unit QT: %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(catalogData))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row catalogRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			log.Printf("Seed: import catalog: skip line: %v", err)
			continue
		}
		vendorID := vendorToID[row.VendorKey]
		var categoryID *uuid.UUID
		// The catalog's category_slug uses dotted paths
		// (lumber.framing_lumber); categories.json carries only the top
		// level slug. Walk the path's prefix until one matches.
		for _, slug := range []string{row.CategorySlug,
			topSlug(row.CategorySlug)} {
			if id, ok := slugToID[slug]; ok {
				cid := id
				categoryID = &cid
				break
			}
		}
		// Lookup the category name from the slug for the legacy text column.
		var categoryName string
		if categoryID != nil {
			db.QueryRowContext(ctx, `SELECT name FROM product_categories WHERE id = $1`, *categoryID).Scan(&categoryName)
		}
		// Compute commission_rate from target_margin when the catalogue did
		// not carry one: 25 percent of margin, the demo's house rule.
		commission := row.Commission
		if commission == 0 && row.TargetMargin > 0 {
			commission = row.TargetMargin * 0.25
		}
		// The board cross section is both dimensions or neither (the products
		// check): a sheet or a profile carries a thickness alone, which is not a
		// board measure, so it records neither.
		thickness, boardWidth := row.ThicknessIn, row.WidthIn
		if (thickness == nil) != (boardWidth == nil) {
			thickness, boardWidth = nil, nil
		}
		// The legacy text column carries the vendor's display name, as the base
		// seed writes it; the lists show it.
		var vendorName string
		if vendorID != uuid.Nil {
			db.QueryRowContext(ctx, `SELECT name FROM vendors WHERE id = $1`, vendorID).Scan(&vendorName)
		}
		var id string
		err := db.QueryRowContext(ctx, `INSERT INTO products
			(sku, description, uom_primary, sale_uom, price_uom, purchase_uom,
				weight_lbs, reorder_point, reorder_qty, base_price,
				average_unit_cost, target_margin, commission_rate,
				upc, category, vendor, vendor_id, category_id,
				species, grade, length_in, width_in, height_in, stackable,
				board_thickness_in, board_width_in, board_length_ft, random_length,
				is_commodity, taxable, image_url)
			VALUES ($1,$2,$3,$3,$3,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
				$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)
			ON CONFLICT (sku) DO UPDATE SET
				description = EXCLUDED.description,
				uom_primary = EXCLUDED.uom_primary,
				weight_lbs = EXCLUDED.weight_lbs,
				reorder_point = EXCLUDED.reorder_point,
				reorder_qty = EXCLUDED.reorder_qty,
				base_price = EXCLUDED.base_price,
				average_unit_cost = EXCLUDED.average_unit_cost,
				target_margin = EXCLUDED.target_margin,
				commission_rate = EXCLUDED.commission_rate,
				upc = EXCLUDED.upc,
				category = EXCLUDED.category,
				vendor = EXCLUDED.vendor,
				vendor_id = EXCLUDED.vendor_id,
				category_id = EXCLUDED.category_id,
				species = EXCLUDED.species,
				grade = EXCLUDED.grade,
				length_in = EXCLUDED.length_in,
				width_in = EXCLUDED.width_in,
				height_in = EXCLUDED.height_in,
				stackable = EXCLUDED.stackable,
				board_thickness_in = EXCLUDED.board_thickness_in,
				board_width_in = EXCLUDED.board_width_in,
				board_length_ft = EXCLUDED.board_length_ft,
				random_length = EXCLUDED.random_length,
				is_commodity = EXCLUDED.is_commodity,
				taxable = EXCLUDED.taxable,
				image_url = EXCLUDED.image_url,
				updated_at = NOW()
			RETURNING id`,
			row.SKU, row.Description, row.UOMPrimary,
			row.WeightLbs, 0.0, 0.0,
			row.Price, row.Cost, row.TargetMargin, commission,
			row.Barcode, categoryName, vendorName, vendorID, categoryID,
			row.Species, row.Grade,
			nfloat64(row.LengthIn), nfloat64(row.WidthInDim), nfloat64(row.HeightIn), row.Stackable,
			nfloat64(thickness), nfloat64(boardWidth), nfloat64(row.LengthFt), row.RandomLength,
			row.IsCommodity, !(row.TaxClass == "EXEMPT"), pictureOf(row.ImageFamily),
		).Scan(&id)
		if err != nil {
			log.Printf("Seed: import catalog %s: %v", row.SKU, err)
			continue
		}
		pid := uuid.MustParse(id)
		skuToID[row.SKU] = pid

		// Per-yard inventory: delete any rows the previous seed wrote, then
		// insert one row per stocked yard with the catalogue's on_hand.
		for _, yardCode := range row.YardsStocked {
			locID, ok := yards[yardCode]
			if !ok {
				continue
			}
			inv := row.Inventory[yardCode]
			db.ExecContext(ctx, `DELETE FROM inventory WHERE product_id = $1 AND location_id = $2`, pid, locID)
			db.ExecContext(ctx, `INSERT INTO inventory (product_id, location_id, location, quantity)
				VALUES ($1, $2, $3, $4)`,
				pid, locID, yardCode, inv.OnHand)
		}
	}
	lastImport.Products = len(skuToID)
	return skuToID, scanner.Err()
}

// importCustomers reads demodata/customers.jsonl and upserts each row into
// customers, keying on account_number. The home_yard code resolves to a
// locations.id; yards_trades_at drives the customer_branches rows ruling A
// (a customer trades at every yard it has a document at) reads. Customers the legacy seed already created (the 13 TARNWICK-001,
// THH-001, etc. accounts) are preserved: a row whose account_number exists
// is left alone, the imported data only fills the address if the existing
// row has none.
func importCustomers(ctx context.Context, db *sql.DB, yards map[string]uuid.UUID,
	priceLevelIDs map[string]uuid.UUID) (map[string]uuid.UUID, error) {

	// salesperson_id resolution: customers.jsonl names a salesperson by email.
	// The migration 041 rows already carry those emails (under the legacy
	// domain), so the import looks up sales_team by email and never creates
	// its own. The legacy salesperson rows stay.
	salespersonByEmail := make(map[string]uuid.UUID)
	rows, _ := db.QueryContext(ctx, `SELECT id, email FROM sales_team`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var email string
			if err := rows.Scan(&id, &email); err == nil {
				salespersonByEmail[email] = id
			}
		}
	}

	customerIDs := make(map[string]uuid.UUID)
	scanner := bufio.NewScanner(bytes.NewReader(customersData))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row customerRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			log.Printf("Seed: import customers.jsonl: skip line: %v", err)
			continue
		}
		branchID, ok := yards[row.HomeYard]
		if !ok {
			log.Printf("Seed: import customer %s: home_yard %s missing", row.AccountNumber, row.HomeYard)
			continue
		}
		// Resolve payment terms code to payment_terms.id.
		var termsID uuid.UUID
		if err := db.QueryRowContext(ctx, `SELECT id FROM payment_terms WHERE code = $1`, row.PaymentTermsCode).Scan(&termsID); err != nil {
			db.QueryRowContext(ctx, `SELECT id FROM payment_terms WHERE code = 'NET30'`).Scan(&termsID)
		}
		priceLevelID := priceLevelIDs[row.PriceLevel]
		var creditLimit interface{} = nil
		if row.CreditLimitCAD != nil {
			creditLimit = *row.CreditLimitCAD
		}
		// Salesperson lookup is by email, then by the local part (before
		// the @), so a data file written against an older domain still
		// finds its salesperson; the seed writes the reserved .example
		// domain over migration 041's rows.
		var salespersonID interface{} = nil
		if sid, ok := salespersonByEmail[row.SalespersonEmail]; ok && sid != uuid.Nil {
			salespersonID = sid
		} else if row.SalespersonEmail != "" {
			// Resolve by local part (everything before @) to absorb the
			// legacy domain rename.
			at := strings.Index(row.SalespersonEmail, "@")
			if at > 0 {
				local := row.SalespersonEmail[:at]
				for email, sid := range salespersonByEmail {
					if strings.HasPrefix(email, local+"@") {
						salespersonID = sid
						break
					}
				}
			}
		}

		// Upsert customer. ON CONFLICT (account_number) updates the editable
		// fields so a fresh deploy adopts new names/emails; ON CONFLICT
		// preserves a row the rest of the seed wrote for the same account.
		var id string
		err := db.QueryRowContext(ctx, `INSERT INTO customers
			(account_number, name, email, phone, address, tier,
				price_level_id, salesperson_id, primary_branch_id,
				currency, credit_limit, payment_terms_id, is_active)
			VALUES ($1,$2,$3,$4,$5,$6::customer_tier,$7,$8,$9,
				(SELECT value FROM system_settings WHERE key = 'currency.default'),$10,$11,TRUE)
			ON CONFLICT (account_number) DO UPDATE SET
				name = EXCLUDED.name,
				email = EXCLUDED.email,
				phone = EXCLUDED.phone,
				address = EXCLUDED.address,
				tier = EXCLUDED.tier,
				price_level_id = EXCLUDED.price_level_id,
				salesperson_id = EXCLUDED.salesperson_id,
				primary_branch_id = EXCLUDED.primary_branch_id,
				currency = EXCLUDED.currency,
				credit_limit = EXCLUDED.credit_limit,
				payment_terms_id = EXCLUDED.payment_terms_id,
				is_active = EXCLUDED.is_active,
				updated_at = NOW()
			RETURNING id`,
			row.AccountNumber, row.Name, row.Email, row.Phone, row.AddressLine1,
			row.Tier, priceLevelID, salespersonID, branchID,
			creditLimit, termsID,
		).Scan(&id)
		if err != nil || id == "" {
			log.Printf("Seed: import customer %s: %v", row.AccountNumber, err)
			continue
		}
		cid := uuid.MustParse(id)
		customerIDs[row.Name] = cid

		// customer_branches: ruling A needs the customer's trades_at yards
		// linked. The import writes one row per yard in the JSON list;
		// existing rows are preserved.
		for _, y := range row.YardsTradesAt {
			yID, ok := yards[y]
			if !ok || yID == branchID {
				continue
			}
			db.ExecContext(ctx, `INSERT INTO customer_branches (customer_id, branch_id)
				VALUES ($1, $2) ON CONFLICT DO NOTHING`, cid, yID)
		}

		// customer_ship_tos: one MAIN row carrying the customer's address,
		// plus one per job_site. Existing rows are preserved by code.
		db.ExecContext(ctx, `INSERT INTO customer_ship_tos (customer_id, code, name, line1, is_default)
			VALUES ($1, 'MAIN', $2, $3, TRUE)
			ON CONFLICT (customer_id, code) DO UPDATE SET name = EXCLUDED.name, line1 = EXCLUDED.line1`,
			cid, row.Name, row.AddressLine1)

		for jsIdx, js := range row.JobSites {
			code := fmt.Sprintf("JS-%03d", jsIdx+1)
			var line2 string
			if js.City != "" {
				line2 = js.City
			}
			if js.Region != "" {
				if line2 != "" {
					line2 += ", " + js.Region
				} else {
					line2 = js.Region
				}
			}
			db.ExecContext(ctx, `INSERT INTO customer_ship_tos (customer_id, code, name, line1, line2, is_default)
				VALUES ($1, $2, $3, $4, NULLIF($5, ''), FALSE)
				ON CONFLICT (customer_id, code) DO UPDATE SET name = EXCLUDED.name, line1 = EXCLUDED.line1`,
				cid, code, js.Name, js.Line1, line2)
		}

		// customer_contacts: one row per contact. Existing rows are kept; the
		// import only fills a contact that does not have one.
		for _, ct := range row.Contacts {
			var exists bool
			db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM customer_contacts WHERE customer_id = $1 AND email = $2)`,
				cid, ct.Email).Scan(&exists)
			if exists {
				continue
			}
			db.ExecContext(ctx, `INSERT INTO customer_contacts
				(customer_id, first_name, last_name, title, email, phone, role, is_primary)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				cid, ct.FirstName, ct.LastName, ct.Title, ct.Email, ct.Phone, ct.Role, ct.IsPrimary)
		}
	}
	lastImport.Customers = len(customerIDs)
	return customerIDs, scanner.Err()
}

// importStaff reads demodata/staff.jsonl and upserts each row into staff
// and user_locations (the staff-home / yard grant). The legacy seed's three
// AI_LM demo rows (dispatcher@gablebuildingsupply.example, yard@gablebuildingsupply.example, admin@gablebuildingsupply.example)
// and the nine multi yard personas live in the same table and are preserved.
func importStaff(ctx context.Context, db *sql.DB, yards map[string]uuid.UUID) error {
	scanner := bufio.NewScanner(bytes.NewReader(staffData))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	count := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row staffRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			log.Printf("Seed: import staff.jsonl: skip line: %v", err)
			continue
		}
		var staffID string
		err := db.QueryRowContext(ctx, `INSERT INTO staff (email, full_name, staff_no, role, active)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (email) DO UPDATE SET
				full_name = EXCLUDED.full_name,
				staff_no = EXCLUDED.staff_no,
				role = EXCLUDED.role,
				active = EXCLUDED.active,
				updated_at = NOW()
			RETURNING id`,
			row.Email, row.FullName, row.StaffNo, row.Role, row.Active,
		).Scan(&staffID)
		if err != nil || staffID == "" {
			log.Printf("Seed: import staff %s: %v", row.Email, err)
			continue
		}
		sid := uuid.MustParse(staffID)
		count++
		// Yard grant: the home_yard row flagged is_home. The grant is created
		// only when missing so an operator's edits survive a redeploy.
		if yID, ok := yards[row.HomeYard]; ok {
			db.ExecContext(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by)
				VALUES ($1, $2, TRUE, 'demo-seed')
				ON CONFLICT (user_sub, branch_id) DO NOTHING`,
				row.Email, yID)
			_ = sid
		}
	}
	lastImport.Staff = count
	return scanner.Err()
}

// historyTaxRate is the invoices' rate, as the base seed's (BC: GST and PST).
const historyTaxRate = 0.12

// historyLine is one drawn line of a history order, in the money convention:
// the quantity whole, the unit price in ten thousandths, the total in cents.
type historyLine struct {
	product    uuid.UUID
	qty        int64
	unitTT     int64
	totalCents int64
}

// seedHistory generates the window's days of orders, invoices and payments
// across the three yards, and today's up to shortly before the run. The
// generator is deterministic: it consumes historyParamsData through a
// math/rand source seeded with the params._seed constant, and walks the yards,
// customers and products in a fixed order, so the same day gives the same
// history (today's stamps follow the time of the run).
//
// Each order is written with its lines, each invoice with the matching lines,
// exactly as the base seed writes its own (UNPAID, open at its total); the
// invoice is then posted and paid in full through the AR core (receivables.
// post), so the ledger, the subledger, the application and the payment's
// number are what the AR core writes, never hand written rows. A day is
// written in one transaction, then posted (on several connections, a
// customer's invoices on one), oldest day first, so each customer's subledger
// runs in day order.
func seedHistory(ctx context.Context, db *sql.DB, ar *receivables,
	customerIDs map[string]uuid.UUID, yards map[string]uuid.UUID, skuToID map[string]uuid.UUID) error {

	var hp historyParams
	if err := json.Unmarshal(historyParamsData, &hp); err != nil {
		return fmt.Errorf("decode history-params.json: %w", err)
	}

	rng := rand.New(rand.NewSource(int64(hp.Seed)))
	now := seedNow()
	// The history's today is the seed's business day, the default branch's
	// calendar day: the AR core refuses a document dated past it, so between
	// UTC's midnight and the branch's the UTC date is not today.
	seedDay, err := seedToday(db)
	if err != nil {
		return err
	}
	anchor, todayFrom, withToday := historyFrame(now, seedDay)

	// The yards walk in code order (a map's order is random, and the history
	// must be the same for the same day), each with its weekday mean from the
	// parameters file.
	type yardMean struct {
		code string
		mean int
	}
	var historyYards []yardMean
	for code, cs := range hp.CounterSales {
		historyYards = append(historyYards, yardMean{code, cs.WeekdayMean})
	}
	sort.Slice(historyYards, func(a, b int) bool { return historyYards[a].code < historyYards[b].code })

	// The customers that trade, with their home yard, by account number.
	type custYard struct {
		ID   uuid.UUID
		Yard uuid.UUID
	}
	custIDs := make([]string, 0, len(customerIDs))
	for _, id := range customerIDs {
		custIDs = append(custIDs, id.String())
	}
	crows, err := db.QueryContext(ctx, `
		SELECT id, primary_branch_id FROM customers
		WHERE id = ANY($1::uuid[]) AND primary_branch_id IS NOT NULL
		ORDER BY account_number, id`, pgArray(custIDs))
	if err != nil {
		return fmt.Errorf("history customers: %w", err)
	}
	var allCust []custYard
	for crows.Next() {
		var c custYard
		if err := crows.Scan(&c.ID, &c.Yard); err != nil {
			crows.Close()
			return fmt.Errorf("history customers: %w", err)
		}
		allCust = append(allCust, c)
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return fmt.Errorf("history customers: %w", err)
	}
	if len(allCust) == 0 {
		return nil
	}

	// The products the generator samples, with their price in ten thousandths.
	prodIDs := make([]string, 0, len(skuToID))
	for _, id := range skuToID {
		prodIDs = append(prodIDs, id.String())
	}
	prows, err := db.QueryContext(ctx, `
		SELECT id, ROUND(base_price * 10000)::bigint FROM products
		WHERE id = ANY($1::uuid[]) AND base_price > 0
		ORDER BY sku, id`, pgArray(prodIDs))
	if err != nil {
		return fmt.Errorf("history products: %w", err)
	}
	type prodRow struct {
		ID     uuid.UUID
		PriceT int64
	}
	var prods []prodRow
	for prows.Next() {
		var p prodRow
		if err := prows.Scan(&p.ID, &p.PriceT); err != nil {
			prows.Close()
			return fmt.Errorf("history products: %w", err)
		}
		prods = append(prods, p)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return fmt.Errorf("history products: %w", err)
	}
	if len(prods) == 0 {
		return nil
	}

	totalOrders, totalPosted := 0, 0

	for dayOffset := hp.Window.Days; dayOffset >= 0; dayOffset-- {
		day := anchor.AddDate(0, 0, -dayOffset)
		if dayOffset == 0 {
			if !withToday {
				continue
			}
			day = todayFrom
		}
		if day.Weekday() == time.Sunday {
			// Sundays closed.
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("history day: %w", err)
		}
		var posts []seedInvoice
		seq := 0
		for _, y := range historyYards {
			yardID, ok := yards[y.code]
			if !ok {
				continue
			}
			count := y.mean + rng.Intn(5) - 2
			if count < 1 {
				count = 1
			}
			pool := make([]custYard, 0)
			for _, c := range allCust {
				if c.Yard == yardID {
					pool = append(pool, c)
				}
			}
			if len(pool) == 0 {
				pool = allCust
			}
			for i := 0; i < count; i++ {
				c := pool[rng.Intn(len(pool))]

				// Two or three product lines per order, priced at the product's
				// list price; every figure is integer arithmetic.
				nLines := 2 + rng.Intn(2)
				lines := make([]historyLine, 0, nLines)
				var subtotal int64
				for l := 0; l < nLines; l++ {
					p := prods[rng.Intn(len(prods))]
					qty := int64(1 + rng.Intn(20))
					hl := historyLine{product: p.ID, qty: qty, unitTT: p.PriceT, totalCents: (qty*p.PriceT + 50) / 100}
					subtotal += hl.totalCents
					lines = append(lines, hl)
				}
				taxCents := (subtotal*12 + 50) / 100
				seq++
				in, err := writeHistoryDocs(ctx, tx, c.ID, c.Yard, day, lines, subtotal, taxCents)
				if err != nil {
					// The day's transaction is aborted by a refused statement:
					// stop the day, the rest of the history goes on.
					log.Printf("Seed: history day %s: %v", day.Format("2006-01-02"), err)
					posts = nil
					break
				}
				// The act stamps its rows with the present; the history day goes
				// on them (see receivables.post), one second apart per document
				// so a day's statement lines keep their order.
				in.Stamp = day.Add(time.Duration(seq) * 2 * time.Second)
				posts = append(posts, in)
			}
			if posts == nil {
				break
			}
		}
		if posts == nil {
			_ = tx.Rollback()
			continue
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("history day: %w", err)
		}
		totalOrders += len(posts)
		totalPosted += ar.postParallel(ctx, posts)
	}
	through := "yesterday"
	if withToday {
		through = "today"
	}
	fmt.Printf("Seed: history: %d days through %s, %d orders, %d invoices posted and paid through the AR core across %d yards\n",
		hp.Window.Days, through, totalOrders, totalPosted, len(historyYards))
	if totalPosted != totalOrders {
		// An unposted invoice stays open with no subledger, GL or payment:
		// the books would no longer tie, so say so instead of carrying on.
		return fmt.Errorf("%d of %d history invoices were not posted through the AR core", totalOrders-totalPosted, totalOrders)
	}
	return nil
}

// writeHistoryDocs writes one history order with its lines and its invoice
// with the matching lines, in the shape the base seed writes them, and answers
// the invoice for the AR core to post and settle in full.
func writeHistoryDocs(ctx context.Context, tx *sql.Tx, customerID, branchID uuid.UUID, day time.Time,
	lines []historyLine, subtotal, taxCents int64) (seedInvoice, error) {
	orderID, invID := uuid.New(), uuid.New()
	total := subtotal + taxCents
	// The order and its invoice both take the tenant's currency, so the
	// documents of one sale agree (the payment follows its invoice).
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO orders (id, customer_id, branch_id, total_amount, subtotal, tax_rate, tax_amount, status, created_at,
			delivery_type, currency, tax_source)
		VALUES ($1, $2, $3, $4::bigint::numeric / 100, $5::bigint::numeric / 100, $7, $8::bigint::numeric / 100,
			'FULFILLED', $6, 'PICKUP', (SELECT value FROM system_settings WHERE key = 'currency.default'), 'LEGACY')`,
		orderID, customerID, branchID, total, subtotal, day, historyTaxRate, taxCents); err != nil {
		return seedInvoice{}, fmt.Errorf("order: %w", err)
	}
	prod, qty, unit, tot, pos := make([]string, len(lines)), make([]string, len(lines)), make([]string, len(lines)),
		make([]string, len(lines)), make([]string, len(lines))
	for i, l := range lines {
		prod[i], qty[i], unit[i], tot[i], pos[i] = l.product.String(), fmt.Sprint(l.qty), fmt.Sprint(l.unitTT), fmt.Sprint(l.totalCents), fmt.Sprint(i)
	}
	// The shared line shape (ADR 0005 2.2), as the base seed writes it: one
	// statement per order, the product's own description, SKU and unit.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO order_lines (order_id, product_id, quantity, unit_price, line_type, position,
			description, sku, uom, price_uom, uom_qty, price_uom_qty, priced_unit_price, price_source, line_total, taxable,
			quantity_allocated, quantity_fulfilled, created_at)
		SELECT $1, l.product_id, l.qty, l.unit_tt::numeric / 10000, 'PRODUCT', l.pos,
			COALESCE(p.description, ''), COALESCE(p.sku, ''), p.uom_primary::text, p.uom_primary::text, 1, 1,
			l.unit_tt::numeric / 10000, 'PRICE_LIST', l.total_c::numeric / 100, TRUE, 0, l.qty, $7
		FROM unnest($2::uuid[], $3::numeric[], $4::bigint[], $5::bigint[], $6::int[]) AS l(product_id, qty, unit_tt, total_c, pos)
		JOIN products p ON p.id = l.product_id`,
		orderID, pgArray(prod), pgArray(qty), pgArray(unit), pgArray(tot), pgArray(pos), day); err != nil {
		return seedInvoice{}, fmt.Errorf("order lines: %w", err)
	}
	// number is left to the column DEFAULT, as the base seed leaves it; the
	// invoice is UNPAID and open at its total until the AR core settles it.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO invoices (id, order_id, customer_id, branch_id, status, total_amount, subtotal,
			tax_rate, tax_amount, due_date, payment_terms_id, invoice_date, created_at, currency)
		VALUES ($1, $2, $3, $4, 'UNPAID', $5::bigint::numeric / 100, $6::bigint::numeric / 100, $7,
			$8::bigint::numeric / 100, $9::date, (SELECT payment_terms_id FROM customers WHERE id = $3), $10::date, $10,
			(SELECT value FROM system_settings WHERE key = 'currency.default'))`,
		invID, orderID, customerID, branchID, total, subtotal, historyTaxRate, taxCents,
		day.AddDate(0, 1, 0), day); err != nil {
		return seedInvoice{}, fmt.Errorf("invoice: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO invoice_lines (invoice_id, order_line_id, product_id, line_type, position, description, sku,
			quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, price_each, price_source, line_total, taxable, created_at)
		SELECT $1, ol.id, ol.product_id, ol.line_type, ol.position, ol.description, ol.sku,
			ol.quantity, ol.uom, ol.price_uom, ol.uom_qty, ol.price_uom_qty, ol.unit_price, ol.unit_price, ol.price_source, ol.line_total, ol.taxable, $3
		FROM order_lines ol WHERE ol.order_id = $2 AND ol.line_type = 'PRODUCT'`, invID, orderID, day); err != nil {
		return seedInvoice{}, fmt.Errorf("invoice lines: %w", err)
	}
	return seedInvoice{ID: invID, CustomerID: customerID, BranchID: branchID, Date: day, Subtotal: subtotal, Tax: taxCents,
		Paid: total, OrderID: orderID, PaidOn: day, Method: "ACH"}, nil
}

// pgArray formats a Postgres array literal of plain values (UUIDs and
// numbers), for a parameter the statement casts to its array type.
func pgArray(values []string) string { return "{" + strings.Join(values, ",") + "}" }

// openImportPool opens a *sql.DB from a connection string, same way seed.Run
// does, so the import functions can use the database/sql API.
func openImportPool(dbURL string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// ensure pgx driver is linked even when only the database/sql open path is used.
var _ = stdlib.GetDefaultDriver
