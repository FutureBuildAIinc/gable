// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

// The multi yard demo world of Gable Building Supply (M1): the single branch
// switch on, one register per yard, stock that differs by yard with a few
// products held at the main yard only, the invented staff with their yard
// grants, a driver for the main yard's truck, a purchase order sent to a
// vendor and waiting at the main yard, customers close to and over their
// credit limits, and a few customers trading at a second yard through their
// own documents. Everything here is invented; the rows the seed owns it
// writes by natural key, and it never moves a row an operator changed.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/google/uuid"
)

// demoPersona is one invented person of the demo: a subject, a name, a role,
// a home yard and the yards granted (user_locations).
type demoPersona struct {
	Subject, Name, Role, Home string
	Yards                     []string
}

var allYards = []string{"KEL-MAIN", "WK", "LK"}

// demoPersonas is the roster of docs/parity/WALKS.md scene 0. The dev sign in
// shows one card per entry.
var demoPersonas = []demoPersona{
	{"demo-owner", "Morgan Hale", "owner", "KEL-MAIN", allYards},
	{"demo-finance", "Robin Diaz", "finance", "KEL-MAIN", allYards},
	{"demo-sales-kel", "Avery Brooks", "sales", "KEL-MAIN", []string{"KEL-MAIN"}},
	{"demo-cashier-kel", "Jordan Pike", "cashier", "KEL-MAIN", []string{"KEL-MAIN"}},
	{"demo-warehouse-kel", "Sam Ortiz", "warehouse", "KEL-MAIN", []string{"KEL-MAIN"}},
	{"demo-purchasing", "Casey Lin", "purchasing", "KEL-MAIN", allYards},
	{"demo-sales-wk", "Taylor Reed", "sales", "WK", []string{"WK"}},
	{"demo-cashier-lk", "Drew Patel", "cashier", "LK", []string{"LK"}},
	{"demo-driver-kel", "Alex Moreno", "driver", "KEL-MAIN", []string{"KEL-MAIN"}},
}

// yardRegisters are the registers the seed owns beside REG-01 (the main
// yard's, moved by section 22). A row that exists is never touched.
var yardRegisters = []struct{ ID, Yard, Name string }{
	{"REG-WK", "WK", "Counter - West Kelowna"},
	{"REG-LK", "LK", "Counter - Lake Country"},
}

// mainYardOnlySKUs are held at the main yard alone, so a stock lookup across
// yards shows a product the satellites do not carry.
var mainYardOnlySKUs = []string{
	"LUM-6612-PT", "LUM-448-WRC", "DR-EXT-3680-STL", "RF-ICE-WTR", "INS-R30-24", "CORNSFT1612V",
}

// seedExec runs one statement and logs a failure under the section's name, so
// a half built demo does not end in an unqualified success line.
func seedExec(db *sql.DB, section, query string, args ...any) {
	if _, err := db.Exec(query, args...); err != nil {
		log.Printf("Seed: %s: %v", section, err)
	}
}

// upsertPersonas writes the staff rows and the yard grants. The subject is the
// staff number, the address an invented example domain, and the home yard the
// grant flagged is_home.
//
// The repave is deliberate. The multi yard seed runs only with DEMO_SEED on a
// demo environment and, like every seed section (AGENTS.md, "Seed resets
// transactional data each run; reference data upserts"), puts its own rows
// back on every run: the personas, their grants, the multi_branch_enabled
// switch and the driver are the demo's own rows, and an edit an operator
// makes to one of them is overwritten by the next seed.
func upsertPersonas(db *sql.DB, yards map[string]uuid.UUID) error {
	for _, p := range demoPersonas {
		if _, err := db.Exec(`INSERT INTO staff (email, full_name, staff_no, role, active)
			VALUES ($1, $2, $3, $4, TRUE)
			ON CONFLICT (email) DO UPDATE SET full_name = $2, staff_no = $3, role = $4, active = TRUE, updated_at = NOW()`,
			p.Subject+"@gablebuildingsupply.example", p.Name, p.Subject, p.Role); err != nil {
			return fmt.Errorf("staff %s: %w", p.Subject, err)
		}
		for _, y := range p.Yards {
			id, ok := yards[y]
			if !ok {
				return fmt.Errorf("persona %s names yard %s, which the seed has no row for", p.Subject, y)
			}
			// A grant that exists is never moved, and a home flag is never set
			// on a user whose home an operator already put elsewhere.
			if _, err := db.Exec(`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by)
				VALUES ($1, $2, $3 AND NOT EXISTS (SELECT 1 FROM user_locations h
					WHERE h.user_sub = $1 AND h.is_home AND h.branch_id <> $2), 'demo-seed')
				ON CONFLICT (user_sub, branch_id) DO NOTHING`,
				p.Subject, id, y == p.Home); err != nil {
				return fmt.Errorf("grant %s at %s: %w", p.Subject, y, err)
			}
		}
	}
	return nil
}

