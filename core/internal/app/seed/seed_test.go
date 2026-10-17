// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"context"
	"database/sql"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
)

// The seeder writes prices. A number in demoPricingRules that the pricing
// engine reads differently from the way the seeder meant it is not a cosmetic
// defect — it is the demo quoting the wrong money, and it shipped: discount_pct
// was written as 0.10 for "10% off" while internal/pricing/service.go divides
// by 100, so a 10% rule delivered a tenth of a percent.
//
// These tests do not restate the arithmetic. They push each seeded row through
// the actual pricing.Service, so the seeder and the engine cannot disagree
// about units without a red test.

// oneRuleRepo is a pricing.Repository holding exactly one rule and no
// contracts, so a Resolve call resolves to that rule or to retail. Category
// scope is not modelled here: it belongs to the repository layer and is
// tested in internal/pricing against Postgres.
type oneRuleRepo struct{ rule pricing.PricingRule }

func (r *oneRuleRepo) GetContract(ctx context.Context, customerID, productID uuid.UUID) (*pricing.CustomerContract, error) {
	return nil, nil
}
func (r *oneRuleRepo) CreateContract(ctx context.Context, c *pricing.CustomerContract) error {
	return nil
}
func (r *oneRuleRepo) GetMatchingRules(ctx context.Context, productID uuid.UUID, customerID, jobID *uuid.UUID, quantity httpx.Quantity) ([]pricing.PricingRule, error) {
	if r.rule.MinQuantity > quantity {
		return nil, nil
	}
	return []pricing.PricingRule{r.rule}, nil
}
func (r *oneRuleRepo) ListBreakQuantities(ctx context.Context, productID uuid.UUID, customerID *uuid.UUID) ([]httpx.Quantity, error) {
	return nil, nil
}
func (r *oneRuleRepo) CreateRule(ctx context.Context, rule *pricing.PricingRule) error { return nil }
func (r *oneRuleRepo) ListRules(ctx context.Context) ([]pricing.PricingRule, error) {
	return nil, nil
}
func (r *oneRuleRepo) ListRulesPage(ctx context.Context, after *pricing.RuleCursor, limit int) ([]pricing.PricingRule, error) {
	return nil, nil
}
func (r *oneRuleRepo) CountRules(ctx context.Context) (int64, error) {
	return 0, nil
}

// priceWithSeededRule prices one line at basePrice/quantity using a single
// seeded rule, through the same service the API and the order path use.
func priceWithSeededRule(t *testing.T, r pricingRule, basePrice, quantity float64) pricing.CalculatedPrice {
	t.Helper()
	rule := pricing.PricingRule{
		ID:          uuid.New(),
		Name:        r.Name,
		RuleType:    pricing.RuleType(r.RuleType),
		Category:    r.Category,
		MinQuantity: seedQty(t, r.MinQty),
		IsActive:    true,
	}
	if r.DiscPct != nil {
		q := seedQty(t, *r.DiscPct)
		rule.DiscountPct = &q
	}
	if r.MarginFloor != nil {
		q := seedQty(t, *r.MarginFloor)
		rule.MarginFloorPct = &q
	}
	svc := pricing.NewService(&oneRuleRepo{rule: rule}).WithRepository(seedResolveRepo{basePrice: basePrice})
	custID := uuid.New()
	prodID := uuid.New()
	qty := seedQty(t, quantity)
	res, err := svc.Resolve(context.Background(), pricing.Request{
		CustomerID: custID,
		ProductID:  prodID,
		Quantity:   qty,
	})
	if err != nil {
		t.Fatalf("Resolve(%s): %v", r.Name, err)
	}
	return pricing.CalculatedPrice{
		ProductID:     prodID,
		OriginalPrice: float64(res.ReferencePrice) / 10_000,
		FinalPrice:    float64(res.UnitPrice) / 10_000,
		Source:        res.PriceBasis,
		Details:       res.Details,
	}
}

