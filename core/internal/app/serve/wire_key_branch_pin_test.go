// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The branch bound machine key's pin (ADR 0007 section 5.5) through serve's
// real mount methods and the real machine key core: one key minted bound to
// branch A, one unbound key, and a user, against every route the sweep found
// writing or reading another branch's rows without the branch middleware.
// Only authentication is a stand-in: the machine key core is the real one
// over a real minted key, and the user arrives as the claims a JWT would
// carry. A route that loses its hold on the pin fails here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/ap"
	"github.com/gablelbm/gable/internal/app/orderwire"
	"github.com/gablelbm/gable/internal/bankrecon"
	"github.com/gablelbm/gable/internal/chargecode"
	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/crm"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/customer/customeraudit"
	"github.com/gablelbm/gable/internal/dashboard"
	"github.com/gablelbm/gable/internal/dealerprofile"
	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/drafts"
	"github.com/gablelbm/gable/internal/edi"
	"github.com/gablelbm/gable/internal/emaildelivery"
	"github.com/gablelbm/gable/internal/events"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/governance"
	glint "github.com/gablelbm/gable/internal/integrations/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/links"
	"github.com/gablelbm/gable/internal/location"
	"github.com/gablelbm/gable/internal/matching"
	"github.com/gablelbm/gable/internal/millwork"
	"github.com/gablelbm/gable/internal/notification"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/parsing"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pim"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/internal/salesteam"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/internal/unit"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/gablelbm/gable/internal/vision"
	"github.com/gablelbm/gable/pkg/apps"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// keyPinFixture is two branches, one yard per branch for the location rows,
// two spare branches for the archive arms, a quote per branch for the
// exposure routes, and the serve mounts the pin must hold on: the location
// routes through branchWall.locations and the exposure routes through
// registerExposureRoutes, both behind the real machine key core.
type keyPinFixture struct {
	t           *testing.T
	db          *database.DB
	srv         *httptest.Server
	branchA     uuid.UUID
	branchB     uuid.UUID
	branchC     uuid.UUID
	branchC2    uuid.UUID
	yardA       uuid.UUID
	yardB       uuid.UUID
	quoteA      uuid.UUID
	quoteB      uuid.UUID
	custID      uuid.UUID
	yardNull    uuid.UUID // a legacy row that names no branch
	custB       uuid.UUID // a customer whose branches hold branch B only
	category    uuid.UUID // one product category the priced rules hang on
	catRuleA    uuid.UUID // a category rule scoped to the pin's customer
	catRuleB    uuid.UUID // a category rule scoped to the foreign customer
	catRuleT    uuid.UUID // a tier category rule that names no customer
	taxExempt   uuid.UUID // a tax exemption row of the foreign customer
	jobA        uuid.UUID // a project of the pin's customer
	jobB        uuid.UUID // a project of the foreign customer
	regA        string    // a POS register per branch, on the branch's yard
	regB        string
	tillA       uuid.UUID // an OPEN till session per register
	tillB       uuid.UUID
	txnA        uuid.UUID // an OPEN POS transaction per branch
	txnB        uuid.UUID
	lineA       uuid.UUID // one line item on each transaction
	lineB       uuid.UUID
	retA        uuid.UUID // a completed POS return per branch
	retB        uuid.UUID
	zrA         uuid.UUID // a frozen Z report per branch's session
	zrB         uuid.UUID
	posProd     uuid.UUID          // the product the POS lines carry
	bound       string             // the key pinned to branch A
	unbound     string             // the same scopes, no pin
	bound2      string             // a second bound key holding the document family scopes the mint cap keeps off the first
	unbound2    string             // the same scopes, no pin
	boundKeyID  string             // the bound key's id, the actor its refusal audit rows name
	bound2KeyID string             // the second bound key's id
	boundFirst  string             // the first mint's bound key, kept when a test swaps in the second
	recording   bool               // mark every bound key call for the walled route coverage test
	keys        *techadmin.Service // the mint the fixture's keys validate against
}

// mintWithScopes mints a key with the scopes given, through the same service
// the fixture's machine key core validates against, and removes it when the
// test ends.
func (f *keyPinFixture) mintWithScopes(t *testing.T, name string, scopes []string, branch *uuid.UUID) string {
	t.Helper()
	raw, key, err := f.keys.GenerateKey(context.Background(), name, scopes, branch)
	if err != nil {
		t.Fatalf("mint %s: %v", name, err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1::text`, key.ID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'key.created' AND entity_id = $1`, key.ID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, key.ID)
	})
	return raw
}

// pinKeyScopes opens every route under test: the location writes, the branch
// directory, the user grants, the exposure surface, the dealer wide routes,
// the staff roster and the customer scoped pricing and tax surface.
// pinDocumentScopes is the second mint's scope list: the document families
// (customers, contacts, ship-tos, invoices, payments, credit memos, orders,
// drafts, dashboard, inventory, locations), kept off the first mint only
// because the mint caps a key at 64 scopes and the two lists together exceed
// it.
var pinDocumentScopes = []string{
	"customers:read", "customers:write", "contacts:read", "contacts:write",
	"ship-tos:read", "ship-tos:write", "invoices:read", "invoices:write",
	"payments:read", "payments:write", "credit-memos:read", "credit-memos:write",
	"orders:read", "orders:write", "orders:propose", "orders:commit", "quotes:propose", "quotes:commit",
	"dashboard:read", "inventory:read", "inventory:write", "locations:read", "locations:write",
	"products:read", "branches:read", "documents:read", "activities:read", "activities:write", "ar:read", "ar:write",
	"matching:read", "matching:write", "delivery:read", "delivery:write",
	"purchase-orders:read", "purchase-orders:write", "quotes:read", "quotes:write",
	"email-deliveries:read", "dealer-profile:read", "dealer-profile:write",
}

var pinKeyScopes = []string{
	"locations:write", "branches:read", "branches:write",
	"users:read", "users:grants", "quotes:read", "quotes:write", "admin:write",
	"admin:staff", "reports:read", "reporting:read", "reporting:write", "events:read",
	"gl:read", "gl:write", "ap:read", "ap:write", "bankrecon:read", "bankrecon:write", "sales-team:read",
	"market-indices:read", "market-indices:write", "tax:read", "tax:write", "pricing:read", "pricing:write",
	"pos:read", "pos:write", "accounts:read", "ar:read",
	"products:read", "products:write", "charge-codes:read", "charge-codes:write",
	"payment-terms:read", "payment-terms:write", "purchase-orders:read", "purchase-orders:write",
	"edi:read", "edi:write", "vendors:read", "vendors:write", "units:read", "units:write",
	"millwork:read", "millwork:write", "governance:read", "governance:write",
	"matching:read", "matching:write", "delivery:read", "delivery:write",
	"apps:read", "apps:write", "admin:settings", "admin:modules",
	"parsing:write", "vision:write",
}

func newKeyPinFixture(t *testing.T) *keyPinFixture {
	t.Helper()
	return newKeyPinFixtureSwitched(t, "true")
}