// seedMultiYard writes the multi yard world on top of the sections above it.
func seedMultiYard(ctx context.Context, db *sql.DB, ar *receivables, yards map[string]uuid.UUID, skuToID map[string]uuid.UUID,
	customerIDs map[string]uuid.UUID, vendorIDs map[string]uuid.UUID, custToBranch map[uuid.UUID]uuid.UUID, priceLevelIDs map[string]uuid.UUID) {

	// The single branch switch on: the desk's yard switcher shows the yards and
	// each yard's desk reads its own yard's documents. DEMO_MULTI_YARD=0 leaves
	// the switch where it is (the browser end to end run sends no yard of its
	// own and reads the whole seed).
	if v := os.Getenv("DEMO_MULTI_YARD"); v != "0" && v != "false" {
		if _, err := db.Exec(`INSERT INTO system_settings (key, value) VALUES ('multi_branch_enabled', 'true')
			ON CONFLICT (key) DO UPDATE SET value = 'true'`); err != nil {
			log.Printf("Seed: multi_branch_enabled: %v", err)
		}
	}

	if err := upsertPersonas(db, yards); err != nil {
		log.Printf("Seed: personas: %v", err)
	}

	for _, r := range yardRegisters {
		y, ok := yards[r.Yard]
		if !ok {
			continue
		}
		if _, err := db.Exec(`INSERT INTO pos_registers (id, location_id, branch_id, name, is_active)
			VALUES ($1, $2, $2, $3, true) ON CONFLICT (id) DO NOTHING`, r.ID, y, r.Name); err != nil {
			log.Printf("Seed: register %s: %v", r.ID, err)
		}
	}

	for _, sku := range mainYardOnlySKUs {
		pid, ok := skuToID[sku]
		if !ok {
			continue
		}
		for _, y := range []string{"WK", "LK"} {
			seedExec(db, "satellite stock delete "+sku, `DELETE FROM inventory WHERE product_id = $1 AND location_id = $2`, pid, yards[y])
		}
	}

	// The main yard's driver, for the one truck the walks dispatch.
	seedExec(db, "driver", `INSERT INTO drivers (name, license_number, phone_number, status, cdl_class, cdl_expiry, hire_date, email)
		VALUES ('Alex Moreno', 'BC-CDL-99001', '250-555-4010', 'ACTIVE', '1', '2027-06-30'::date, '2021-05-03'::date,
			'demo-driver-kel@gablebuildingsupply.example')
		ON CONFLICT (license_number) WHERE deleted_at IS NULL
		DO UPDATE SET name = 'Alex Moreno', status = 'ACTIVE'`)

	kel := yards["KEL-MAIN"]

	// Quillfield Formwork stands close to its credit limit: a $20,000 limit and
	// one open invoice of $18,816.00 posted through the AR core. It is a
	// customer of the seed's own, so no scenario that reads another customer's
	// credit moves.
	if ar != nil && yards["WK"] != uuid.Nil {
		cid, branch, err := seedCustomer(db, demoCustomer{
			Name: "Quillfield Formwork Ltd", Acct: "QFC-001", Email: "billing@quillfieldformwork.example", Phone: "250-555-3300",
			Addr: "610 Plywood Ct, West Kelowna BC", Tier: "SILVER", Terms: "NET30", PriceLevel: "Contractor",
			CreditLimit: 20000, PrimaryBranch: yards["WK"],
		}, priceLevelIDs["Contractor"])
		if err != nil {
			log.Printf("Seed: Quillfield Formwork: %v", err)
		} else {
			seedExec(db, "Quillfield ship to", `INSERT INTO customer_ship_tos (customer_id, code, name, line1, is_default)
				VALUES ($1, 'MAIN', 'Quillfield Formwork Ltd', '610 Plywood Ct, West Kelowna BC', TRUE)
				ON CONFLICT (customer_id, code) DO NOTHING`, cid)
			var invCount int
			db.QueryRow(`SELECT COUNT(*) FROM invoices WHERE customer_id = $1`, cid).Scan(&invCount)
			if invCount == 0 {
				orderID, invID := uuid.New(), uuid.New()
				on := seedNow().AddDate(0, 0, -12)
				seedExec(db, "Quillfield order", `INSERT INTO orders (id, customer_id, branch_id, total_amount, subtotal, status, created_at, delivery_type, currency, tax_source)
					VALUES ($1, $2, $3, 18816.00, 16800.00, 'FULFILLED', $4, 'PICKUP',
						COALESCE((SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD'), 'LEGACY')`, orderID, cid, branch, on)
				seedExec(db, "Quillfield invoice", `INSERT INTO invoices (id, order_id, customer_id, branch_id, status, total_amount, subtotal, tax_rate, tax_amount,
						due_date, payment_terms_id, invoice_date, created_at)
					VALUES ($1, $2, $3, $4, 'UNPAID', 18816.00, 16800.00, 0.12, 2016.00, $5::date,
						(SELECT payment_terms_id FROM customers WHERE id = $3), $6::date, $6)`,
					invID, orderID, cid, branch, on.AddDate(0, 1, 0), on.AddDate(0, 0, 1))
				// The two lines the subtotal is made of, in the shared line shape
				// seed.go writes, billed on the invoice. Both products or neither:
				// never a document whose lines disagree with its subtotal.
				lines := []struct {
					SKU   string
					Qty   float64
					Price float64
				}{{"PLY-34-CDX", 400, 36.00}, {"LUM-21012-NO2", 100, 24.00}}
				ready := true
				for _, l := range lines {
					if _, ok := skuToID[l.SKU]; !ok {
						ready = false
						log.Printf("Seed: Quillfield Formwork lines not written: product %s is missing", l.SKU)
					}
				}
				if ready {
					for j, l := range lines {
						seedExec(db, "Quillfield order line "+l.SKU, `INSERT INTO order_lines (order_id, product_id, quantity, unit_price, line_type, position,
								description, sku, uom, price_uom, uom_qty, price_uom_qty, priced_unit_price, price_source, line_total, taxable,
								quantity_allocated, quantity_fulfilled)
							SELECT $1, p.id, $3, $4, 'PRODUCT', $5,
								COALESCE(p.description, ''), COALESCE(p.sku, ''), p.uom_primary::text, p.uom_primary::text, 1, 1, $4, 'PRICE_LIST', ROUND($3::numeric * $4::numeric, 2), TRUE,
								0, $3
							FROM products p WHERE p.id = $2`, orderID, skuToID[l.SKU], l.Qty, l.Price, j)
					}
					seedExec(db, "Quillfield invoice lines", `INSERT INTO invoice_lines (invoice_id, order_line_id, product_id, line_type, position, description, sku,
							quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, price_each, price_source, line_total, taxable)
						SELECT $1, ol.id, ol.product_id, ol.line_type, ol.position, ol.description, ol.sku,
							ol.quantity, ol.uom, ol.price_uom, ol.uom_qty, ol.price_uom_qty, ol.unit_price, ol.unit_price, ol.price_source, ol.line_total, ol.taxable
						FROM order_lines ol WHERE ol.order_id = $2 AND ol.line_type = 'PRODUCT'`, invID, orderID)
				}
				ar.post(ctx, []seedInvoice{{ID: invID, CustomerID: cid, BranchID: branch, Date: on.AddDate(0, 0, 1), Subtotal: 1680000, Tax: 201600}})
			}
		}
	}

	// A purchase order sent to a vendor and ready to receive at the main yard.
	if vid, ok := vendorIDs["Fernbank Hardware Distribution"]; ok && kel != uuid.Nil {
		var po string
		db.QueryRow(`INSERT INTO purchase_orders (vendor_id, branch_id, status) VALUES ($1, $2, 'SENT') RETURNING id`, vid, kel).Scan(&po)
		if po != "" {
			for _, line := range []struct {
				SKU  string
				Qty  float64
				Cost float64
			}{{"HANGER-26", 400, 0.80}, {"HANGER-28", 300, 0.95}} {
				if pid, ok := skuToID[line.SKU]; ok {
					seedExec(db, "purchase order line "+line.SKU, `INSERT INTO purchase_order_lines (po_id, description, quantity, cost, product_id, qty_received)
						SELECT $1, p.description, $2, $3, p.id, 0 FROM products p WHERE p.id = $4`, po, line.Qty, line.Cost, pid)
				}
			}
		}
	}

	// A few customers trade at a second yard through documents of their own:
	// an open quote there and the customer_branches row with its audit row, the
	// shape the link follows trade rule writes.
	crossYard := []struct{ Customer, Yard, SKU string }{
		{"Tarnwick Construction", "WK", "LUM-2610-PREM"},
		{"Thornmere Homes Ltd", "LK", "LUM-2612-PREM"},
		{"Alderfold Builders", "KEL-MAIN", "LUM-448-PT"},
	}
	for _, c := range crossYard {
		cid, ok := customerIDs[c.Customer]
		y, yok := yards[c.Yard]
		pid, pok := skuToID[c.SKU]
		if !ok || !yok || !pok || custToBranch[cid] == y {
			continue
		}
		var qid string
		db.QueryRow(`INSERT INTO quotes (customer_id, branch_id, state, total_amount, expires_at)
			VALUES ($1, $2, 'SENT', 0, NOW() + interval '14 days') RETURNING id`, cid, y).Scan(&qid)
		if qid == "" {
			continue
		}
		seedExec(db, "cross yard quote line "+c.Customer, `INSERT INTO quote_lines (quote_id, product_id, sku, description, quantity, uom, unit_price, line_total)
			SELECT $1, p.id, p.sku, p.description, 40, p.uom_primary::text, p.base_price, ROUND(40 * p.base_price, 2)
			FROM products p WHERE p.id = $2`, qid, pid)
		seedExec(db, "cross yard quote total "+c.Customer, `UPDATE quotes SET total_amount = (SELECT COALESCE(SUM(line_total), 0) FROM quote_lines WHERE quote_id = $1) WHERE id = $1`, qid)
		var linked bool
		db.QueryRow(`WITH ins AS (INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)
				ON CONFLICT DO NOTHING RETURNING 1) SELECT EXISTS (SELECT 1 FROM ins)`, cid, y).Scan(&linked)
		if linked {
			seedExec(db, "cross yard link audit "+c.Customer, `INSERT INTO audit_log (action, entity_type, entity_id, user_id, changes, actor_kind, actor_id)
				VALUES ('customer.branch_linked', 'customer', $1, 'system',
					jsonb_build_object('branch_id', $2::text, 'document_type', 'quote', 'document_id', $3::text, 'source', 'demo seed'),
					'user', 'system')`, cid, y, qid)
		}
	}
	fmt.Println("Seed: multi yard world (personas, yard registers, main yard only stock, a PO to receive, cross yard quotes)")
}