// seedQty parses one of the seed's decimal numbers into the engine's exact
// scale 4 quantity, the way the wire would.
func seedQty(t *testing.T, f float64) httpx.Quantity {
	t.Helper()
	q, err := httpx.ParseQuantity(strconv.FormatFloat(f, 'f', -1, 64))
	if err != nil {
		t.Fatalf("fixture quantity %v: %v", f, err)
	}
	return q
}

// CORRECTNESS: every seeded discount is a PERCENT, and the engine delivers it.
//
// Before the fix these rows held 0.10/0.15/0.05 and the engine's
// `basePrice * (1 - DiscountPct/100)` turned "10% off" into 0.1% off. The
// assertion that catches it is not "the field equals 10" but "the customer
// actually saves what the rule advertises", which is the property that matters
// and the one a future edit is most likely to break.
func TestDemoPricingRules_DeliverTheDiscountTheyAdvertise(t *testing.T) {
	const basePrice = 23.25 // the SKU from the live transcript

	for _, r := range demoPricingRules {
		if r.DiscPct == nil {
			continue
		}
		// Price at the rule's own threshold so it is guaranteed to apply.
		qty := math.Max(r.MinQty, 1)
		got := priceWithSeededRule(t, r, basePrice, qty)

		if got.Source == pricing.SourceRetail {
			t.Errorf("%s: rule did not apply at qty %v (got %+v)", r.Name, qty, got)
			continue
		}

		delivered := (basePrice - got.FinalPrice) / basePrice * 100
		if delivered < 1.0 {
			t.Errorf("%s: advertises %.2f%% off but the engine delivers %.4f%% — a fraction was written into a column the engine reads as a percent",
				r.Name, *r.DiscPct, delivered)
		}
		if math.Abs(delivered-*r.DiscPct) > 0.05 {
			t.Errorf("%s: advertises %.2f%% off, engine delivers %.4f%% (final %.4f on base %.2f)",
				r.Name, *r.DiscPct, delivered, got.FinalPrice, basePrice)
		}
	}
}

// CORRECTNESS: no seeded margin floor claws back its own rule's discount.
//
// margin_floor_pct is read as the deepest discount a rule may reach
// (`minPrice := basePrice * (1 - MarginFloorPct/100)`). It shares the units bug
// and the failure mode is nastier: with discount_pct corrected to 10 and the
// floor left at the old 0.20, the floor clamps the 10% break back to 0.2% and
// the bug survives the fix wearing a different hat. A floor below its rule's
// discount is always a units error or a typo.
func TestDemoPricingRules_MarginFloorsDoNotClampTheirOwnDiscount(t *testing.T) {
	for _, r := range demoPricingRules {
		if r.MarginFloor == nil || r.DiscPct == nil {
			continue
		}
		if *r.MarginFloor < *r.DiscPct {
			t.Errorf("%s: margin floor %.2f%% is shallower than the %.2f%% discount it guards, so the floor silently overrides the rule",
				r.Name, *r.MarginFloor, *r.DiscPct)
		}
		got := priceWithSeededRule(t, r, 23.25, math.Max(r.MinQty, 1))
		if got.Details != r.Name {
			t.Errorf("%s: margin floor fired on the rule's own discount (details %q)", r.Name, got.Details)
		}
	}
}

// REGRESSION: the exact line from the live transcript.
//
// "Spring Roofing Promo" is 5% off. Written as 0.05 it delivered 0.05%, and a
// $23.25 board came back at $23.24 — a saving of one cent, on a rule the
// dealer thinks takes $1.16 off. Written as 5 the exact engine answers
// 22.0875 (C3-1: the engine no longer rounds a rule price to cents; the one
// cent rounding is the line's extension, ADR 0006 R4).
func TestSpringRoofingPromo_TakesFivePercentNotFiveHundredthsOfOne(t *testing.T) {
	var promo pricingRule
	for _, r := range demoPricingRules {
		if r.Name == "Spring Roofing Promo" {
			promo = r
		}
	}
	if promo.Name == "" {
		t.Fatal("Spring Roofing Promo is no longer in demoPricingRules")
	}

	got := priceWithSeededRule(t, promo, 23.25, 1)
	if got.FinalPrice != 22.0875 {
		t.Errorf("FinalPrice = %.4f, want 22.0875 (exactly 5%% off 23.25); 23.24 is the old 0.05%%", got.FinalPrice)
	}
	if got.Source != pricing.SourcePromotional {
		t.Errorf("Source = %v, want PROMOTIONAL", got.Source)
	}
}