// newKeyPinFixtureSwitched builds the fixture with the multi_branch_enabled
// switch in the state given: the branch middleware reads it once at
// construction, so the state has to be in the table before the wall is
// built.
func newKeyPinFixtureSwitched(t *testing.T, switchState string) *keyPinFixture {
	t.Helper()
	testutil.LockOutboxTables(t) // the exposure writes record outbox events
	db := testutil.RequireDB(t)
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	f := &keyPinFixture{t: t, db: db,
		branchA: uuid.New(), branchB: uuid.New(), branchC: uuid.New(), branchC2: uuid.New(),
		yardA: uuid.New(), yardB: uuid.New(), quoteA: uuid.New(), quoteB: uuid.New(), custID: uuid.New(),
		custB: uuid.New(), category: uuid.New(), catRuleA: uuid.New(), catRuleB: uuid.New(), taxExempt: uuid.New(),
		yardNull: uuid.New(), catRuleT: uuid.New(), jobA: uuid.New(), jobB: uuid.New(),
		tillA: uuid.New(), tillB: uuid.New(), txnA: uuid.New(), txnB: uuid.New(), lineA: uuid.New(), lineB: uuid.New(),
		retA: uuid.New(), retB: uuid.New(), zrA: uuid.New(), zrB: uuid.New(), posProd: uuid.New(),
		regA: "PINREG-A-" + uuid.NewString()[:8], regB: "PINREG-B-" + uuid.NewString()[:8],
	}
	for _, r := range []struct {
		id     uuid.UUID
		typ    string
		parent any
	}{{f.branchA, "BRANCH", nil}, {f.branchB, "BRANCH", nil}, {f.branchC, "BRANCH", nil}, {f.branchC2, "BRANCH", nil},
		{f.yardA, "YARD", f.branchA}, {f.yardB, "YARD", f.branchB}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, name, parent_id) VALUES ($1, $2, $3, $4, $5)`,
			r.id, r.typ, "pin-"+r.id.String()[:8], "pin row "+r.id.String()[:8], r.parent); err != nil {
			t.Fatalf("seed location: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'pin cust', $2, $3)`, f.custID, "PIN-"+f.custID.String()[:8], f.branchA); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	for _, q := range []struct{ id, branch uuid.UUID }{{f.quoteA, f.branchA}, {f.quoteB, f.branchB}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO quotes (id, number, customer_id, state, branch_id, total_amount)
			VALUES ($1, $2, $3, 'DRAFT', $4, 10)`, q.id, "PINQ-"+q.id.String()[:8], f.custID, q.branch); err != nil {
			t.Fatalf("seed quote: %v", err)
		}
	}
	// A user granted both branches: the read narrowing keeps the pin's branch
	// only for the bound key and both for everyone else.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by)
		VALUES ('pin-both', $1, TRUE, 'test'), ('pin-both', $2, FALSE, 'test')`, f.branchA, f.branchB); err != nil {
		t.Fatalf("seed grants: %v", err)
	}
	// The customer confinement rows: a customer per branch with its
	// customer_branches row, a product category, one priced category rule per
	// customer, and a tax exemption of the foreign customer.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'pin cust B', $2, $3)`, f.custB, "PINB-"+f.custB.String()[:8], f.branchB); err != nil {
		t.Fatalf("seed customer B: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2), ($3, $4)`,
		f.custID, f.branchA, f.custB, f.branchB); err != nil {
		t.Fatalf("seed customer branches: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO product_categories (id, name, slug, path)
		VALUES ($1, 'pin cat', $2, $3)`, f.category, "pin-cat-"+f.category.String()[:8], "pin."+f.category.String()[:8]); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	for _, r := range []struct{ id, customer uuid.UUID }{{f.catRuleA, f.custID}, {f.catRuleB, f.custB}} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO category_pricing_rules (id, target_type, customer_id, category_id, rule_type, rule_value, created_by)
			VALUES ($1, 'ACCOUNT', $2, $3, 'MARKUP', 10, 'test')`, r.id, r.customer, f.category); err != nil {
			t.Fatalf("seed category rule: %v", err)
		}
	}
	// A tier rule names no customer: it prices every branch's customers, so it
	// is a dealer wide write a bound key is refused.
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO category_pricing_rules (id, target_type, tier, category_id, rule_type, rule_value, created_by)
		VALUES ($1, 'TIER', 'CONTRACTOR', $2, 'MARKUP', 10, 'test')`, f.catRuleT, f.category); err != nil {
		t.Fatalf("seed tier rule: %v", err)
	}
	// A project (a job) belongs to exactly one customer: a pricing rule that
	// names a job prices that customer, so the job resolves to its customer.
	for _, j := range []struct{ id, customer uuid.UUID }{{f.jobA, f.custID}, {f.jobB, f.custB}} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, 'pin job', 'Active')`, j.id, j.customer); err != nil {
			t.Fatalf("seed project: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO tax_exemptions (id, customer_id, exempt_reason, certificate_number, issuing_state)
		VALUES ($1, $2, 'pin', 'PIN-CERT-1', 'WA')`, f.taxExempt, f.custB); err != nil {
		t.Fatalf("seed tax exemption: %v", err)
	}
	// A legacy row that names no branch: the denorm trigger refuses such rows
	// on an insert, but it fires only on INSERT and UPDATE OF parent_id and
	// type, so the seed inserts a normal yard and then nulls its branch with
	// an update the trigger never sees (no table level trigger lifting, which
	// would race the packages that run beside this one).
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, name, parent_id) VALUES ($1, 'YARD', $2, 'pin null row', $3)`,
		f.yardNull, "pin-null-"+f.yardNull.String()[:8], f.branchA); err != nil {
		t.Fatalf("seed null branch row: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET branch_id = NULL WHERE id = $1`, f.yardNull); err != nil {
		t.Fatalf("null the branch: %v", err)
	}
	// The POS twins: a register on each branch's yard, an OPEN till session,
	// an OPEN transaction with one line, a completed return and a frozen
	// Z report, so the by id pos routes act on real records of each branch.
	cashier := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'pin pos item', 'EA', 1)`,
		f.posProd, "PINP-"+f.posProd.String()[:8]); err != nil {
		t.Fatalf("seed pos product: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO pos_registers (id, location_id, branch_id, name) VALUES ($1, $2, $3, 'pin reg A'), ($4, $5, $6, 'pin reg B')`,
		f.regA, f.yardA, f.branchA, f.regB, f.yardB, f.branchB); err != nil {
		t.Fatalf("seed registers: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO till_sessions (id, register_id, branch_id, cashier_id, status, opening_float)
		VALUES ($1, $2, $3, $5, 'OPEN', 50), ($6, $4, $7, $5, 'OPEN', 50)`,
		f.tillA, f.regA, f.branchA, f.regB, cashier, f.tillB, f.branchB); err != nil {
		t.Fatalf("seed till sessions: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO pos_transactions (id, register_id, cashier_id, status, branch_id, till_session_id, currency)
		VALUES ($1, $2, $4, 'OPEN', $5, $6, 'USD'), ($3, $7, $4, 'OPEN', $8, $9, 'USD')`,
		f.txnA, f.regA, f.txnB, cashier, f.branchA, f.tillA, f.regB, f.branchB, f.tillB); err != nil {
		t.Fatalf("seed pos transactions: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO pos_line_items (id, transaction_id, product_id, description, quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, line_total, line_type, price_source)
		VALUES ($1, $2, $4, 'pin', 1, 'EA', 'EA', 1, 1, 1, 1, 'PRODUCT', 'PRICE_LIST'), ($3, $5, $4, 'pin', 1, 'EA', 'EA', 1, 1, 1, 1, 'PRODUCT', 'PRICE_LIST')`,
		f.lineA, f.txnA, f.lineB, f.posProd, f.txnB); err != nil {
		t.Fatalf("seed pos lines: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO pos_returns (id, register_id, branch_id, cashier_id, refund_method, status, currency, number)
		VALUES ($1, $2, $3, $5, 'CASH', 'COMPLETED', 'USD', $8), ($6, $4, $7, $5, 'CASH', 'COMPLETED', 'USD', $9)`,
		f.retA, f.regA, f.branchA, f.regB, cashier, f.retB, f.branchB, "RTPIN-"+f.retA.String()[:8], "RTPIN-"+f.retB.String()[:8]); err != nil {
		t.Fatalf("seed pos returns: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO till_z_reports (id, till_session_id, register_id, branch_id, over_short, payload)
		VALUES ($1, $2, $3, $4, 0, '{}'), ($5, $6, $7, $8, 0, '{}')`,
		f.zrA, f.tillA, f.regA, f.branchA, f.zrB, f.tillB, f.regB, f.branchB); err != nil {
		t.Fatalf("seed z reports: %v", err)
	}
	t.Cleanup(func() {
		purgeFixtureRows(ctx, db, []uuid.UUID{f.custID, f.custB}, []uuid.UUID{f.branchA, f.branchB, f.branchC, f.branchC2})
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE account_number LIKE 'PINNEW-%'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote_exposure_event'
			AND entity_id IN (SELECT id::text FROM quote_exposure_events WHERE quote_id IN ($1, $2))`, f.quoteA, f.quoteB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN ($1, $2)`, f.quoteA, f.quoteB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quote_exposure_events WHERE quote_id IN ($1, $2)`, f.quoteA, f.quoteB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE id IN ($1, $2)`, f.quoteA, f.quoteB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM tax_exemptions WHERE customer_id IN ($1, $2) OR id = $3`, f.custID, f.custB, f.taxExempt)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'category_rule'
			AND entity_id IN (SELECT id::text FROM category_pricing_rules WHERE customer_id IN ($1, $2) OR tier = 'PIN-TIER')`, f.custID, f.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'pricing_rule'
			AND entity_id IN (SELECT id::text FROM pricing_rules WHERE customer_id IN ($1, $2) OR job_id IN ($3, $4)
			OR (customer_id IS NULL AND job_id IS NULL AND name LIKE 'pin %'))`, f.custID, f.custB, f.jobA, f.jobB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM category_pricing_audit WHERE rule_id IN (SELECT id FROM category_pricing_rules WHERE customer_id IN ($1, $2) OR tier = 'PIN-TIER')`, f.custID, f.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM category_pricing_rules WHERE customer_id IN ($1, $2) OR id = $3 OR tier = 'PIN-TIER'`, f.custID, f.custB, f.catRuleT)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pricing_rules WHERE customer_id IN ($1, $2) OR job_id IN ($3, $4) OR (customer_id IS NULL AND job_id IS NULL AND name LIKE 'pin %')`, f.custID, f.custB, f.jobA, f.jobB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM projects WHERE id IN ($1, $2)`, f.jobA, f.jobB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_categories WHERE id = $1`, f.category)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id IN ($1, $2)`, f.custID, f.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id IN ($1, $2)`, f.custID, f.custB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub LIKE 'pin-%' OR user_sub = 'pin-both'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE code LIKE 'pin-new-%'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM till_z_reports WHERE id IN ($1, $2)`, f.zrA, f.zrB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_return_lines WHERE return_id IN ($1, $2)`, f.retA, f.retB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_returns WHERE id IN ($1, $2)`, f.retA, f.retB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_tenders WHERE transaction_id IN ($1, $2)`, f.txnA, f.txnB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_line_items WHERE transaction_id IN ($1, $2)`, f.txnA, f.txnB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id IN ($1, $2)`, f.txnA, f.txnB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM till_sessions WHERE id IN ($1, $2)`, f.tillA, f.tillB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_registers WHERE id IN ($1, $2)`, f.regA, f.regB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, f.posProd)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.posProd)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE parent_id IN ($1, $2, $3, $4)`, f.branchA, f.branchB, f.branchC, f.branchC2)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2, $3, $4, $5, $6, $7)`,
			f.branchA, f.branchB, f.branchC, f.branchC2, f.yardA, f.yardB, f.yardNull)
	})

	setSetting(t, db, "multi_branch_enabled", switchState)
	setSetting(t, db, "default_branch_required", "false")
	wall := newBranchWall(db)
	mux := http.NewServeMux()
	wall.locations(mux, location.NewHandler(location.NewService(location.NewRepository(db)),
		location.NewUserRepository(db), middleware.RequireRole("admin", "owner")).
		WithAuditLog(audit.NewLogger(db)))

	// The exposure surface exactly as wireExposure registers it (the index
	// admin surface with the market index refresh comes with it).
	escRepo := pricing.NewEscalatorRepository(db)
	exposureRepo := pricing.NewExposureRepository(db)
	quoteRepo := quote.NewRepository(db)
	exposureAudit := &exposureAuditAdapter{auditLog: audit.NewLogger(db)}
	registerExposureRoutes(mux, exposureRoutes{
		Scanner: pricing.NewExposureScanner(exposureRepo, escRepo, quoteRepo, exposureAudit, db, slog.Default()).
			WithOutbox(outbox.NewWriter(db, "")),
		Checker:  pricing.NewExposureChecker(db),
		Exposure: exposureRepo,
		Service: pricing.NewExposureService(exposureRepo, escRepo, quoteRepo, exposureAudit, pricing.NewExposureChecker(db), slog.Default()).
			WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db),
		Escalators: escRepo,
		DB:         db,
		Logger:     slog.Default(),
		AuditLog:   audit.NewLogger(db),
		MW:         wall.mw,
		Guard:      wall.guard,
	})

	// The dealer wide modules exactly as serve registers them: the reporting
	// surface in its three registrations, the events feed, the GL, AP and bank
	// reconciliation books and the sales team reads. A branch bound key is
	// refused them by the route allowlist gate in the chain, not by these
	// mounts.
	reportingHandler := reporting.NewHandler(reporting.NewService(reporting.NewRepository(db)))
	wall.reporting(mux, reportingHandler)
	wall.reportingSchedules(mux, reportingHandler, nil)
	wall.reportingBIIntegration(mux, reportingHandler)
	wall.eventsFeed(mux, events.NewHandler(db))
	glSvc := gl.NewService(gl.NewRepository(db), glint.NewMockGLAdapter(), slog.Default())
	gl.NewHandler(glSvc).RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	ap.NewHandler(ap.NewService(db, ap.NewRepository(db), glSvc, slog.Default()).WithBranchGuard(wall.guard)).
		RegisterRoutes(mux, wall.scoped("admin", "owner", "finance"))
	bankrecon.NewHandler(bankrecon.NewService(db, bankrecon.NewRepository(db), glSvc, slog.Default())).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "finance"))
	salesteam.NewHandler(salesteam.NewRepository(db)).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "sales"))

	// The staff roster surface exactly as serve registers it.
	wireStaffAdmin(mux, db, audit.NewLogger(db), "")

	// The customer confined modules exactly as serve registers them: the tax
	// exemption writes and the customer priced rules.
	customerSvc := customer.NewService(customer.NewRepository(db)).WithAudit(customeraudit.New(audit.NewLogger(db)))
	productSvc := product.NewService(product.NewRepository(db))
	tax.NewHandler(orderwire.NewTaxService(db, &config.Config{}, slog.Default())).
		RegisterRoutes(mux, taxCustomerWall(wall, db, "admin", "owner", "finance"))
	pricing.NewHandler(pricing.NewService(pricing.NewRepository(db)).WithTxRunner(db), customerSvc, productSvc).
		RegisterRoutes(mux, pricingRulesCustomerWall(wall.keyWall, db, "admin", "owner"))
	pricing.NewCategoryHandler(
		pricing.NewCategoryPricingService(pricing.NewCategoryRepository(db)).WithTxRunner(db).
			WithOutbox(outbox.NewWriter(db, "")).
			WithAudit(pricingAuditAdapter{l: audit.NewLogger(db)}),
		customerSvc).RegisterCategoryRoutes(mux, categoryRulesCustomerWall(wall.keyWall, db, "admin", "owner"))

	// The account module exactly as serve registers it: the summary, the
	// subledger and the statement name a customer in the path, and the aging
	// and reconciliation reads come with the same mount.
	accountSvc := account.NewService(db, glSvc, slog.Default())
	wall.accounts(mux, accountSvc)

	// The POS module exactly as serve registers it: the scoped role guard
	// plus the branch middleware, and the repositories hold the pin on the
	// till, Z report, return and line item rows.
	invoiceSvc := invoice.NewService(invoice.NewRepository(db), glSvc, accountSvc, db).
		WithAuditLog(audit.NewLogger(db))
	paymentSvc := payment.NewService(db, payment.NewRepository(db), accountSvc).
		WithAuditLog(audit.NewLogger(db))
	posSvc := pos.NewService(db, pos.NewRepository(db), slog.Default()).
		WithInvoices(invoiceSvc).WithAR(accountSvc).
		WithInventory(inventory.NewService(inventory.NewRepository(db))).
		WithLedger(glSvc).WithAuditLog(audit.NewLogger(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	wall.pos(mux, pos.NewHandler(posSvc), "admin", "owner", "cashier")

	// The AI spending routes exactly as serve registers them, each call
	// audited with whoever spent the credit (the PD-6 ruling).
	parsing.NewHandler(parsing.NewService(product.NewRepository(db), nil)).
		WithAuditLog(audit.NewLogger(db)).RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "sales"))
	vision.NewHandler(vision.NewService()).WithAuditLog(audit.NewLogger(db)).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))

	// The dealer wide catalogue and configuration modules exactly as serve
	// registers them: products, PIM, the unit catalogue, the payment terms on
	// the customer handler, vendors, EDI partners, charge codes, rebate
	// programs, market indices, millwork, governance, the delivery fleet, the
	// matching config, the techadmin settings and app switches, and the
	// purchase orders (the reorder refresh among them).
	wall.products(mux, product.NewHandler(product.NewService(product.NewRepository(db))))
	pim.NewHandler(pim.NewService(pim.NewRepository(db), productSvc)).RegisterRoutes(mux, wall.scoped("admin", "owner"))
	unit.NewHandler(unit.NewService(unit.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "sales", "warehouse", "finance", "cashier", "purchasing"),
			middleware.RequireRole("admin", "owner"))
	wall.customers(mux, customerSvc.WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db))
	vendor.NewHandler(vendor.NewService(vendor.NewRepository(db))).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "purchasing"))
	edi.NewEDIHandler(edi.NewEDIRepository(db), edi.NewBuyingGroupService(slog.Default()), edi.NewService("", slog.Default())).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	wall.chargeCodes(mux, chargecode.NewService(chargecode.NewRepository(db)))
	pricing.NewRebateHandler(pricing.NewRebateService(pricing.NewRebateRepository(db))).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	pricing.NewEscalatorHandler(pricing.NewEscalatorService(pricing.NewEscalatorRepository(db))).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	millwork.NewHandler(millwork.NewService(millwork.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAudit(audit.NewLogger(db))).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "sales"))
	governance.NewHandler(governance.NewService(governance.NewRepository(db), governance.NewTemplateAIProvider()).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAuditLog(audit.NewLogger(db))).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	wall.delivery(mux, delivery.NewHandler(delivery.NewService(delivery.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithAudit(audit.NewLogger(db))),
		"admin", "owner", "warehouse", "driver")
	wall.matching(mux, matching.NewService(db, matching.NewRepository(db), pinPOSource{db: db}, pinAPSource{}, slog.Default()))
	apps.NewHandler(apps.NewRegistry(db, slog.Default()).WithAudit(audit.NewLogger(db))).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	poProductSvc := product.NewService(product.NewRepository(db))
	poRecSvc := purchase_order.NewRecommendationService(purchase_order.NewRepository(db),
		inventory.NewService(inventory.NewRepository(db)), poProductSvc,
		vendor.NewService(vendor.NewRepository(db))).
		WithVelocityRepo(purchase_order.NewVelocityRepository(db))
	poSvc := purchase_order.NewService(purchase_order.NewRepository(db), db, nil, nil, poProductSvc, nil).
		WithVelocityRepo(purchase_order.NewVelocityRepository(db)).
		WithBranchGuard(wall.guard)
	wall.purchaseOrders(mux, purchase_order.NewHandler(poSvc, poRecSvc))

	// The branch walled document families exactly as serve registers them:
	// quotes, orders, invoices, payments, crm, documents, drafts, dashboard,
	// inventory and the link resolver, each through its module's wall.
	quoteSvc := quote.NewService(quote.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	wall.quotes(mux, quoteSvc)
	// The order service exactly as serve builds it: the price engine, the tax
	// provider, inventory, the invoice writer and the pre-ship exposure gate.
	orderSvc := orderwire.New(orderwire.Deps{
		DB: db, Config: &config.Config{}, Logger: slog.Default(), AuditLog: audit.NewLogger(db),
		Inventory: inventory.NewService(inventory.NewRepository(db)), Invoices: invoiceSvc, Accounts: accountSvc,
		Pricing:   pricing.NewService(pricing.NewRepository(db)).WithTxRunner(db),
		Customers: customerSvc, Escalators: escRepo, QuoteLines: quoteRepo,
	})
	quoteSvc.WithOrderCreator(orderSvc)
	wall.orders(mux, orderSvc)
	wall.invoices(mux, invoiceSvc.WithOrders(orderSvc).WithStock(inventory.NewService(inventory.NewRepository(db))))
	wall.payments(mux, paymentSvc.WithBranchGuard(wall.guard))
	wall.crm(mux, crm.NewService(crm.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAudit(audit.NewLogger(db)))
	docStore := document.NewStore(db)
	profileSvc := dealerprofile.NewService(dealerprofile.NewRepository(db), db, audit.NewLogger(db))
	deliverySvc := emaildelivery.NewService(emaildelivery.NewRepository(db))
	wall.documents(mux, document.NewHandler(
		document.NewService(product.NewRepository(db)).
			WithProfile(profileSvc).WithYards(docStore).WithJobs(docStore).WithStatements(accountSvc),
		orderSvc, invoiceSvc, customerSvc, notification.NewLogEmailService(slog.Default())).
		WithQuotes(quoteSvc).WithPurchaseOrders(poSvc).WithDeliveries(deliverySvc))
	wall.emailDeliveries(mux, emaildelivery.NewHandler(deliverySvc))
	wall.dealerProfile(mux, dealerprofile.NewHandler(profileSvc))
	quoteKind := quote.NewDraftKind(quoteSvc)
	orderKind := order.NewDraftKind(orderSvc)
	draftsRegistry, derr := drafts.NewRegistry(quoteKind, orderKind)
	if derr != nil {
		t.Fatalf("draft kind registration: %v", derr)
	}
	keys := techadmin.NewService(techadmin.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAuditLog(audit.NewLogger(db))
	f.keys = keys
	draftsRepo := drafts.NewRepository(db)
	draftHub := drafts.NewHub(draftsRepo, 100*time.Millisecond, slog.Default())
	t.Cleanup(draftHub.Stop)
	draftsSvc := drafts.NewService(draftsRepo, draftsRegistry).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAudit(audit.NewLogger(db)).
		WithBranchGuard(wall.guard).WithFeed(draftHub)
	draftsHandler := drafts.NewHandler(draftsSvc).WithFeedHandler(
		drafts.NewFeedHandler(draftsSvc, draftsRepo, draftHub, drafts.FeedSettings{
			Heartbeat: 200 * time.Millisecond, Poll: 100 * time.Millisecond, Batch: 100,
			WriteTimeout: 2 * time.Second, MaxLifetime: 10 * time.Second, Retention: time.Hour,
			MaxStreamsPerPrincipal: 8, MaxStreams: 32,
		}, func(ctx context.Context, keyID uuid.UUID) (bool, error) { return keys.KeyActive(ctx, keyID) }))
	quote.RegisterDraftRoutes(mux, draftsHandler, quoteKind, wall.draftGuard(db, "quotes", quoteKind.Roles()...))
	order.RegisterDraftRoutes(mux, draftsHandler, orderKind, wall.draftGuard(db, "orders", orderKind.Roles()...))
	linksHandler := links.NewHandler(links.Settings{},
		links.Entity{Row: rowOrNone(links.RowFor("quotes", false)), Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			q, err := quoteSvc.GetQuoteByIDOrNumber(ctx, raw)
			if err != nil {
				return uuid.Nil, "", err
			}
			return q.ID, q.Number, nil
		}},
		links.Entity{Row: rowOrNone(links.RowFor("orders", false)), Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			o, err := orderSvc.GetOrderByIDOrNumber(ctx, raw)
			if err != nil {
				return uuid.Nil, "", err
			}
			return o.ID, o.Number, nil
		}},
		links.Entity{Row: rowOrNone(links.RowFor("invoices", false)), Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			inv, err := invoiceSvc.GetInvoiceByIDOrNumber(ctx, raw)
			if err != nil {
				return uuid.Nil, "", err
			}
			return inv.ID, inv.Number, nil
		}},
		links.Entity{Row: rowOrNone(links.RowFor("customers", false)), Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			c, err := customerSvc.Get(ctx, uuid.MustParse(raw))
			if err != nil {
				return uuid.Nil, "", err
			}
			return c.ID, "", nil
		}},
		links.Entity{Row: rowOrNone(links.RowFor("products", false)), Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			pc, err := productSvc.GetProduct(ctx, uuid.MustParse(raw))
			if err != nil {
				return uuid.Nil, "", err
			}
			return pc.ID, "", nil
		}},
		links.Entity{Row: rowOrNone(links.RowFor("quotes", true)), Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			d, err := draftsSvc.Get(ctx, "quotes", uuid.MustParse(raw))
			if err != nil {
				return uuid.Nil, "", err
			}
			return d.ID, "", nil
		}},
		links.Entity{Row: rowOrNone(links.RowFor("orders", true)), Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			d, err := draftsSvc.Get(ctx, "orders", uuid.MustParse(raw))
			if err != nil {
				return uuid.Nil, "", err
			}
			return d.ID, "", nil
		}},
	)
	links.RegisterAll(mux, linksHandler, links.Guards{
		Quotes:      wall.scoped("admin", "owner", "sales"),
		Orders:      wall.scoped("admin", "owner", "sales", "finance"),
		Invoices:    wall.scoped("admin", "owner", "sales", "finance"),
		Customers:   wall.scoped("admin", "owner", "sales"),
		Products:    wall.scoped("admin", "owner", "sales", "warehouse"),
		DraftQuotes: wall.scoped(quoteKind.Roles()...),
		DraftOrders: wall.scoped(orderKind.Roles()...),
	})
	dashboard.NewHandler(dashboard.NewService(dashboard.NewRepository(db))).
		RegisterRoutes(mux, wall.scoped("admin", "owner", "finance"))
	wall.inventory(mux, inventory.NewService(inventory.NewRepository(db)))

	techadmin.NewHandler(keys).RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	mint := func(name string, branch *uuid.UUID) (string, string) {
		t.Helper()
		scopes := pinKeyScopes
		if strings.Contains(name, "documents") {
			scopes = pinDocumentScopes
		}
		raw, key, err := keys.GenerateKey(ctx, name, scopes, branch)
		if err != nil {
			t.Fatalf("mint %s: %v", name, err)
		}
		// A bound key references its branch, so the fixture's branch rows can
		// only be removed once the keys it minted are gone, with the mint's
		// audit row and outbox event.
		t.Cleanup(func() {
			_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1::text`, key.ID)
			_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'key.created' AND entity_id = $1`, key.ID)
			_, _ = db.Pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, key.ID)
		})
		return raw, key.ID.String()
	}
	f.bound, f.boundKeyID = mint("pin bound to A", &f.branchA)
	f.unbound, _ = mint("pin unbound", nil)
	f.bound2, f.bound2KeyID = mint("pin bound to A, documents", &f.branchA)
	f.unbound2, _ = mint("pin unbound, documents", nil)
	mkAuth := middleware.NewMachineKeyAuth(pinKeyValidator{svc: keys}, audit.NewLogger(db), []string{"/api/integration/"}, nil)
	// The production chain, built by the same builder Run uses, so every test
	// here drives the wiring serve serves: the machine key core, the request
	// size limit, the idempotency layer and the mux mounts above. Only the
	// claims a JWT would carry are a stand in, injected outside the chain for
	// the user requests (a keyed request never carries them).
	chain := buildChain(ChainDeps{
		Mux:            mux,
		DB:             db,
		AuditLog:       audit.NewLogger(db),
		MachineKeyAuth: mkAuth,
	})
	f.srv = httptest.NewServer(pinClaims(chain))
	t.Cleanup(f.srv.Close)
	return f
}

// pinClaims injects the claims a JWT would carry only for a request that
// names a role, so a keyed request reaches the role guards with no claims
// (the machine key core's context is what passes it there), exactly as the
// real auth layer arranges.
func pinClaims(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role := r.Header.Get("X-Test-Role"); role != "" {
			claims := &middleware.UserClaims{
				RegisteredClaims: jwt.RegisteredClaims{Subject: r.Header.Get("X-Test-Sub")},
				Role:             role, Roles: []string{role},
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, claims)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// pinPOSource answers the matching module's purchase order seam from the
// table: the branch wall reads the branch, the service reads the record.
type pinPOSource struct {
	db *database.DB
}

func (s pinPOSource) GetPO(ctx context.Context, id uuid.UUID) (*purchase_order.PurchaseOrder, error) {
	var exists bool
	if err := s.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM purchase_orders WHERE id = $1)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("no such purchase order")
	}
	return &purchase_order.PurchaseOrder{ID: id}, nil
}

func (s pinPOSource) GetPOBranch(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
	var branch *uuid.UUID
	err := s.db.Pool.QueryRow(ctx, `SELECT branch_id FROM purchase_orders WHERE id = $1`, id).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return branch, err
}

// pinAPSource is an empty accounts payable: the cases that pass the wall
// answer before any invoice is needed.
type pinAPSource struct{}

func (pinAPSource) ListVendorInvoices(context.Context, *uuid.UUID, string) ([]ap.Invoice, error) {
	return nil, nil
}

func (pinAPSource) GetVendorInvoice(context.Context, uuid.UUID) (*ap.Invoice, error) {
	return nil, nil
}

func (pinAPSource) ApproveInvoice(context.Context, uuid.UUID, uuid.UUID) (*ap.Invoice, error) {
	return nil, nil
}

// pinKeyValidator adapts the techadmin service to the machine key core, the
// same adapter serve uses.
type pinKeyValidator struct {
	svc *techadmin.Service
}

func (v pinKeyValidator) ValidateKey(ctx context.Context, rawKey string) (middleware.KeyPrincipal, error) {
	k, err := v.svc.ValidateKey(ctx, rawKey)
	if err != nil {
		if err == techadmin.ErrInvalidKey {
			return middleware.KeyPrincipal{}, middleware.ErrInvalidMachineKey
		}
		return middleware.KeyPrincipal{}, err
	}
	return middleware.KeyPrincipal{ID: k.ID.String(), Scopes: k.Scopes, BranchID: k.BranchID}, nil
}

// key sends one authenticated machine key request.
func (f *keyPinFixture) key(t *testing.T, raw, method, path, body string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+raw)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, buf
}

// userAs sends one request as the claims a JWT would carry, with the role
// given (the role decides the shared customer restricted field rule).
func (f *keyPinFixture) userAs(t *testing.T, role, method, path, body string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Role", role)
	req.Header.Set("X-Test-Sub", "pin-"+role)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, buf
}

// user sends one request as the claims a JWT would carry.
func (f *keyPinFixture) user(t *testing.T, method, path, body string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Role", "admin")
	req.Header.Set("X-Test-Sub", "pin-boss")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, buf
}

// auditRows counts the pin key's refusal rows: scoped to the bound key's
// own id, so another package's test writing a refusal row for its own key
// into the shared database between two reads cannot look like this key's
// second refusal (the packages run in parallel against one database).
func (f *keyPinFixture) auditRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action = 'key.branch_refused' AND actor_id = $1`,
		f.boundKeyID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// refuseForeign runs one bound key call that must be refused with its audit
