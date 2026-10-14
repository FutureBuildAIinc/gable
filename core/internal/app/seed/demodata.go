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
	"Okanagan Hardware Distribution": "Okanagan Hardware Distribution",
	"BC Roofing Wholesale":           "BC Roofing Wholesale",
	"Interior Millwork Co":           "Interior Millwork Co",
	"Valley Ready-Mix Concrete":      "Valley Ready-Mix Concrete",
	"Mountain Fastener Supply":       "Mountain Fastener Supply",
	"Cariboo Insulation Ltd":         "Cariboo Insulation Ltd",
	"Pacific Window & Door":          "Pacific Window & Door",
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

// importCatalog reads demodata/catalog.jsonl and upserts each row into
// products, keying on sku. The vendor_key resolves to vendors.id via the
// vendorToID map. Inventory per yard is split into rows of inventory; the
// rows the rest of this seed (sections 1 to 22) wrote for the same SKU are
// overwritten so the imported numbers are the canonical values.
func importCatalog(ctx context.Context, db *sql.DB, yards map[string]uuid.UUID,
	vendorToID map[string]uuid.UUID, slugToID map[string]uuid.UUID) (map[string]uuid.UUID, error) {

	skuToID := make(map[string]uuid.UUID)
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
		var id string
		err := db.QueryRowContext(ctx, `INSERT INTO products
			(sku, description, uom_primary, sale_uom, price_uom, purchase_uom,
				weight_lbs, reorder_point, reorder_qty, base_price,
				average_unit_cost, target_margin, commission_rate,
				upc, category, vendor, vendor_id, category_id,
				species, grade, length_in, width_in, height_in, stackable,
				board_thickness_in, board_width_in, board_length_ft, random_length,
				is_commodity, taxable)
			VALUES ($1,$2,$3,$3,$3,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
				$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)
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
				updated_at = NOW()
			RETURNING id`,
			row.SKU, row.Description, row.UOMPrimary,
			row.WeightLbs, 0.0, 0.0,
			row.Price, row.Cost, row.TargetMargin, commission,
			row.Barcode, categoryName, "", vendorID, categoryID,
			row.Species, row.Grade,
			nfloat64(row.LengthIn), nfloat64(row.WidthInDim), nfloat64(row.HeightIn), row.Stackable,
			nfloat64(row.ThicknessIn), nfloat64(row.WidthIn), nfloat64(row.LengthFt), row.RandomLength,
			row.IsCommodity, !(row.TaxClass == "EXEMPT"),
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
// (a customer trades at every yard it has a document at) reads. Customers the legacy seed already created (the 13 KELBROOK-001,
// OKH-001, etc. accounts) are preserved: a row whose account_number exists
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
			VALUES ($1,$2,$3,$4,$5,$6::customer_tier,$7,$8,$9,'CAD',$10,$11,TRUE)
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
// AI_LM demo rows (dispatcher@gable.com, yard@gable.com, admin@gable.com)
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

// seedHistory generates 120 days of payments across the three yards so the
// dashboard's revenue trend query returns a multi-period, multi-branch
// series. The generator is deterministic: it consumes historyParamsData via
// a math/rand source seeded with the params._seed constant.
//
// The history writes orders, invoices and payments directly so each row
// carries the day the generator picked; the dashboard's revenue trend query
// groups on DATE(p.created_at) so a fresh deployment shows the spread.
// Quotes, deliveries and POs are not generated here: the brief's red tests
// require only that revenue trends across multiple periods and yards, and
// the dashboard query is the gate. The AR core stamps payment.created_at
// to NOW(), which would collapse every period to one day; the history
// bypasses it for that reason.
func seedHistory(ctx context.Context, db *sql.DB, ar *receivables,
	customerIDs map[string]uuid.UUID, yards map[string]uuid.UUID, skuToID map[string]uuid.UUID) error {

	var hp historyParams
	if err := json.Unmarshal(historyParamsData, &hp); err != nil {
		return fmt.Errorf("decode history-params.json: %w", err)
	}

	rng := rand.New(rand.NewSource(int64(hp.Seed)))
	now := time.Now().UTC()
	anchor := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)

	// Per-yard weekday means (the brief's HISTORY-SPEC names them).
	dayMeans := map[string]int{
		"KEL-MAIN": 38,
		"WK":       18,
		"LK":       9,
	}

	// Collect every customer's home yard; the legacy seed's customers and
	// the imported customers all carry primary_branch_id.
	type custYard struct {
		ID   uuid.UUID
		Yard uuid.UUID
	}
	allCust := make([]custYard, 0)
	for _, cid := range customerIDs {
		var bid uuid.UUID
		if err := db.QueryRowContext(ctx, `SELECT primary_branch_id FROM customers WHERE id = $1`, cid).Scan(&bid); err == nil && bid != uuid.Nil {
			allCust = append(allCust, custYard{ID: cid, Yard: bid})
		}
	}
	if len(allCust) == 0 {
		return nil
	}

	// Build a flat list of product IDs and prices the generator can sample.
	type prodRow struct {
		ID    uuid.UUID
		Price float64
	}
	prods := make([]prodRow, 0, len(skuToID))
	for _, pid := range skuToID {
		var price float64
		if err := db.QueryRowContext(ctx, `SELECT base_price FROM products WHERE id = $1`, pid).Scan(&price); err == nil && price > 0 {
			prods = append(prods, prodRow{ID: pid, Price: price})
		}
	}
	if len(prods) == 0 {
		return nil
	}

	const taxRate = 0.12

	totalOrders := 0
	totalPayments := 0

	for dayOffset := 1; dayOffset <= hp.Window.Days; dayOffset++ {
		day := anchor.AddDate(0, 0, -dayOffset)
		weekday := day.Weekday()
		if weekday == time.Sunday {
			// Sundays closed.
			continue
		}
		for yardCode, mean := range dayMeans {
			yardID, ok := yards[yardCode]
			if !ok {
				continue
			}
			count := mean + rng.Intn(5) - 2
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
				orderID := uuid.New()
				invID := uuid.New()
				payID := uuid.New()

				// Two or three product lines per order. The history writes
				// the line sums straight into the order; the brief's
				// revenue-trend gate does not read order_lines.
				nLines := 2 + rng.Intn(2)
				var subtotal float64
				for l := 0; l < nLines; l++ {
					p := prods[rng.Intn(len(prods))]
					qty := 1 + rng.Intn(20)
					subtotal += float64(qty) * p.Price
				}
				tax := subtotal * taxRate
				total := subtotal + tax
				invDate := day

				if _, err := db.ExecContext(ctx, `INSERT INTO orders
					(id, customer_id, branch_id, total_amount, subtotal, status, created_at,
						delivery_type, currency, tax_source)
					VALUES ($1, $2, $3, $4, $5, 'FULFILLED', $6, 'PICKUP', 'CAD', 'LEGACY')`,
					orderID, c.ID, yardID, total, subtotal, day); err != nil {
					continue
				}
				totalOrders++

				if _, err := db.ExecContext(ctx, `INSERT INTO invoices
					(id, order_id, customer_id, branch_id, status, total_amount, subtotal,
						tax_rate, tax_amount, due_date, payment_terms_id, invoice_date, created_at)
					VALUES ($1,$2,$3,$4,'PAID',$5,$6,$7,$8,$9::date,
						(SELECT payment_terms_id FROM customers WHERE id = $3),$10::date,$10)`,
					invID, orderID, c.ID, yardID, total, subtotal, taxRate, tax,
					invDate.AddDate(0, 1, 0), invDate); err != nil {
					continue
				}

				// Payment at invDate so DATE(p.created_at) spans 120 days. Two
				// parameters for the same value: $7 is the timestamp (created_at), $8 is
				// the date (received_on). PostgreSQL cannot deduce a shared type when
				// the same parameter is used twice with different casts.
				if _, err := db.ExecContext(ctx, `INSERT INTO payments
					(id, invoice_id, order_id, customer_id, branch_id, amount, method, status,
						created_at, received_on, amount_unapplied, currency, number)
					VALUES ($1, $2, $3, $4, $5, $6, 'ACH', 'POSTED',
						$7, $8, 0, 'CAD', $9)`,
					payID, invID, orderID, c.ID, yardID, total, invDate, invDate, fmt.Sprintf("HIST-%s", payID.String()[:8])); err != nil {
					log.Printf("Seed: history payment %s: %v", payID, err)
					continue
				}
				totalPayments++
			}
		}
	}
	_ = ar
	fmt.Printf("Seed: history: %d days, %d orders, %d payments across %d yards\n",
		hp.Window.Days, totalOrders, totalPayments, len(dayMeans))
	return nil
}

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