// CORRECTNESS: re-seeding updates the rule it already wrote instead of adding
// a second copy of it.
//
// The old clause was `ON CONFLICT DO NOTHING` on a table with no unique
// constraint — nothing for the conflict to be ON, so every row inserted every
// time and a working database accumulated a fresh set of six rules per run.
// This runs the seeder's actual statement twice and counts.
//
// DB-gated: it is a test of a Postgres constraint, so it needs Postgres.
func TestPricingRuleUpsert_IsIdempotentAndRepairsValues(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	name := "seedtest-upsert-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pricing_rules WHERE name = $1`, name)
	})

	upsert := func(discount float64) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, pricingRuleUpsertSQL,
			name, "QUANTITY_BREAK", "Lumber", discount, 100.0, 20.0); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	upsert(10)
	upsert(10)
	upsert(10)

	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM pricing_rules WHERE name = $1`, name).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("after three seed runs there are %d copies of %q, want 1 — the upsert is inserting instead of updating", count, name)
	}

	// And it is a real UPDATE, not DO NOTHING: a demo database holding the old
	// fractional 0.10 has to be repaired by the next seed run, not left as is.
	upsert(0.10)
	upsert(15)
	var discount float64
	if err := db.Pool.QueryRow(ctx, `SELECT discount_pct FROM pricing_rules WHERE name = $1`, name).Scan(&discount); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if discount != 15 {
		t.Errorf("discount_pct = %v after re-seeding at 15, want 15 — DO NOTHING would have left the stale value", discount)
	}
}

// CORRECTNESS: a re-seed moves the seed's own REG-01 and nothing else
// (principal item 57 question 3's condition).
//
// The upsert conflicts on the id, so it can only ever touch the migration's
// demo register REG-01; a register created by a person, an import or another
// seed has another id and must be left exactly as it was. Of REG-01 the seed
// owns the location and the branch alone, and only while the row still sits
// on migration 027's placeholder location: a register's name is an
// operator's to keep, so a re-seed must not rename it back (r14 P3-2), and a
// register the operator re-pointed by hand is not the seed's to move back
// either, because migration 027 created the row and item 57's condition
// reads that as another seed (r16 P2).
//
// DB-gated: it is a test of a Postgres upsert, so it needs Postgres, and it
// runs the seeder's actual statement, twice per leg.
func TestPosRegisterUpsert_MovesOnlyTheSeedsOwnRow(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	// A branch location of the test's own for the seed's target, and one the
	// operator re-point leg moves REG-01 onto.
	loc := uuid.New()
	opLoc := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'seed test branch')`,
		loc, "SEEDT-"+loc.String()[:8]); err != nil {
		t.Fatal(err)
	}
	// A register created outside the seed, on a location and branch of its
	// own, with its own name.
	foreign := "REG-SEEDTEST-" + uuid.NewString()[:8]
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO pos_registers (id, location_id, branch_id, name, is_active)
		 VALUES ($1, $2, $2, 'a person made this counter', false)`, foreign, loc); err != nil {
		t.Fatal(err)
	}
	// REG-01 as migrations 027 and 066 leave it: the placeholder location, the
	// default branch, the name 027 gave. The fixture is forced, not assumed:
	// the upsert under test can never put the row back once it has moved, so
	// the test starts from the migration shape whatever an earlier run (or an
	// operator) left behind, and the cleanup restores the same shape.
	restoreMigrationShape := func() {
		t.Helper()
		if _, err := db.Pool.Exec(ctx,
			`UPDATE pos_registers SET location_id = '00000000-0000-0000-0000-000000000000',
				branch_id = (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'),
				name = 'Counter 1'
			 WHERE id = 'REG-01'`); err != nil {
			t.Fatalf("restore REG-01's migration shape: %v", err)
		}
	}
	restoreMigrationShape()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_registers WHERE id = $1`, foreign)
		restoreMigrationShape()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, loc)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, opLoc)
	})
	if _, err := db.Pool.Exec(ctx, `UPDATE pos_registers SET name = 'renamed by the operator' WHERE id = 'REG-01'`); err != nil {
		t.Fatal(err)
	}

	// The branch half of the migration shape: the seeder reads
	// default_branch_id before it changes anything (preSeedDefaultBranch in
	// seed.go), and the test reads it the same way so the two run the same
	// statement with the same comparison value.
	var migrationBranch string
	if err := db.Pool.QueryRow(ctx,
		`SELECT value FROM system_settings WHERE key = 'default_branch_id'`).Scan(&migrationBranch); err != nil {
		t.Fatal(err)
	}
	upsert := func() {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, posRegisterUpsertSQL, loc.String(), migrationBranch); err != nil {
			t.Fatalf("the seeder's register upsert: %v", err)
		}
	}
	upsert()
	upsert()

	// The register created outside the seed is exactly as it was.
	var fLoc, fBranch, fName string
	var fActive bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT location_id::text, branch_id::text, name, is_active FROM pos_registers WHERE id = $1`, foreign).
		Scan(&fLoc, &fBranch, &fName, &fActive); err != nil {
		t.Fatal(err)
	}
	if fLoc != loc.String() || fBranch != loc.String() || fName != "a person made this counter" || fActive {
		t.Errorf("a re-seed changed a register it did not create: location %s branch %s name %q active %v", fLoc, fBranch, fName, fActive)
	}

	// The seed's own row moved onto the seed's location, twice idempotently,
	// and kept the operator's name.
	var loc1, branch1, name1 string
	if err := db.Pool.QueryRow(ctx,
		`SELECT location_id::text, branch_id::text, name FROM pos_registers WHERE id = 'REG-01'`).
		Scan(&loc1, &branch1, &name1); err != nil {
		t.Fatal(err)
	}
	if loc1 != loc.String() || branch1 != loc.String() {
		t.Errorf("the seed's own register did not move to the seed's location: location %s branch %s", loc1, branch1)
	}
	if name1 != "renamed by the operator" {
		t.Errorf("a re-seed renamed the register to %q, want the operator's name kept", name1)
	}

	// An operator re-pointed REG-01 onto a location and branch of their own
	// and named it: the row migration 027 created is not the seed's to move
	// back, so a re-seed, run twice, leaves it exactly where the operator
	// put it.
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'seed test operator branch')`,
		opLoc, "SEEDT-"+opLoc.String()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx,
		`UPDATE pos_registers SET location_id = $1, branch_id = $1, name = 'operator counter' WHERE id = 'REG-01'`, opLoc); err != nil {
		t.Fatal(err)
	}
	upsert()
	upsert()
	var opLoc1, opBranch1, opName1 string
	if err := db.Pool.QueryRow(ctx,
		`SELECT location_id::text, branch_id::text, name FROM pos_registers WHERE id = 'REG-01'`).
		Scan(&opLoc1, &opBranch1, &opName1); err != nil {
		t.Fatal(err)
	}
	if opLoc1 != opLoc.String() || opBranch1 != opLoc.String() {
		t.Errorf("a re-seed moved an operator re-pointed REG-01: location %s branch %s, want it kept at %s",
			opLoc1, opBranch1, opLoc)
	}
	if opName1 != "operator counter" {
		t.Errorf("a re-seed renamed the re-pointed register to %q, want the operator's name kept", opName1)
	}

	// The location alone leg (r16 P2's case, held since round 8): the
	// operator moved the location and left the branch as the migrations set
	// it. A re-seed, run twice, leaves the row where the operator put it.
	if _, err := db.Pool.Exec(ctx,
		`UPDATE pos_registers SET location_id = $1, branch_id = $2 WHERE id = 'REG-01'`,
		opLoc, migrationBranch); err != nil {
		t.Fatal(err)
	}
	upsert()
	upsert()
	var lLoc, lBranch string
	if err := db.Pool.QueryRow(ctx,
		`SELECT location_id::text, branch_id::text FROM pos_registers WHERE id = 'REG-01'`).
		Scan(&lLoc, &lBranch); err != nil {
		t.Fatal(err)
	}
	if lLoc != opLoc.String() || lBranch != migrationBranch {
		t.Errorf("a re-seed moved a location only re-pointed REG-01: location %s branch %s, want %s and the migration branch",
			lLoc, lBranch, opLoc)
	}

	// While the location is the placeholder, which names no locations row,
	// the register's branch comes from pos_registers.branch_id alone on every
	// pos read and write, so an operator who re-points REG-01 by its branch
	// alone has moved it as completely as a location re-point (r19 P2-1,
	// item 59's "the operator's row is never moved"). A re-seed, run twice,
	// must leave the branch where the operator put it and the location on
	// the placeholder.
	if _, err := db.Pool.Exec(ctx,
		`UPDATE pos_registers SET location_id = '00000000-0000-0000-0000-000000000000',
			branch_id = $1, name = 'operator counter' WHERE id = 'REG-01'`, opLoc); err != nil {
		t.Fatal(err)
	}
	upsert()
	upsert()
	var bLoc, bBranch, bName string
	if err := db.Pool.QueryRow(ctx,
		`SELECT location_id::text, branch_id::text, name FROM pos_registers WHERE id = 'REG-01'`).
		Scan(&bLoc, &bBranch, &bName); err != nil {
		t.Fatal(err)
	}
	if bLoc != "00000000-0000-0000-0000-000000000000" || bBranch != opLoc.String() {
		t.Errorf("a re-seed moved a branch only re-pointed REG-01: location %s branch %s, want the placeholder and %s",
			bLoc, bBranch, opLoc)
	}
	if bName != "operator counter" {
		t.Errorf("a re-seed renamed the branch only re-pointed register to %q, want the operator's name kept", bName)
	}
}