// row: 403 forbidden, the pin refusal message, one fresh key.branch_refused.
func (f *keyPinFixture) refuseForeign(t *testing.T, method, path, body string, hdr map[string]string) {
	t.Helper()
	before := f.auditRows(t)
	status, buf := f.key(t, f.bound, method, path, body, hdr)
	f.twinLeg(method, path)
	if status != http.StatusForbidden {
		t.Errorf("bound key %s %s: %d %s, want 403", method, path, status, buf)
		return
	}
	// The pin rule words the walls share: the route gate and the customer
	// branch wall lead with "a branch bound key", the exclusive wall with
	// "this key" (the ADR 0007 section 5.5 lead in, r17 P3-2); each wall's
	// own lead in is asserted where its words matter.
	if !strings.Contains(string(buf), "may act only on its own branch") {
		t.Errorf("bound key %s %s: refusal body does not name the pin rule: %s", method, path, buf)
	}
	if after := f.auditRows(t); after != before+1 {
		t.Errorf("bound key %s %s: key.branch_refused rows %d then %d, want one new row", method, path, before, after)
	}
}

// allow runs one call that must pass (the bound key's own branch, or an
// unbound key, or a user) and returns its status with the body.
func (f *keyPinFixture) allow(t *testing.T, who, method, path, body string, hdr map[string]string, want int) []byte {
	t.Helper()
	var (
		status int
		buf    []byte
	)
	switch who {
	case "bound":
		status, buf = f.key(t, f.bound, method, path, body, hdr)
	case "unbound":
		status, buf = f.key(t, f.unbound, method, path, body, hdr)
	default:
		status, buf = f.user(t, method, path, body, hdr)
	}
	if status != want {
		t.Errorf("%s %s %s: %d %s, want %d", who, method, path, status, buf, want)
	}
	return buf
}