// The demo seed sets an invented dealer profile and logo, once: a profile the
// operator has filled in is not overwritten by a re-seed. The locale is
// en-CA (the demo dealer is in British Columbia): set on the blank write, and
// lifted from the migration's en-US default on a profile the demo already
// seeded; an operator's own tag is kept.
//
// DB-gated: it runs the seeder's actual statement against the single row.
func TestDealerProfileSeed_SetsOnlyABlankProfile(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	type snap struct {
		legal, trading, phone, email, website, remit string
		locale                                       string
		logo                                         []byte
		ctype                                        *string
		revision                                     int64
	}
	var orig snap
	if err := db.Pool.QueryRow(ctx, `SELECT legal_name, trading_name, phone, email, website, remit_to, locale, logo, logo_content_type, revision FROM dealer_profile WHERE id = 1`).
		Scan(&orig.legal, &orig.trading, &orig.phone, &orig.email, &orig.website, &orig.remit, &orig.locale, &orig.logo, &orig.ctype, &orig.revision); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `UPDATE dealer_profile SET legal_name=$1, trading_name=$2, phone=$3, email=$4, website=$5, remit_to=$6, locale=$7, logo=$8, logo_content_type=$9, revision=$10 WHERE id = 1`,
			orig.legal, orig.trading, orig.phone, orig.email, orig.website, orig.remit, orig.locale, orig.logo, orig.ctype, orig.revision)
	})
	blank := func() {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `UPDATE dealer_profile SET legal_name='', trading_name='', phone='', email='', website='', remit_to='', locale='en-US', logo=NULL, logo_content_type=NULL WHERE id = 1`); err != nil {
			t.Fatal(err)
		}
	}
	read := func() (legal, remit, locale string, hasLogo bool) {
		t.Helper()
		if err := db.Pool.QueryRow(ctx, `SELECT legal_name, remit_to, locale, logo IS NOT NULL FROM dealer_profile WHERE id = 1`).Scan(&legal, &remit, &locale, &hasLogo); err != nil {
			t.Fatal(err)
		}
		return
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	blank()
	seedDealerProfile(sqlDB)
	legal, remit, locale, hasLogo := read()
	if legal != "Gable Building Supply Ltd" || !strings.Contains(remit, "Accounts Receivable") || !hasLogo {
		t.Fatalf("a blank profile was not seeded: %q %q logo=%v", legal, remit, hasLogo)
	}
	if locale != "en-CA" {
		t.Errorf("the seeded locale = %q, want en-CA", locale)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE dealer_profile SET legal_name = 'Edited By The Operator Ltd', locale = 'fr-CA' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	seedDealerProfile(sqlDB)
	if legal, _, locale, _ := read(); legal != "Edited By The Operator Ltd" {
		t.Errorf("a re-seed overwrote the operator's profile: %q", legal)
	} else if locale != "fr-CA" {
		t.Errorf("a re-seed overwrote the operator's locale: %q", locale)
	}

	// A profile the demo already seeded that still sits on the migration's
	// en-US default is lifted to en-CA.
	if _, err := db.Pool.Exec(ctx, `UPDATE dealer_profile SET legal_name='Gable Building Supply Ltd', trading_name='Gable Building Supply', locale='en-US' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	seedDealerProfile(sqlDB)
	if _, _, locale, _ := read(); locale != "en-CA" {
		t.Errorf("the demo default was not lifted to en-CA: %q", locale)
	}
}

// CORRECTNESS: the seed mirrors each customer's primary branch into
// customer_branches, the row the product's own create writes beside it, and
// a re-seed never moves a customer's stored primary branch (r5 P2-1 and P3-9,
// r6 section 2). Without the row the customer wall hides the customer from
// every header and grants caller; moving the branch would strand the row.
// The seed owns only the rows it creates: a customer an operator moved by
// hand is not moved back.
//
// DB-gated. It runs the seeder's own customer step twice per leg.
func TestSeedCustomer_MirrorsThePrimaryBranchAndNeverMovesIt(t *testing.T) {
	pool := testutil.RequireDB(t)
	ctx := context.Background()
	sqlDB := stdlib.OpenDBFromPool(pool.Pool)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// P1-6 (review r1 argon): seedCustomer's third parameter is the
	// customer's pricing program id (price_profiles), not a level id.
	var priceLevel uuid.UUID
	if err := pool.Pool.QueryRow(ctx, `SELECT id FROM price_profiles LIMIT 1`).Scan(&priceLevel); err != nil {
		t.Fatalf("a price profile for the customer: %v", err)
	}
	branchA, branchB := uuid.New(), uuid.New()
	for _, b := range []uuid.UUID{branchA, branchB} {
		if _, err := pool.Pool.Exec(ctx,
			`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'seed customer test branch')`,
			b, "SEEDC-"+b.String()[:8]); err != nil {
			t.Fatal(err)
		}
	}
	acct := "SEEDC-" + uuid.NewString()[:8]
	c := demoCustomer{Name: "seed customer test", Acct: acct, Email: "seedc@example.com", Phone: "250-555-0100",
		Addr: "1 Test Rd", Terms: "NET30", Profile: "x", CreditLimit: 1000, PrimaryBranch: branchA}
	t.Cleanup(func() {
		_, _ = pool.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id IN (SELECT id FROM customers WHERE account_number = $1)`, acct)
		_, _ = pool.Pool.Exec(ctx, `DELETE FROM customers WHERE account_number = $1`, acct)
		_, _ = pool.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, branchA, branchB)
	})

	rows := func(id uuid.UUID) map[uuid.UUID]bool {
		t.Helper()
		out := map[uuid.UUID]bool{}
		r, err := pool.Pool.Query(ctx, `SELECT branch_id FROM customer_branches WHERE customer_id = $1`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for r.Next() {
			var b uuid.UUID
			if err := r.Scan(&b); err != nil {
				t.Fatal(err)
			}
			out[b] = true
		}
		return out
	}
	primary := func(id uuid.UUID) uuid.UUID {
		t.Helper()
		var b uuid.UUID
		if err := pool.Pool.QueryRow(ctx, `SELECT primary_branch_id FROM customers WHERE id = $1`, id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}

	id, branch, err := seedCustomer(sqlDB, c, priceLevel)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := seedCustomer(sqlDB, c, priceLevel); err != nil {
		t.Fatal(err)
	}
	if branch != branchA {
		t.Errorf("seedCustomer returned branch %s, want %s", branch, branchA)
	}
	if got := rows(id); len(got) != 1 || !got[branchA] {
		t.Errorf("after two seeds the customer holds branch rows %v, want exactly its primary branch %s", got, branchA)
	}

	// A customer an operator moved by hand is not moved back, and the row
	// follows the stored branch only by adding it, never by deleting.
	if _, err := pool.Pool.Exec(ctx, `UPDATE customers SET primary_branch_id = $1 WHERE id = $2`, branchB, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := seedCustomer(sqlDB, c, priceLevel); err != nil {
		t.Fatal(err)
	}
	if got := primary(id); got != branchB {
		t.Errorf("a re-seed moved the customer's primary branch to %s, want it left at %s", got, branchB)
	}
	if got := rows(id); !got[branchB] {
		t.Errorf("after the move the customer holds branch rows %v, want a row for its stored primary branch %s", got, branchB)
	}
}

// seedResolveRepo is the minimal ResolveRepository the seed's pricing rule
// tests price through: one product whose stocking and price unit are EA at
// the base price the test states, its (1, 1) unit set row, and nothing else
// (no contract, no explicit prices, no program, no vendor costs), so the
// seeded rule under test is the only rung that can answer.
type seedResolveRepo struct{ basePrice float64 }

func (r seedResolveRepo) ProductPricingFacts(_ context.Context, _ uuid.UUID) (*pricing.ProductPricingFacts, error) {
	return &pricing.ProductPricingFacts{
		BasePrice: priceOfSeed(r.basePrice), PriceUOM: "EA", StockUOM: "EA", SaleUOM: "EA",
	}, nil
}

func (r seedResolveRepo) UnitSetRow(_ context.Context, _ uuid.UUID, uom string) (*pricing.UnitSetRowFact, error) {
	if uom == "EA" {
		q, err := httpx.ParseQuantity("1")
		if err != nil {
			return nil, err
		}
		return &pricing.UnitSetRowFact{UOM: uom, UnitQty: q, StockQty: q}, nil
	}
	return nil, nil
}

func (r seedResolveRepo) ContractForBranch(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) (*pricing.CustomerContract, error) {
	return nil, nil
}
func (r seedResolveRepo) ProductPrice(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) (*pricing.ProductPrice, error) {
	return nil, nil
}
func (r seedResolveRepo) CustomerProgram(context.Context, uuid.UUID) (*pricing.PricingProgram, error) {
	return nil, nil
}
func (r seedResolveRepo) VendorCost(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, *uuid.UUID, time.Time) (*pricing.VendorCostRow, error) {
	return nil, nil
}

// priceOfSeed parses a float the way the wire would, exact at scale 4.
func priceOfSeed(f float64) httpx.Price {
	p, err := httpx.ParsePrice(strconv.FormatFloat(f, 'f', -1, 64))
	if err != nil {
		panic(err)
	}
	return p
}