// parity asserts the unbound key and the user keep the same answer on a
// route: the reach they hold wherever the wall refuses the bound key.
func (f *keyPinFixture) parity(t *testing.T, method, path, body string) int {
	t.Helper()
	sk, _ := f.key(t, f.unbound, method, path, body, nil)
	su, _ := f.user(t, method, path, body, nil)
	if sk != su {
		t.Errorf("unbound key %s %s: %d, user: %d, want the same answer", method, path, sk, su)
	}
	return sk
}

// locState reads a location row's revision, name and active flag.
func (f *keyPinFixture) locState(t *testing.T, id uuid.UUID) (revision int64, name string, active bool) {
	t.Helper()
	err := f.db.Pool.QueryRow(context.Background(),
		`SELECT revision, COALESCE(name, ''), active FROM locations WHERE id = $1`, id).
		Scan(&revision, &name, &active)
	if err != nil {
		t.Fatal(err)
	}
	return revision, name, active
}

func (f *keyPinFixture) hasGrant(t *testing.T, sub string, branch uuid.UUID) bool {
	t.Helper()
	var ok bool
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM user_locations WHERE user_sub = $1 AND branch_id = $2)`, sub, branch).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// homeBranch reads the branch a user's is_home grant row names.
func (f *keyPinFixture) homeBranch(t *testing.T, sub string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT branch_id FROM user_locations WHERE user_sub = $1 AND is_home`, sub).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *keyPinFixture) exposureEvents(t *testing.T, quote uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM quote_exposure_events WHERE quote_id = $1`, quote).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// putLocationBody builds a PUT body that keeps the row's code and changes its
// name, with the If-Match the row's current revision demands.
func (f *keyPinFixture) putLocation(t *testing.T, who string, id uuid.UUID, name string, want int) {
	t.Helper()
	rev, code, _ := f.locState(t, id)
	body := fmt.Sprintf(`{"code":%q,"path":"pin","name":%q}`, code, name)
	f.allow(t, who, "PUT", "/api/v1/locations/"+id.String(), body,
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, want)
}

// TestKeyBranchPin_LocationWrites walks the location module's unwalled writes:
// every branch B row the bound key names is a refused 403 with its audit row
// and no write, the key's own branch passes, and the unbound key and the user
// keep their reach.
func TestKeyBranchPin_LocationWrites(t *testing.T) {
	f := newKeyPinFixture(t)
	A, B := f.branchA.String(), f.branchB.String()

	// PUT /locations/{id}: the foreign yard is refused and unchanged, the own
	// yard, the unbound key and the user pass.
	f.refuseForeign(t, "PUT", "/api/v1/locations/"+f.yardB.String(),
		fmt.Sprintf(`{"code":"pin-%s","path":"pin","name":"no"}`, f.yardB.String()[:8]),
		map[string]string{"If-Match": `"1"`})
	if rev, _, active := f.locState(t, f.yardB); rev != 1 || !active {
		t.Errorf("refused PUT changed the foreign yard: revision %d active %v", rev, active)
	}
	f.putLocation(t, "bound", f.yardA, "pin own edit", http.StatusOK)
	f.putLocation(t, "unbound", f.yardB, "pin free edit", http.StatusOK)
	f.putLocation(t, "user", f.yardB, "pin user edit", http.StatusOK)

	// PUT /locations/{id} writes `path`, a materialized breadcrumb with no
	// enforcement (round 5 P3-4 probed whether it can move a row under
	// another branch's tree). It cannot: the row's branch comes from its
	// parent at insert (the denorm trigger fires on parent_id and type,
	// neither writable here) and no branch scoped read resolves a branch from
	// path, so a path naming the foreign branch's breadcrumb leaves the row
	// exactly where it was. This leg pins that.
	revA, codeA, _ := f.locState(t, f.yardA)
	f.allow(t, "bound", "PUT", "/api/v1/locations/"+f.yardA.String(),
		fmt.Sprintf(`{"code":%q,"path":%q,"name":"pin path probe"}`, codeA, B+"/yards/under-B"),
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, revA)}, http.StatusOK)
	var pathBranch uuid.UUID
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT branch_id FROM locations WHERE id = $1`, f.yardA).Scan(&pathBranch); err != nil {
		t.Fatal(err)
	}
	if pathBranch != f.branchA {
		t.Errorf("a path write moved the own yard to branch %s, want it to stay its own", pathBranch)
	}

	// DELETE /locations/{id}: the foreign yard is refused and stays active.
	f.refuseForeign(t, "DELETE", "/api/v1/locations/"+f.yardB.String(), "", map[string]string{"If-Match": `"2"`})
	if _, _, active := f.locState(t, f.yardB); !active {
		t.Errorf("refused DELETE archived the foreign yard")
	}
	rev, _, _ := f.locState(t, f.yardA)
	f.allow(t, "bound", "DELETE", "/api/v1/locations/"+f.yardA.String(), "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, http.StatusNoContent)
	rev, _, _ = f.locState(t, f.yardB)
	f.allow(t, "unbound", "DELETE", "/api/v1/locations/"+f.yardB.String(), "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, http.StatusNoContent)

	// POST /branches: the directory create is refused for a bound key
	// outright (a new branch is outside every pin), and stays open to the
	// unbound key and the user.
	newBranch := fmt.Sprintf(`{"type":"branch","code":"pin-new-%s","name":"pin new"}`, uuid.NewString()[:8])
	f.refuseForeign(t, "POST", "/api/v1/branches", newBranch, nil)
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM locations WHERE code LIKE 'pin-new-%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("refused POST /branches created rows")
	}
	f.allow(t, "unbound", "POST", "/api/v1/branches", newBranch, nil, http.StatusCreated)
	f.allow(t, "user", "POST", "/api/v1/branches",
		fmt.Sprintf(`{"type":"branch","code":"pin-new-%s","name":"pin new"}`, uuid.NewString()[:8]), nil, http.StatusCreated)

	// PUT /branches/{id}: the foreign branch is refused and unchanged, the
	// key's own branch passes, and the unbound key and the user keep theirs.
	f.refuseForeign(t, "PUT", "/api/v1/branches/"+B,
		fmt.Sprintf(`{"code":"pin-%s","path":"pin","name":"no"}`, B[:8]), map[string]string{"If-Match": `"1"`})
	if _, name, _ := f.locState(t, f.branchB); name != "pin row "+B[:8] {
		t.Errorf("refused PUT /branches renamed the foreign branch to %q", name)
	}
	putBranch := func(who string, id uuid.UUID, name string, want int) {
		rev, code, _ := f.locState(t, id)
		f.allow(t, who, "PUT", "/api/v1/branches/"+id.String(),
			fmt.Sprintf(`{"code":%q,"path":"pin","name":%q}`, code, name),
			map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, want)
	}
	putBranch("bound", f.branchA, "pin own branch", http.StatusOK)
	putBranch("unbound", f.branchB, "pin free branch", http.StatusOK)
	putBranch("user", f.branchB, "pin user branch", http.StatusOK)

	// DELETE /branches/{id}: the foreign branch is refused and stays active.
	f.refuseForeign(t, "DELETE", "/api/v1/branches/"+B, "", map[string]string{"If-Match": `"2"`})
	if _, _, active := f.locState(t, f.branchB); !active {
		t.Errorf("refused DELETE /branches archived the foreign branch")
	}
	rev, _, _ = f.locState(t, f.branchC)
	f.allow(t, "unbound", "DELETE", "/api/v1/branches/"+f.branchC.String(), "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, http.StatusNoContent)
	rev, _, _ = f.locState(t, f.branchC2)
	f.allow(t, "user", "DELETE", "/api/v1/branches/"+f.branchC2.String(), "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, http.StatusNoContent)
	rev, _, _ = f.locState(t, f.branchA)
	f.allow(t, "bound", "DELETE", "/api/v1/branches/"+A, "",
		map[string]string{"If-Match": fmt.Sprintf(`"%d"`, rev)}, http.StatusNoContent)
}

// TestKeyBranchPin_UserGrants walks the grant, revoke and home branch routes:
// the bound key names branch B in the body or the path and is refused with
// its audit row and no grant write, its own branch passes, and the unbound
// key and the user keep their reach.
func TestKeyBranchPin_UserGrants(t *testing.T) {
	f := newKeyPinFixture(t)
	A, B := f.branchA.String(), f.branchB.String()

	// Grant: body branch_id.
	f.refuseForeign(t, "POST", "/api/v1/users/pin-bound-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, B), nil)
	if f.hasGrant(t, "pin-bound-grant", f.branchB) {
		t.Errorf("refused grant wrote the foreign grant row")
	}
	f.allow(t, "bound", "POST", "/api/v1/users/pin-bound-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, A), nil, http.StatusNoContent)
	if !f.hasGrant(t, "pin-bound-grant", f.branchA) {
		t.Errorf("own branch grant did not land")
	}
	f.allow(t, "unbound", "POST", "/api/v1/users/pin-free-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, B), nil, http.StatusNoContent)
	f.allow(t, "user", "POST", "/api/v1/users/pin-user-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, B), nil, http.StatusNoContent)

	// Home branch: body branch_id.
	f.refuseForeign(t, "PUT", "/api/v1/users/pin-bound-grant/home-branch", fmt.Sprintf(`{"branch_id":%q}`, B), nil)
	f.allow(t, "bound", "PUT", "/api/v1/users/pin-bound-grant/home-branch", fmt.Sprintf(`{"branch_id":%q}`, A), nil, http.StatusNoContent)
	f.allow(t, "unbound", "PUT", "/api/v1/users/pin-free-grant/home-branch", fmt.Sprintf(`{"branch_id":%q}`, B), nil, http.StatusNoContent)
	f.allow(t, "user", "PUT", "/api/v1/users/pin-user-grant/home-branch", fmt.Sprintf(`{"branch_id":%q}`, B), nil, http.StatusNoContent)

	// A body the handler would read only in part never reaches it: the wall
	// parses the whole body exactly as the handler's own decoder would have to,
	// and a valid object followed by anything at all is refused for the bound
	// key with its audit row and no write. Each body below names the foreign
	// branch in its first value, which is all the handler's json.Decoder reads.
	for _, body := range []string{
		fmt.Sprintf(`{"branch_id":%q} {}`, B),
		fmt.Sprintf(`{"branch_id":%q}xyz`, B),
		fmt.Sprintf(`{"branch_id":%q} 1`, B),
	} {
		f.refuseForeign(t, "POST", "/api/v1/users/pin-trail-grant/branches", body, nil)
		if f.hasGrant(t, "pin-trail-grant", f.branchB) {
			t.Errorf("grant body with trailing data %q wrote the foreign grant row", body)
		}
	}
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by)
		VALUES ('pin-trail-home', $1, TRUE, 'test'), ('pin-trail-home', $2, FALSE, 'test')`, f.branchA, f.branchB); err != nil {
		t.Fatalf("seed trail home grants: %v", err)
	}
	for _, body := range []string{
		fmt.Sprintf(`{"branch_id":%q} {}`, B),
		fmt.Sprintf(`{"branch_id":%q}]`, B),
	} {
		f.refuseForeign(t, "PUT", "/api/v1/users/pin-trail-home/home-branch", body, nil)
		if home := f.homeBranch(t, "pin-trail-home"); home != f.branchA {
			t.Errorf("home branch body with trailing data %q moved the home to %s", body, home)
		}
	}

	// Revoke: path branch_id.
	f.refuseForeign(t, "DELETE", "/api/v1/users/pin-both/branches/"+B, "", nil)
	if !f.hasGrant(t, "pin-both", f.branchB) {
		t.Errorf("refused revoke removed the foreign grant row")
	}
	f.allow(t, "bound", "DELETE", "/api/v1/users/pin-bound-grant/branches/"+A, "", nil, http.StatusNoContent)
	if f.hasGrant(t, "pin-bound-grant", f.branchA) {
		t.Errorf("own branch revoke did not land")
	}
	f.allow(t, "unbound", "DELETE", "/api/v1/users/pin-free-grant/branches/"+B, "", nil, http.StatusNoContent)
	f.allow(t, "user", "DELETE", "/api/v1/users/pin-user-grant/branches/"+B, "", nil, http.StatusNoContent)
}

// TestKeyBranchPin_LocationReads narrows the unwalled reads: the bound key
// reads only its own branch's grant rows, and the unbound key and the user
// keep the unfiltered answers.
func TestKeyBranchPin_LocationReads(t *testing.T) {
	f := newKeyPinFixture(t)
	A, B := f.branchA.String(), f.branchB.String()

	// The branch users read of the foreign branch is refused like a write.
	f.refuseForeign(t, "GET", "/api/v1/branches/"+B+"/users", "", nil)
	f.allow(t, "bound", "GET", "/api/v1/branches/"+A+"/users", "", nil, http.StatusOK)
	f.allow(t, "unbound", "GET", "/api/v1/branches/"+B+"/users", "", nil, http.StatusOK)
	f.allow(t, "user", "GET", "/api/v1/branches/"+B+"/users", "", nil, http.StatusOK)

	// A user's branch list carries only the pin's branch for the bound key,
	// and every granted branch for everyone else.
	read := func(who string) map[string]bool {
		t.Helper()
		buf := f.allow(t, who, "GET", "/api/v1/users/pin-both/branches", "", nil, http.StatusOK)
		var out []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(buf, &out); err != nil {
			t.Fatalf("%s branch list body: %v %s", who, err, buf)
		}
		got := map[string]bool{}
		for _, it := range out {
			got[it.ID] = true
		}
		return got
	}
	if got := read("bound"); got[A] != true || got[B] != false {
		t.Errorf("bound key branch list = %v, want only its own branch", got)
	}
	if got := read("unbound"); !got[A] || !got[B] {
		t.Errorf("unbound key branch list = %v, want both branches", got)
	}
	if got := read("user"); !got[A] || !got[B] {
		t.Errorf("user branch list = %v, want both branches", got)
	}
}

// TestKeyBranchPin_ExposureRoutes holds the pin on the exposure surface: the
// quote of the other branch is refused on every by id route with its audit
// row and no write, the key's own branch's quote passes to the module, the
// dealer wide scan is refused for a bound key, and the unbound key and the
// user keep their reach. AUTH_MODE=dev is the mode where the actor fallback
// lets a keyless caller act, so it is the mode that must hold.
func TestKeyBranchPin_ExposureRoutes(t *testing.T) {
	f := newKeyPinFixture(t)
	qA, qB := f.quoteA.String(), f.quoteB.String()
	ack := `{"method":"VERBAL","notes":"customer acknowledged by phone"}`
	override := `{"notes":"emergency override, owner spoken to"}`

	// Every by id route on the foreign quote: refused, audited, no write.
	for _, c := range []struct{ method, suffix, body string }{
		{"GET", "/exposure", ""},
		{"POST", "/exposure/acknowledge", ack},
		{"POST", "/exposure/request-ack", ""},
		{"POST", "/exposure/override", override},
		{"POST", "/exposure/escalate-now", ""},
	} {
		f.refuseForeign(t, c.method, "/api/v1/quotes/"+qB+c.suffix, c.body, nil)
	}
	if n := f.exposureEvents(t, f.quoteB); n != 0 {
		t.Errorf("refused exposure routes wrote %d events for the foreign quote", n)
	}

	// The own branch's quote passes to the module: the read and the preview
	// answer 200, request ack writes its event, and the two state gated
	// actions reach the module's own refusal (409, exposure already clear),
	// not the wall.
	f.allow(t, "bound", "GET", "/api/v1/quotes/"+qA+"/exposure", "", nil, http.StatusOK)
	f.allow(t, "bound", "POST", "/api/v1/quotes/"+qA+"/exposure/escalate-now", "", nil, http.StatusOK)
	f.allow(t, "bound", "POST", "/api/v1/quotes/"+qA+"/exposure/request-ack", "", nil, http.StatusOK)
	if n := f.exposureEvents(t, f.quoteA); n != 1 {
		t.Errorf("own quote request ack wrote %d events, want 1", n)
	}
	f.allow(t, "bound", "POST", "/api/v1/quotes/"+qA+"/exposure/acknowledge", ack, nil, http.StatusConflict)
	f.allow(t, "bound", "POST", "/api/v1/quotes/"+qA+"/exposure/override", override, nil, http.StatusConflict)

	// The dealer wide scan is refused for a bound key; the unbound key and
	// the user keep it.
	f.refuseForeign(t, "POST", "/api/v1/admin/exposure-scan", "", nil)
	f.allow(t, "unbound", "POST", "/api/v1/admin/exposure-scan", "", nil, http.StatusOK)
	f.allow(t, "user", "POST", "/api/v1/admin/exposure-scan", "", nil, http.StatusOK)

	// The unbound key and the user keep the foreign quote's surface.
	f.allow(t, "unbound", "GET", "/api/v1/quotes/"+qB+"/exposure", "", nil, http.StatusOK)
	f.allow(t, "user", "GET", "/api/v1/quotes/"+qB+"/exposure", "", nil, http.StatusOK)
	f.allow(t, "unbound", "POST", "/api/v1/quotes/"+qB+"/exposure/request-ack", "", nil, http.StatusOK)
	f.allow(t, "user", "POST", "/api/v1/quotes/"+qB+"/exposure/request-ack", "", nil, http.StatusOK)

	// The auth core's own header rule stands in front of all of it: a bound
	// key naming the other branch in X-Branch-Id is refused before the router.
	status, buf := f.key(t, f.bound, "GET", "/api/v1/quotes/"+qA+"/exposure", "",
		map[string]string{"X-Branch-Id": f.branchB.String()})
	if status != http.StatusForbidden {
		t.Errorf("bound key with foreign X-Branch-Id: %d %s, want 403", status, buf)
	}
}

// TestKeyBranchPin_DealerWideReads holds the deny by default refusals on the
// dealer wide surface: every reporting route, the events feed, the two exposure
// lists, the GL, AP, bank reconciliation and sales team routes, the known
// users list, the staff roster, the market index refresh and its preview, and
// the unfiltered pricing lists refuse a branch bound key outright with their
// audit row, while the unbound key and the user keep the same answer as each
// other on every route.
func TestKeyBranchPin_DealerWideReads(t *testing.T) {
	f := newKeyPinFixture(t)
	preview := `{"entity_type":"invoices","definition":{}}`
	idx := uuid.NewString()
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/reports/sales-summary", ""},
		{"GET", "/api/v1/reports/daily-till", ""},
		{"GET", "/api/v1/reports/ar-aging", ""},
		{"GET", "/api/v1/reports/customer-statement/" + f.custID.String(), ""},
		{"GET", "/api/v1/reporting/export/invoices", ""},
		{"POST", "/api/v1/reporting/builder/preview", preview},
		{"POST", "/api/v1/reporting/builder/export", preview},
		{"GET", "/api/v1/reporting/saved", ""},
		{"POST", "/api/v1/reporting/saved/" + uuid.NewString() + "/run", ""},
		{"GET", "/api/v1/reporting/schedules", ""},
		{"POST", "/api/v1/reporting/schedules", `{"name":"pin"}`},
		{"GET", "/api/v1/events", ""},
		{"GET", "/api/v1/quotes/exposure", ""},
		{"GET", "/api/v1/reports/exposure", ""},
		{"GET", "/api/v1/gl/accounts", ""},
		{"GET", "/api/v1/gl/journal-entries", ""},
		{"GET", "/api/v1/gl/journal-entries/" + uuid.NewString(), ""},
		{"GET", "/api/v1/gl/trial-balance", ""},
		{"GET", "/api/v1/gl/profit-and-loss", ""},
		{"GET", "/api/v1/gl/balance-sheet", ""},
		{"GET", "/api/v1/gl/fiscal-periods", ""},
		{"POST", "/api/v1/gl/accounts", `{"name":"pin wall probe"}`},
		{"POST", "/api/v1/gl/journal-entries", `{"lines":[]}`},
		{"POST", "/api/v1/gl/fiscal-periods/" + uuid.NewString() + "/close", ""},
		{"POST", "/api/v1/gl/fiscal-periods/" + uuid.NewString() + "/reopen", ""},
		{"POST", "/api/v1/gl/journal-entries/" + uuid.NewString() + "/post", ""},
		{"POST", "/api/v1/gl/journal-entries/" + uuid.NewString() + "/reverse", ""},
		{"POST", "/api/v1/gl/journal-entries/" + uuid.NewString() + "/void", ""},
		{"PUT", "/api/v1/gl/accounts/" + uuid.NewString(), `{"name":"pin wall probe"}`},
		{"GET", "/api/v1/ap/invoices", ""},
		{"GET", "/api/v1/ap/invoices/" + uuid.NewString(), ""},
		{"GET", "/api/v1/ap/payments", ""},
		{"GET", "/api/v1/ap/aging", ""},
		{"POST", "/api/v1/ap/invoices", `{"vendor_id":"` + uuid.NewString() + `"}`},
		{"POST", "/api/v1/ap/payments", `{"invoice_id":"` + uuid.NewString() + `"}`},
		{"POST", "/api/v1/ap/invoices/" + uuid.NewString() + "/transitions", ""},
		{"GET", "/api/v1/bankrecon/accounts", ""},
		{"GET", "/api/v1/bankrecon/sessions", ""},
		{"POST", "/api/v1/bankrecon/import", ""},
		{"POST", "/api/v1/bankrecon/match", `{"bank_tx_id":"` + uuid.NewString() + `"}`},
		{"POST", "/api/v1/bankrecon/sessions", `{"bank_account_id":"` + uuid.NewString() + `"}`},
		{"GET", "/api/v1/bankrecon/sessions/" + uuid.NewString(), ""},
		{"POST", "/api/v1/bankrecon/accounts", `{"name":"pin"}`},
		{"POST", "/api/v1/bankrecon/sessions/" + uuid.NewString() + "/complete", ""},
		{"POST", "/api/v1/bankrecon/unmatch", `{"match_id":"` + uuid.NewString() + `"}`},
		{"GET", "/api/v1/sales-team", ""},
		{"GET", "/api/v1/sales-team/" + uuid.NewString(), ""},
		{"GET", "/api/v1/users", ""},
		{"GET", "/api/v1/admin/staff", ""},
		{"GET", "/api/v1/admin/staff/" + uuid.NewString(), ""},
		{"POST", "/api/v1/admin/staff", `{"name":"pin"}`},
		{"POST", "/api/v1/admin/staff/" + uuid.NewString() + "/modules", `{"modules":["pos"]}`},
		{"PUT", "/api/v1/admin/staff/" + uuid.NewString(), `{"name":"pin"}`},
		{"DELETE", "/api/v1/admin/staff/" + uuid.NewString() + "/modules/m", ""},
		{"POST", "/api/v1/market-indices/" + idx + "/refresh", ""},
		{"POST", "/api/v1/market-indices/" + idx + "/refresh/preview", `{"new_value":120}`},
		{"GET", "/api/v1/pricing/rules", ""},
		{"GET", "/api/v1/pricing/matrix", ""},
		{"GET", "/api/v1/reporting/saved/" + uuid.NewString(), ""},
		{"POST", "/api/v1/reporting/save", `{"name":"pin"}`},
		{"PUT", "/api/v1/reporting/saved/" + uuid.NewString(), `{"name":"pin"}`},
		{"DELETE", "/api/v1/reporting/saved/" + uuid.NewString(), ""},
		{"DELETE", "/api/v1/reporting/schedules/" + uuid.NewString(), ""},
	} {
		f.refuseForeign(t, c.method, c.path, c.body, nil)
		f.parity(t, c.method, c.path, c.body)
	}

	// The reads that answer data keep answering it to the unbound key: the
	// summaries and lists the second review read branch B rows through.
	for _, path := range []string{
		"/api/v1/reports/sales-summary", "/api/v1/reports/ar-aging",
		"/api/v1/reports/customer-statement/" + f.custID.String(),
		"/api/v1/events", "/api/v1/quotes/exposure", "/api/v1/gl/accounts",
		"/api/v1/ap/invoices", "/api/v1/sales-team", "/api/v1/users",
		"/api/v1/admin/staff", "/api/v1/pricing/rules", "/api/v1/pricing/matrix",
	} {
		if sk := f.parity(t, "GET", path, ""); sk != http.StatusOK {
			t.Errorf("unbound key GET %s: %d, want 200", path, sk)
		}
	}

	// A route the router resolves to no pattern at all is refused the same
	// way: a method no route registers on that path (the mux answers 405) or
	// a path no route registers (404) fails closed for a bound key, and the
	// unbound key and the user keep the router's own answer, never a refusal.
	f.refuseForeign(t, "GET", "/api/v1/tax/exemptions", "", nil)
	sk := f.parity(t, "GET", "/api/v1/tax/exemptions", "")
	if sk == http.StatusForbidden {
		t.Errorf("GET /api/v1/tax/exemptions: unbound and user answer 403; the router own answer was expected")
	}
	f.refuseForeign(t, "GET", "/api/v1/quotes/analytics/unregistered/deeper", "", nil)
	if sk := f.parity(t, "GET", "/api/v1/quotes/analytics/unregistered/deeper", ""); sk != http.StatusNotFound {
		t.Errorf("GET /api/v1/quotes/analytics/unregistered/deeper: unbound and user answer %d, want 404 both", sk)
	}
}

// pricingRuleBody builds a plain pricing rule create body for a customer.
// label tells the principals' rules apart: a rule's name and scope is unique.
func pricingRuleBody(label string, customer uuid.UUID) string {
	return fmt.Sprintf(`{"name":"pin rule %s","rule_type":"job_override","customer_id":%q,"discount_pct":"5"}`,
		label, customer.String())
}

// categoryRuleBody builds a category rule create body for a customer on a
// fresh category.
func categoryRuleBody(customer, category uuid.UUID) string {
	return fmt.Sprintf(`{"target_type":"account","customer_id":%q,"category_id":%q,"rule_type":"markup","value_pct":"5"}`,
		customer.String(), category.String())
}

// freshCategory inserts a product category no rule hangs on yet, so a create
// or a bulk element never meets the duplicate the seeded rules hold.
func (f *keyPinFixture) freshCategory(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO product_categories (id, name, slug, path) VALUES ($1, 'pin cat', $2, $3)`,
		id, "pin-cat-"+id.String()[:8], "pin."+id.String()[:8]); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	return id
}

// seedCatRule inserts a category rule row for a customer on a category.
func (f *keyPinFixture) seedCatRule(t *testing.T, id, customer, category uuid.UUID) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO category_pricing_rules (id, target_type, customer_id, category_id, rule_type, rule_value, created_by)
		VALUES ($1, 'ACCOUNT', $2, $3, 'MARKUP', 10, 'test')`, id, customer, category); err != nil {
		t.Fatalf("seed category rule: %v", err)
	}
}

// seedTaxExempt inserts a tax exemption row for a customer.
func (f *keyPinFixture) seedTaxExempt(t *testing.T, id, customer uuid.UUID) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO tax_exemptions (id, customer_id, exempt_reason, certificate_number, issuing_state)
		VALUES ($1, $2, 'pin', 'PIN-CERT-1', 'WA')`, id, customer); err != nil {
		t.Fatalf("seed tax exemption: %v", err)
	}
}

// exemptionsOf counts a customer's tax exemption rows.
func (f *keyPinFixture) exemptionsOf(t *testing.T, customer uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM tax_exemptions WHERE customer_id = $1`, customer).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// rulesOf counts a customer's priced rules, plain and category.
func (f *keyPinFixture) rulesOf(t *testing.T, customer uuid.UUID) (plain, category int) {
	t.Helper()
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT (SELECT COUNT(*) FROM pricing_rules WHERE customer_id = $1),
		(SELECT COUNT(*) FROM category_pricing_rules WHERE customer_id = $1)`, customer).Scan(&plain, &category); err != nil {
		t.Fatal(err)
	}
	return plain, category
}

// catRuleRev reads a category rule row's revision for an If-Match.
func (f *keyPinFixture) catRuleRev(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var rev int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT revision FROM category_pricing_rules WHERE id = $1`, id).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`"%d"`, rev)
}

// TestKeyBranchPin_CustomerConfinedWrites holds the pin on the customer
// confined writes (the lead's ruling): the tax exemption writes and the
// customer priced rules (plain and category, single and bulk) reach a branch
// bound key only while its pin is among the customer's branches
// (customer_branches); a customer outside the pin is refused with the audit
// row and no write, the pin's own customer passes, and the unbound key and
// the user keep the same answer as each other on every route, each against
// its own fresh target where the write would otherwise consume it.
func TestKeyBranchPin_CustomerConfinedWrites(t *testing.T) {
	f := newKeyPinFixture(t)
	A, B := f.custID.String(), f.custB.String()

	// Tax exemptions: the create's body names the customer, the delete's row
	// belongs to one.
	f.refuseForeign(t, "POST", "/api/v1/tax/exemptions",
		fmt.Sprintf(`{"customer_id":%q,"exempt_reason":"pin"}`, B), nil)
	if n := f.exemptionsOf(t, f.custB); n != 1 {
		t.Errorf("refused exemption create wrote the foreign customer's rows: %d", n)
	}
	exemptA := uuid.New()
	f.seedTaxExempt(t, exemptA, f.custID)
	f.allow(t, "bound", "DELETE", "/api/v1/tax/exemptions/"+exemptA.String(), "", nil, http.StatusNoContent)
	f.refuseForeign(t, "DELETE", "/api/v1/tax/exemptions/"+f.taxExempt.String(), "", nil)
	if n := f.exemptionsOf(t, f.custB); n != 1 {
		t.Errorf("refused exemption delete changed the foreign customer's rows: %d", n)
	}
	f.allow(t, "bound", "POST", "/api/v1/tax/exemptions",
		fmt.Sprintf(`{"customer_id":%q,"exempt_reason":"pin"}`, A), nil, http.StatusCreated)
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/tax/exemptions",
			fmt.Sprintf(`{"customer_id":%q,"exempt_reason":"pin"}`, B), nil, http.StatusCreated)
	}
	for _, who := range []string{"unbound", "user"} {
		id := uuid.New()
		f.seedTaxExempt(t, id, f.custB)
		f.allow(t, who, "DELETE", "/api/v1/tax/exemptions/"+id.String(), "", nil, http.StatusNoContent)
	}

	// Plain pricing rules: the create's body names the customer.
	f.refuseForeign(t, "POST", "/api/v1/pricing/rules", pricingRuleBody("refused", f.custB), nil)
	if _, n := f.rulesOf(t, f.custB); n != 1 {
		t.Errorf("refused pricing rule create wrote the foreign customer's rows: %d", n)
	}
	f.allow(t, "bound", "POST", "/api/v1/pricing/rules", pricingRuleBody("own", f.custID), nil, http.StatusCreated)
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/rules", pricingRuleBody(who, f.custB), nil, http.StatusCreated)
	}

	// Category rules: the create's body names the customer, the update and
	// the delete act on a row that belongs to one.
	f.refuseForeign(t, "POST", "/api/v1/pricing/category-rules", categoryRuleBody(f.custB, f.freshCategory(t)), nil)
	if _, n := f.rulesOf(t, f.custB); n != 1 {
		t.Errorf("refused category rule create wrote the foreign customer's rows: %d", n)
	}
	f.allow(t, "bound", "POST", "/api/v1/pricing/category-rules",
		categoryRuleBody(f.custID, f.freshCategory(t)), nil, http.StatusCreated)
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/category-rules",
			categoryRuleBody(f.custB, f.freshCategory(t)), nil, http.StatusCreated)
	}

	putBody := `{"rule_type":"markdown","value_pct":"2"}`
	putHdr := func(id uuid.UUID) map[string]string {
		return map[string]string{"If-Match": f.catRuleRev(t, id)}
	}
	f.refuseForeign(t, "PUT", "/api/v1/pricing/category-rules/"+f.catRuleB.String(), putBody, putHdr(f.catRuleB))
	f.allow(t, "bound", "PUT", "/api/v1/pricing/category-rules/"+f.catRuleA.String(), putBody, putHdr(f.catRuleA), http.StatusOK)
	ruleFor := func(customer uuid.UUID) uuid.UUID {
		id := uuid.New()
		f.seedCatRule(t, id, customer, f.freshCategory(t))
		return id
	}
	for _, who := range []string{"unbound", "user"} {
		id := ruleFor(f.custB)
		f.allow(t, who, "PUT", "/api/v1/pricing/category-rules/"+id.String(), putBody, putHdr(id), http.StatusOK)
	}

	_, beforeCat := f.rulesOf(t, f.custB)
	f.refuseForeign(t, "DELETE", "/api/v1/pricing/category-rules/"+f.catRuleB.String(), "", putHdr(f.catRuleB))
	if _, n := f.rulesOf(t, f.custB); n != beforeCat {
		t.Errorf("refused category rule delete changed the foreign customer's rows: %d then %d", beforeCat, n)
	}
	deadA := ruleFor(f.custID)
	f.allow(t, "bound", "DELETE", "/api/v1/pricing/category-rules/"+deadA.String(), "", putHdr(deadA), http.StatusNoContent)
	for _, who := range []string{"unbound", "user"} {
		id := ruleFor(f.custB)
		f.allow(t, who, "DELETE", "/api/v1/pricing/category-rules/"+id.String(), "", putHdr(id), http.StatusNoContent)
	}

	// Bulk: every element's customer must hold the pin, and a bulk delete's
	// rows belong to their customers.
	_, beforeBulk := f.rulesOf(t, f.custB)
	f.refuseForeign(t, "POST", "/api/v1/pricing/category-rules/bulk",
		"["+categoryRuleBody(f.custID, f.freshCategory(t))+","+categoryRuleBody(f.custB, f.freshCategory(t))+"]", nil)
	if _, n := f.rulesOf(t, f.custB); n != beforeBulk {
		t.Errorf("refused bulk upsert wrote the foreign customer's rows: %d then %d", beforeBulk, n)
	}
	f.allow(t, "bound", "POST", "/api/v1/pricing/category-rules/bulk",
		"["+categoryRuleBody(f.custID, f.freshCategory(t))+"]", nil, http.StatusOK)
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "POST", "/api/v1/pricing/category-rules/bulk",
			"["+categoryRuleBody(f.custB, f.freshCategory(t))+"]", nil, http.StatusOK)
	}
	_, beforeBulkDel := f.rulesOf(t, f.custB)
	f.refuseForeign(t, "DELETE", "/api/v1/pricing/category-rules/bulk",
		fmt.Sprintf(`{"ids":[%q]}`, f.catRuleB.String()), nil)
	if _, n := f.rulesOf(t, f.custB); n != beforeBulkDel {
		t.Errorf("refused bulk delete changed the foreign customer's rows: %d then %d", beforeBulkDel, n)
	}
	// A bulk delete body that is not exactly one complete JSON value never
	// reaches the handler for a bound key: the wall reads the whole body, and
	// the handler's own stricter decoder would have answered it 400 with the
	// delete never attempted.
	for _, body := range []string{
		fmt.Sprintf(`{"ids":[%q]} {}`, f.catRuleB.String()),
		fmt.Sprintf(`{"ids":[%q}] trailing`, f.catRuleB.String()),
		`[{"ids":[]}]`,
		``,
	} {
		f.refuseForeign(t, "DELETE", "/api/v1/pricing/category-rules/bulk", body, nil)
	}
	for _, who := range []string{"unbound", "user"} {
		id := ruleFor(f.custB)
		f.allow(t, who, "DELETE", "/api/v1/pricing/category-rules/bulk",
			fmt.Sprintf(`{"ids":[%q]}`, id.String()), nil, http.StatusNoContent)
	}
}

// TestKeyBranchPin_SwitchOff pins the wall's independence from the
// multi_branch_enabled switch: with the switch off the pin still refuses the
// foreign branch and still passes the pin's own, and the unbound key and the
// user keep their reach. The wall never reads the switch; this is the case
// that says so.
func TestKeyBranchPin_SwitchOff(t *testing.T) {
	f := newKeyPinFixtureSwitched(t, "false")
	A, B := f.branchA.String(), f.branchB.String()

	f.refuseForeign(t, "POST", "/api/v1/users/pin-off-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, B), nil)
	if f.hasGrant(t, "pin-off-grant", f.branchB) {
		t.Errorf("switch off refused grant wrote the foreign grant row")
	}
	f.allow(t, "bound", "POST", "/api/v1/users/pin-off-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, A), nil, http.StatusNoContent)
	f.allow(t, "unbound", "POST", "/api/v1/users/pin-off-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, B), nil, http.StatusNoContent)
	f.allow(t, "user", "POST", "/api/v1/users/pin-off-grant/branches", fmt.Sprintf(`{"branch_id":%q}`, B), nil, http.StatusNoContent)
	f.refuseForeign(t, "GET", "/api/v1/branches/"+B+"/users", "", nil)
}

// TestKeyBranchPin_RowWithoutBranch refuses a bound key a location row that
// names no branch (a legacy shape the denorm trigger refuses today): such a
// row is outside every pin, so it is refused like a foreign branch, while the
// unbound key and the user keep their reach on it.
func TestKeyBranchPin_RowWithoutBranch(t *testing.T) {
	f := newKeyPinFixture(t)
	f.refuseForeign(t, "PUT", "/api/v1/locations/"+f.yardNull.String(),
		fmt.Sprintf(`{"code":"pin-null-%s","path":"pin","name":"no"}`, f.yardNull.String()[:8]),
		map[string]string{"If-Match": `"1"`})
	if rev, _, active := f.locState(t, f.yardNull); rev != 1 || !active {
		t.Errorf("refused PUT changed the null branch row: revision %d active %v", rev, active)
	}
	f.putLocation(t, "unbound", f.yardNull, "pin free null edit", http.StatusOK)
	f.putLocation(t, "user", f.yardNull, "pin user null edit", http.StatusOK)
}

// TestKeyBranchPin_PosAndAccountRows holds the pin on the POS and account by
// id routes (round 5 P1, round 6 P1-2): a branch bound key reads or writes
// branch B's till session, Z report, return, transaction line or account only
// through its own branch. A foreign row answers exactly what a missing row
// answers, never B data; nothing is written (row counts before and after);
// the pin's own rows pass to the module; and the unbound key and the user
// keep their reach. The reconciliation reads every branch's books and is
// refused outright (the allowlist gate), like the GL surface.
func TestKeyBranchPin_PosAndAccountRows(t *testing.T) {
	f := newKeyPinFixture(t)
	f.record()
	defer driverDone(t, "pos")
	custA, custB := f.custID.String(), f.custB.String()
	missing := uuid.NewString()
	linesOn := func(tx uuid.UUID) (n int) {
		t.Helper()
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM pos_line_items WHERE transaction_id = $1`, tx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tillOpen := func(id uuid.UUID) bool {
		t.Helper()
		var status string
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT status FROM till_sessions WHERE id = $1`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status == "OPEN"
	}
	zReportsOn := func(session uuid.UUID) (n int) {
		t.Helper()
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM till_z_reports WHERE till_session_id = $1`, session).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The open till of the foreign register: no session answers, while the
	// pin's own register answers its own.
	_, buf := f.key(t, f.bound, "GET", "/api/v1/pos/till/current?register_id="+f.regB, "", nil)
	f.twinLeg("GET", "/api/v1/pos/till/current")
	if strings.Contains(string(buf), f.tillB.String()) {
		t.Errorf("bound key till current of the foreign register answered the foreign session: %s", buf)
	}
	buf = f.allow(t, "bound", "GET", "/api/v1/pos/till/current?register_id="+f.regA, "", nil, http.StatusOK)
	if !strings.Contains(string(buf), f.tillA.String()) {
		t.Errorf("bound key till current of the own register did not answer the own session: %s", buf)
	}

	// The till report of the foreign session answers exactly what a missing
	// session answers (never B data), and the unbound key reads the foreign
	// one; the pin's own session answers its report.
	sMiss, _ := f.key(t, f.bound, "GET", "/api/v1/pos/till/"+missing+"/report", "", nil)
	sB, bB := f.key(t, f.bound, "GET", "/api/v1/pos/till/"+f.tillB.String()+"/report", "", nil)
	f.twinLeg("GET", "/api/v1/pos/till/"+f.tillB.String()+"/report")
	if sB != sMiss || strings.Contains(string(bB), f.regB) {
		t.Errorf("bound key foreign till report: %d %s, want the missing row answer (%d, no B data)", sB, bB, sMiss)
	}
	f.allow(t, "unbound", "GET", "/api/v1/pos/till/"+f.tillB.String()+"/report", "", nil, http.StatusOK)
	f.allow(t, "bound", "GET", "/api/v1/pos/till/"+f.tillA.String()+"/report", "", nil, http.StatusOK)

	// The foreign till cannot be closed: no session write, no Z report.
	f.key(t, f.bound, "POST", "/api/v1/pos/till/"+f.tillB.String()+"/close",
		`{"counted_by_method":{"cash":5000}}`, nil)
	f.twinLeg("POST", "/api/v1/pos/till/"+f.tillB.String()+"/close")
	if !tillOpen(f.tillB) {
		t.Errorf("bound key closed the foreign till session")
	}
	if n := zReportsOn(f.tillB); n != 1 {
		t.Errorf("bound key foreign till close wrote Z reports: %d rows for the session", n)
	}

	// The Z report of the foreign session answers what a missing one answers;
	// the list carries the pin's own Z report and not the foreign one.
	sMiss, _ = f.key(t, f.bound, "GET", "/api/v1/pos/till/"+missing+"/zreport", "", nil)
	if sB, _ := f.key(t, f.bound, "GET", "/api/v1/pos/till/"+f.tillB.String()+"/zreport", "", nil); sB != sMiss {
		t.Errorf("bound key foreign Z report: %d, want the missing row answer %d", sB, sMiss)
	}
	f.twinLeg("GET", "/api/v1/pos/till/"+f.tillB.String()+"/zreport")
	_, buf = f.key(t, f.bound, "GET", "/api/v1/pos/zreports", "", nil)
	f.twinLeg("GET", "/api/v1/pos/zreports")
	if strings.Contains(string(buf), f.zrB.String()) {
		t.Errorf("bound key Z report list carries the foreign Z report: %s", buf)
	}
	if !strings.Contains(string(buf), f.zrA.String()) {
		t.Errorf("bound key Z report list does not carry the pin's own Z report: %s", buf)
	}

	// The returns list carries the pin's own return and not the foreign one;
	// the foreign return reads as missing, the own one answers.
	_, buf = f.key(t, f.bound, "GET", "/api/v1/pos/returns", "", nil)
	f.twinLeg("GET", "/api/v1/pos/returns")
	if strings.Contains(string(buf), f.retB.String()) {
		t.Errorf("bound key returns list carries the foreign return: %s", buf)
	}
	if !strings.Contains(string(buf), f.retA.String()) {
		t.Errorf("bound key returns list does not carry the pin's own return: %s", buf)
	}
	if sB, _ := f.key(t, f.bound, "GET", "/api/v1/pos/returns/"+f.retB.String(), "", nil); sB != http.StatusNotFound {
		t.Errorf("bound key foreign return read: %d, want 404", sB)
	}
	f.twinLeg("GET", "/api/v1/pos/returns/"+f.retB.String())
	f.allow(t, "unbound", "GET", "/api/v1/pos/returns/"+f.retB.String(), "", nil, http.StatusOK)
	f.allow(t, "bound", "GET", "/api/v1/pos/returns/"+f.retA.String(), "", nil, http.StatusOK)

	// The transaction reads hold the pin already (branch_id predicate); the
	// line writes now hold it too: no line is written on or removed from the
	// foreign transaction, and the pin's own transaction takes the write.
	if sB, _ := f.key(t, f.bound, "GET", "/api/v1/pos/transactions/"+f.txnB.String(), "", nil); sB != http.StatusNotFound {
		t.Errorf("bound key foreign transaction read: %d, want 404", sB)
	}
	f.twinLeg("GET", "/api/v1/pos/transactions/"+f.txnB.String())
	before := linesOn(f.txnB)
	f.key(t, f.bound, "POST", "/api/v1/pos/transactions/"+f.txnB.String()+"/items",
		fmt.Sprintf(`{"line":{"product_id":%q,"quantity":"1"}}`, f.posProd.String()), nil)
	f.twinLeg("POST", "/api/v1/pos/transactions/"+f.txnB.String()+"/items")
	if after := linesOn(f.txnB); after != before {
		t.Errorf("bound key line insert on the foreign transaction wrote: %d rows then %d", before, after)
	}
	f.key(t, f.bound, "DELETE", "/api/v1/pos/transactions/"+f.txnB.String()+"/items/"+f.lineB.String(), "", nil)
	f.twinLeg("DELETE", "/api/v1/pos/transactions/"+f.txnB.String()+"/items/"+f.lineB.String())
	if after := linesOn(f.txnB); after != before {
		t.Errorf("bound key line delete on the foreign transaction wrote: %d rows then %d", before, after)
	}
	before = linesOn(f.txnA)
	f.allow(t, "bound", "POST", "/api/v1/pos/transactions/"+f.txnA.String()+"/items",
		fmt.Sprintf(`{"line":{"product_id":%q,"quantity":"1"}}`, f.posProd.String()), nil, http.StatusOK)
	if after := linesOn(f.txnA); after != before+1 {
		t.Errorf("bound key line insert on the own transaction: %d rows then %d, want one new line", before, after)
	}

	// The counter's customer: the lookup finds the pin's own customer and
	// never the foreign one, and naming a customer on the foreign sale reads
	// as a missing sale (nothing written) while the pin's own sale takes it.
	var acctA, acctB string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT account_number FROM customers WHERE id = $1`, f.custID).Scan(&acctA); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT account_number FROM customers WHERE id = $1`, f.custB).Scan(&acctB); err != nil {
		t.Fatal(err)
	}
	_, buf = f.key(t, f.bound, "GET", "/api/v1/pos/customers?q="+url.QueryEscape(acctB), "", nil)
	f.twinLeg("GET", "/api/v1/pos/customers")
	if strings.Contains(string(buf), f.custB.String()) {
		t.Errorf("bound key counter lookup carries the foreign customer: %s", buf)
	}
	buf = f.allow(t, "bound", "GET", "/api/v1/pos/customers?q="+url.QueryEscape(acctA), "", nil, http.StatusOK)
	if !strings.Contains(string(buf), f.custID.String()) {
		t.Errorf("bound key counter lookup does not carry the pin's own customer: %s", buf)
	}
	if sB, _ := f.key(t, f.bound, "PUT", "/api/v1/pos/transactions/"+f.txnB.String()+"/customer",
		fmt.Sprintf(`{"customer_id":%q}`, f.custID.String()), nil); sB != http.StatusNotFound {
		t.Errorf("bound key naming a customer on the foreign sale: %d, want 404", sB)
	}
	f.twinLeg("PUT", "/api/v1/pos/transactions/"+f.txnB.String()+"/customer")
	var named *uuid.UUID
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT customer_id FROM pos_transactions WHERE id = $1`, f.txnB).Scan(&named); err != nil {
		t.Fatal(err)
	}
	if named != nil && *named == f.custID {
		t.Errorf("bound key named the pin's customer on the foreign sale")
	}
	f.allow(t, "bound", "PUT", "/api/v1/pos/transactions/"+f.txnA.String()+"/customer",
		`{"customer_id":null}`, nil, http.StatusOK)

	// The account routes name a customer in the path: the foreign customer is
	// refused with the audit row, the pin's own answers, and the
	// reconciliation (every branch's books) is refused outright.
	f.refuseForeign(t, "GET", "/api/v1/accounts/"+custB, "", nil)
	f.refuseForeign(t, "GET", "/api/v1/accounts/"+custB+"/transactions", "", nil)
	f.refuseForeign(t, "GET", "/api/v1/ar/customers/"+custB+"/statement", "", nil)
	f.refuseForeign(t, "GET", "/api/v1/ar/reconciliation", "", nil)
	f.allow(t, "bound", "GET", "/api/v1/accounts/"+custA, "", nil, http.StatusOK)
	f.allow(t, "bound", "GET", "/api/v1/accounts/"+custA+"/transactions", "", nil, http.StatusOK)
	f.allow(t, "bound", "GET", "/api/v1/ar/customers/"+custA+"/statement", "", nil, http.StatusOK)
	for _, who := range []string{"unbound", "user"} {
		f.allow(t, who, "GET", "/api/v1/accounts/"+custB, "", nil, http.StatusOK)
		f.allow(t, who, "GET", "/api/v1/ar/customers/"+custB+"/statement", "", nil, http.StatusOK)
		f.allow(t, who, "GET", "/api/v1/ar/reconciliation", "", nil, http.StatusOK)
	}
}
