// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// Migration 122 on rows that exist (ADR 0006 section 8, P1 to P6): the
// dealer's levels keep their customers and their price, the four fresh tier
// and default levels are created by id in system_settings, every customer's
// price after the migration equals the price before it to the ten
// thousandth, the tier category rules become LEVEL rules and PROFILE copies,
// the inert ones move aside with a reason, the contracts and rules carry
// their unit, and the vendor side gets its LIST level. Replacing the file
// with SELECT 1 fails every assertion here.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// scratch122 builds a scratch database with every migration before 122
// applied.
func scratch122(t *testing.T) *sql.DB {
	t.Helper()
	base, err := url.Parse(testutil.RequireDB(t).Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", base.String())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "gv1_m122_" + hex.EncodeToString(suffix)
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		admin.Close()
		t.Skipf("cannot create a scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name))
		admin.Close()
	})

	scratch := *base
	scratch.Path = "/" + name
	db, err := sql.Open("pgx", scratch.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := applyMigrationsBefore(db, "122_"); err != nil {
		t.Fatal(err)
	}
	return db
}

// applyMigrationsBefore applies every migration file that sorts before the
// given prefix, oldest first.
func applyMigrationsBefore(db *sql.DB, prefix string) error {
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(files) == 0 {
		return fmt.Errorf("no migrations found: %w", err)
	}
	sort.Strings(files)
	ctx := context.Background()
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_down.sql") || strings.Compare(base, prefix) >= 0 {
			continue
		}
		sqlText, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, string(sqlText)); err != nil {
			return fmt.Errorf("applying %s: %w", base, err)
		}
	}
	return nil
}

func applyMigrationFile(t *testing.T, db *sql.DB, pattern string) {
	t.Helper()
	files, err := filepath.Glob("../../migrations/" + pattern)
	if err != nil || len(files) != 1 {
		t.Fatalf("migration %s not found: %v", pattern, err)
	}
	sqlText, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), string(sqlText)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(files[0]), err)
	}
}

// mustExec runs a statement and fails the test on error, naming the SQL.
func mustExec(t *testing.T, db *sql.DB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(sql, args...); err != nil {
		t.Fatalf("%s: %v", oneLine(sql), err)
	}
}

// mustScalar reads one value; the test fails when the read does.
func mustScalar[T any](t *testing.T, db *sql.DB, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", oneLine(sql), err)
	}
	return v
}

func oneLine(sql string) string {
	return strings.Join(strings.Fields(sql), " ")
}

// scale4 rounds an exact rational once to scale 4, half away from zero, the
// engine's only rounding.
func scale4(r *big.Rat) *big.Rat {
	scaled := new(big.Rat).Mul(r, big.NewRat(10_000, 1))
	f := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	if !scaled.IsInt() {
		rem := new(big.Rat).Abs(new(big.Rat).Sub(scaled, new(big.Rat).SetInt(f)))
		if rem.Cmp(big.NewRat(1, 2)) >= 0 {
			if scaled.Sign() > 0 {
				f.Add(f, big.NewInt(1))
			} else {
				f.Sub(f, big.NewInt(1))
			}
		}
	}
	return new(big.Rat).Quo(new(big.Rat).SetInt(f), big.NewRat(10_000, 1))
}

func ratOfDecimal(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic("bad decimal " + s)
	}
	return r
}

// fxPct is p percent of r, exact.
func fxPct(r, p *big.Rat) *big.Rat {
	return new(big.Rat).Mul(r, new(big.Rat).Quo(p, big.NewRat(100, 1)))
}

// fxMarkdown is r less p percent, exact.
func fxMarkdown(r, p *big.Rat) *big.Rat {
	return new(big.Rat).Sub(r, fxPct(r, p))
}

// fxMarkup is r plus p percent, exact.
func fxMarkup(r, p *big.Rat) *big.Rat {
	return new(big.Rat).Add(r, fxPct(r, p))
}

// The fixture ids. Fixed ids keep the assertions readable; the migration
// never rewrites ids.
const (
	fxRootCategory  = "00000000-0122-4000-8000-00000000c001"
	fxChildCategory = "00000000-0122-4000-8000-00000000c002"
	fxSilverLevel   = "00000000-0122-4000-8000-00000000a001" // dealer "Silver", multiplier 0.95
	fxGoldLevel     = "00000000-0122-4000-8000-00000000a002" // dealer "Gold", multiplier 0.80
	fxPlatinumLevel = "00000000-0122-4000-8000-00000000a003" // dealer "Platinum", multiplier 0.93
	fxRetailA       = "00000000-0122-4000-8000-00000000a004" // dealer "Retail", multiplier 0.95
	fxRetailB       = "00000000-0122-4000-8000-00000000a005" // second "Retail", multiplier 1.05
	fxContractorLvl = "00000000-0122-4000-8000-00000000a006" // dealer "Contractor", multiplier 0.85
	fxP1            = "00000000-0122-4000-8000-00000000b001" // base 10.0000, root category, cost 6
	fxP2            = "00000000-0122-4000-8000-00000000b002" // base 1.3725, child category, cost 0.80
	fxP3            = "00000000-0122-4000-8000-00000000b003" // base 0, no cost
	fxVendor        = "00000000-0122-4000-8000-00000000d001"
	fxRulePromoP1   = "00000000-0122-4000-8000-00000000e001" // PROMOTIONAL markup 10 on p1
	fxRuleFixedP3   = "00000000-0122-4000-8000-00000000e002" // PROMOTIONAL fixed 0.25 on p3
	fxTierSilver    = "00000000-0122-4000-8000-00000000e011" // TIER SILVER MARKDOWN 10 root
	fxTierGold      = "00000000-0122-4000-8000-00000000e012" // TIER GOLD MARKDOWN 15 root
	fxTierPlat      = "00000000-0122-4000-8000-00000000e013" // TIER PLATINUM MARKDOWN 20 root
	fxTierRtl       = "00000000-0122-4000-8000-00000000e014" // TIER RETAIL MARKDOWN 5 (inert)
	fxTierWs        = "00000000-0122-4000-8000-00000000e015" // TIER WHOLESALE MARKDOWN 30 (inert)
	fxTierGoldChild = "00000000-0122-4000-8000-00000000e016" // TIER GOLD MARKUP 25 child
	fxContractC1P1  = "00000000-0122-4000-8000-00000000f001" // contract for M122-C01 on p1
	fxContractOrph  = "00000000-0122-4000-8000-00000000f002" // null customer, p2
)

// fxCustomers is every (level, tier) combination the engine distinguishes,
// under account numbers of their own.
var fxCustomers = []struct {
	acct, name, tier, level string
}{
	{"M122-C01", "m122 retail walkin", "RETAIL", "NULL"},
	{"M122-C02", "m122 silver walkin", "SILVER", "NULL"},
	{"M122-C03", "m122 gold walkin", "GOLD", "NULL"},
	{"M122-C04", "m122 platinum walkin", "PLATINUM", "NULL"},
	{"M122-C05", "m122 silver dealer retail", "RETAIL", fxSilverLevel},
	{"M122-C06", "m122 silver dealer silver", "SILVER", fxSilverLevel},
	{"M122-C07", "m122 gold dealer gold", "GOLD", fxGoldLevel},
	{"M122-C08", "m122 platinum dealer retail", "RETAIL", fxPlatinumLevel},
	{"M122-C09", "m122 retailA retail", "RETAIL", fxRetailA},
	{"M122-C10", "m122 retailB retail", "RETAIL", fxRetailB},
	{"M122-C11", "m122 contractor platinum", "PLATINUM", fxContractorLvl},
}

func fxCustomerID(acct string) string {
	for i, c := range fxCustomers {
		if c.acct == acct {
			return fmt.Sprintf("00000000-0122-4000-8000-0000000001%02d", i+1)
		}
	}
	panic("no fixture customer " + acct)
}

// plantFixture122 writes the pre migration world: dealer levels with tier
// like names at their own discounts, a second Retail, customers on every
// (level, tier) combination, tier category rules plus the two inert ones,
// products, a markup rule, a fixed price rule, contracts and a vendor.
func plantFixture122(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, fmt.Sprintf(`
		INSERT INTO product_categories (id, name, slug, path, sort_order, is_active) VALUES
			('%[1]s', 'm122 root', 'm122-root', 'm122root', 1, TRUE),
			('%[2]s', 'm122 child', 'm122-child', 'm122root.m122child', 1, TRUE);
		INSERT INTO price_levels (id, name, multiplier, created_at) VALUES
			('%[3]s', 'Silver',    0.9500, NOW() - INTERVAL '9 hours'),
			('%[4]s', 'Gold',      0.8000, NOW() - INTERVAL '8 hours'),
			('%[5]s', 'Platinum',  0.9300, NOW() - INTERVAL '7 hours'),
			('%[6]s', 'Retail',    0.9500, NOW() - INTERVAL '6 hours'),
			('%[7]s', 'Retail',    1.0500, NOW() - INTERVAL '5 hours'),
			('%[8]s', 'Contractor',0.8500, NOW() - INTERVAL '4 hours');
	`, fxRootCategory, fxChildCategory, fxSilverLevel, fxGoldLevel, fxPlatinumLevel, fxRetailA, fxRetailB, fxContractorLvl))

	for _, c := range fxCustomers {
		levelSQL := "NULL"
		if c.level != "NULL" {
			levelSQL = "'" + c.level + "'"
		}
		mustExec(t, db, fmt.Sprintf(`
			INSERT INTO customers (id, name, account_number, tier, price_level_id, primary_branch_id)
			VALUES ('%s', '%s', '%s', '%s'::customer_tier, %s, (SELECT id FROM locations LIMIT 1))`,
			fxCustomerID(c.acct), c.name, c.acct, c.tier, levelSQL))
	}

	mustExec(t, db, fmt.Sprintf(`
		INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost, category_id, created_at) VALUES
			('%[1]s', 'M122-P1', 'm122 product one', 'EA', 10.0000, 6.0000, '%[4]s', NOW() - INTERVAL '3 hours'),
			('%[2]s', 'M122-P2', 'm122 product two', 'EA', 1.3725, 0.8000, '%[5]s', NOW() - INTERVAL '3 hours'),
			('%[3]s', 'M122-P3', 'm122 product three', 'EA', 0.0000, 0.0000, '%[4]s', NOW() - INTERVAL '3 hours');
		INSERT INTO vendors (id, name) VALUES ('%[6]s', 'm122 vendor');
		UPDATE products SET vendor_id = '%[6]s' WHERE id = '%[1]s';
	`, fxP1, fxP2, fxP3, fxRootCategory, fxChildCategory, fxVendor))

	mustExec(t, db, fmt.Sprintf(`
		INSERT INTO pricing_rules (id, name, rule_type, product_id, fixed_price, markup_pct, min_quantity, is_active, priority, created_at) VALUES
			('%[1]s', 'm122 promo markup', 'PROMOTIONAL', '%[3]s', NULL, 10.0000, 0, TRUE, 10, NOW() - INTERVAL '2 hours'),
			('%[2]s', 'm122 promo fixed',  'PROMOTIONAL', '%[4]s', 0.2500, NULL,    0, TRUE, 10, NOW() - INTERVAL '2 hours');
		INSERT INTO category_pricing_rules (id, target_type, tier, category_id, rule_type, rule_value, is_active, priority, created_by, created_at) VALUES
			('%[5]s',  'TIER', 'SILVER',    '%[8]s',  'MARKDOWN', 10.0000, TRUE, 5, 'm122', NOW() - INTERVAL '2 hours'),
			('%[6]s',  'TIER', 'GOLD',      '%[8]s',  'MARKDOWN', 15.0000, TRUE, 5, 'm122', NOW() - INTERVAL '2 hours'),
			('%[7]s',  'TIER', 'PLATINUM',  '%[8]s',  'MARKDOWN', 20.0000, TRUE, 5, 'm122', NOW() - INTERVAL '2 hours'),
			('%[9]s',  'TIER', 'RETAIL',    '%[8]s',  'MARKDOWN',  5.0000, TRUE, 5, 'm122', NOW() - INTERVAL '2 hours'),
			('%[10]s', 'TIER', 'WHOLESALE', '%[8]s',  'MARKDOWN', 30.0000, TRUE, 5, 'm122', NOW() - INTERVAL '2 hours'),
			('%[11]s', 'TIER', 'GOLD',      '%[12]s', 'MARKUP',   25.0000, TRUE, 5, 'm122', NOW() - INTERVAL '2 hours');
	`, fxRulePromoP1, fxRuleFixedP3, fxP1, fxP3,
		fxTierSilver, fxTierGold, fxTierPlat, fxRootCategory,
		fxTierRtl, fxTierWs, fxTierGoldChild, fxChildCategory))

	mustExec(t, db, fmt.Sprintf(`
		INSERT INTO customer_contracts (id, customer_id, product_id, contract_price, created_at) VALUES
			('%[1]s', '%[3]s', '%[4]s', 9.5000, NOW() - INTERVAL '1 hour'),
			('%[2]s', NULL,    '%[5]s', 1.0000, NOW() - INTERVAL '1 hour');
	`, fxContractC1P1, fxContractOrph, fxCustomerID("M122-C01"), fxP1, fxP2))
}

// tierMultiplier is the hard coded multiplier of the pre migration engine.
func tierMultiplier(tier string) *big.Rat {
	switch tier {
	case "SILVER":
		return big.NewRat(90, 100)
	case "GOLD":
		return big.NewRat(85, 100)
	case "PLATINUM":
		return big.NewRat(80, 100)
	}
	return big.NewRat(1, 1)
}

// fixtureCombo reads the customer's pre migration level and tier.
func fixtureCombo(t *testing.T, db *sql.DB, acct string) (level *string, tier string) {
	t.Helper()
	var l string
	if err := db.QueryRow(`SELECT COALESCE(price_level_id::text, '') FROM customers WHERE account_number = $1`, acct).Scan(&l); err != nil {
		t.Fatalf("customer %s: %v", acct, err)
	}
	if err := db.QueryRow(`SELECT tier::text FROM customers WHERE account_number = $1`, acct).Scan(&tier); err != nil {
		t.Fatalf("customer %s: %v", acct, err)
	}
	if l != "" {
		level = &l
	}
	return level, tier
}

// fxProductCategory is the product's category in the fixture: p2 sits in the
// child category, everything else in the root.
func fxProductCategory(productID string) string {
	if productID == fxP2 {
		return fxChildCategory
	}
	return fxRootCategory
}

// fixtureBase reads the product's base price and cost.
func fixtureBase(t *testing.T, db *sql.DB, productID string) (base *big.Rat, cost *big.Rat) {
	t.Helper()
	var b, c string
	if err := db.QueryRow(`SELECT base_price::text, average_unit_cost::text FROM products WHERE id = $1`, productID).Scan(&b, &c); err != nil {
		t.Fatalf("product %s: %v", productID, err)
	}
	return ratOfDecimal(b), ratOfDecimal(c)
}

// fixtureContract reads the customer's contract price on the product, if the
// fixture holds one.
func fixtureContract(t *testing.T, db *sql.DB, acct, productID string) *big.Rat {
	t.Helper()
	var price sql.NullString
	err := db.QueryRow(`SELECT contract_price::text FROM customer_contracts WHERE customer_id = $1 AND product_id = $2`,
		fxCustomerID(acct), productID).Scan(&price)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		t.Fatalf("contract lookup: %v", err)
	}
	return ratOfDecimal(price.String)
}

// applyRuleValue computes one category or pricing rule's price exactly, the
// semantics both engines share.
func applyRuleValue(ruleType string, value *big.Rat, baseRat, costRat *big.Rat) (*big.Rat, bool) {
	switch ruleType {
	case "MARKDOWN":
		return fxMarkdown(baseRat, value), true
	case "MARKUP":
		if costRat.Sign() > 0 {
			return fxMarkup(costRat, value), true
		}
		return fxMarkup(baseRat, value), true
	case "FIXED":
		return value, true
	case "MARGIN":
		if costRat.Sign() > 0 && value.Cmp(big.NewRat(100, 1)) < 0 {
			return new(big.Rat).Quo(costRat, new(big.Rat).Sub(big.NewRat(1, 1), new(big.Rat).Quo(value, big.NewRat(100, 1)))), true
		}
		return baseRat, true
	}
	return nil, false
}

// fixtureCategoryRule reads the fixture's one matching rule for a tier, a
// profile or a level on the product's category, answering its type and
// value. The fixture keeps a two level tree, so exact and ancestor are the
// same lookup.
func fixtureCategoryRule(t *testing.T, db *sql.DB, targetWhere, targetKey, targetValue, productID string) (ruleType string, value *big.Rat, ok bool) {
	t.Helper()
	q := fmt.Sprintf(`SELECT rule_type, rule_value::text FROM category_pricing_rules
		WHERE target_type = %s AND %s = $1 AND is_active AND category_id = $2
		ORDER BY priority DESC LIMIT 1`, targetWhere, targetKey)
	var rt, v sql.NullString
	err := db.QueryRow(q, targetValue, fxProductCategory(productID)).Scan(&rt, &v)
	if err == sql.ErrNoRows || (err == nil && !rt.Valid) {
		return "", nil, false
	}
	if err != nil {
		t.Fatalf("category rule lookup: %v", err)
	}
	return rt.String, ratOfDecimal(v.String), true
}

// expectedPriceBefore computes the price the pre migration engine answered
// for one (customer, product) pair on the fixture: contract, then the
// pricing rules (a percent rule acts on the base price), then the tier
// category rules, then the level multiplier or the hard coded tier
// multiplier, then the base price. Only the winning step rounds, once, to
// scale 4. The customer's tier, level and the level's multiplier were read
// before the migration dropped the columns.
func expectedPriceBefore(t *testing.T, db *sql.DB, acct, tier string, mult *big.Rat, productID string) *big.Rat {
	t.Helper()
	baseRat, costRat := fixtureBase(t, db, productID)

	if price := fixtureContract(t, db, acct, productID); price != nil {
		return price
	}
	// Rung 2: the pricing rules.
	if productID == fxP1 {
		return scale4(fxMarkup(baseRat, big.NewRat(10, 1)))
	}
	if productID == fxP3 {
		return scale4(ratOfDecimal("0.25"))
	}
	// Rung 3: the tier category rules (skipped for RETAIL, as today).
	if tier != "RETAIL" {
		if rt, v, ok := fixtureCategoryRule(t, db, "'TIER'", "tier", tier, productID); ok {
			price, _ := applyRuleValue(rt, v, baseRat, costRat)
			return scale4(price)
		}
	}
	// Rung 4: the level multiplier, else the hard coded tier multiplier.
	if mult != nil {
		return scale4(new(big.Rat).Mul(baseRat, mult))
	}
	if tier != "RETAIL" {
		return scale4(new(big.Rat).Mul(baseRat, tierMultiplier(tier)))
	}
	return scale4(baseRat)
}

// priceAfterMigration reads the price the migrated data answers for one
// (customer, product) pair, computing the new waterfall over the migrated
// rows alone: contract, pricing rules, category rules by account (none on
// the fixture), profile then level, the profile's level derived from its
// basis, the branch base (none on the fixture), the base price. The engine
// test of the resolve commit runs the same pairs through pricing.Resolve;
// this one pins the migration itself.
func priceAfterMigration(t *testing.T, db *sql.DB, acct, productID string) *big.Rat {
	t.Helper()
	baseRat, costRat := fixtureBase(t, db, productID)

	if price := fixtureContract(t, db, acct, productID); price != nil {
		return price
	}
	if productID == fxP1 {
		return scale4(fxMarkup(baseRat, big.NewRat(10, 1)))
	}
	if productID == fxP3 {
		return scale4(ratOfDecimal("0.25"))
	}

	profileID := mustScalar[string](t, db, `SELECT price_profile_id::text FROM customers WHERE account_number = $1`, acct)
	levelID := mustScalar[string](t, db, `SELECT price_level_id::text FROM price_profiles WHERE id = $1`, profileID)
	for _, target := range []struct{ where, key, value string }{
		{"'PROFILE'", "price_profile_id", profileID},
		{"'LEVEL'", "price_level_id", levelID},
	} {
		rt, v, ok := fixtureCategoryRule(t, db, target.where, target.key, target.value, productID)
		if ok {
			price, _ := applyRuleValue(rt, v, baseRat, costRat)
			return scale4(price)
		}
	}

	// Rung 4: the profile's level, derived from its basis (every fixture
	// level is BASE off the reference price, which here is the base price).
	adjust := mustScalar[sql.NullString](t, db, `SELECT adjust_percent::text FROM price_levels WHERE id = $1`, levelID)
	p := big.NewRat(0, 1)
	if adjust.Valid {
		p = ratOfDecimal(adjust.String)
	}
	return scale4(fxMarkup(baseRat, p))
}

func TestMigration122BackfillPreservesEveryPrice(t *testing.T) {
	db := scratch122(t)
	plantFixture122(t, db)

	// The dealer's own level, tier and the level's multiplier for each
	// customer, read before the migration drops the columns.
	type beforeCombo struct {
		tier, level string
		mult        *big.Rat
	}
	before := map[string]beforeCombo{}
	for _, c := range fxCustomers {
		level, tier := fixtureCombo(t, db, c.acct)
		bc := beforeCombo{tier: tier, mult: nil}
		if level != nil {
			bc.level = *level
			mult := mustScalar[string](t, db, `SELECT multiplier::text FROM price_levels WHERE id = $1`, *level)
			bc.mult = ratOfDecimal(mult)
		}
		before[c.acct] = bc
	}

	// The price the pre migration engine answers for every (customer,
	// product) pair of the fixture, computed before the migration moves
	// anything.
	wantPrice := map[string]*big.Rat{}
	for _, c := range fxCustomers {
		for _, p := range []string{fxP1, fxP2, fxP3} {
			wantPrice[c.acct+"\x00"+p] = expectedPriceBefore(t, db, c.acct, before[c.acct].tier, before[c.acct].mult, p)
		}
	}

	applyMigrationFile(t, db, "122_price_levels_profiles_and_costs.sql")

	// P1: the dealer's levels carry a unique code, the basis, the exact
	// percent of their multiplier, a position; the multiplier is gone.
	type levelRow struct {
		code, name, basis, adjust string
	}
	levelByID := map[string]levelRow{}
	rows, err := db.Query(`SELECT id::text, code, name, basis, adjust_percent::text FROM price_levels ORDER BY position`)
	if err != nil {
		t.Fatalf("read price_levels after the migration: %v", err)
	}
	codePattern := regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,15}$`)
	seenCodes := map[string]bool{}
	for rows.Next() {
		var id string
		var l levelRow
		if err := rows.Scan(&id, &l.code, &l.name, &l.basis, &l.adjust); err != nil {
			t.Fatal(err)
		}
		if seenCodes[l.code] {
			t.Errorf("level code %s is not unique", l.code)
		}
		seenCodes[l.code] = true
		if !codePattern.MatchString(l.code) {
			t.Errorf("level code %s does not match the code vocabulary", l.code)
		}
		if l.basis != "BASE" {
			t.Errorf("level %s: basis %s, want BASE", l.code, l.basis)
		}
		levelByID[id] = l
	}
	rows.Close()

	wantDealer := map[string]string{
		fxSilverLevel: "-5.0000", fxGoldLevel: "-20.0000", fxPlatinumLevel: "-7.0000",
		fxRetailA: "-5.0000", fxRetailB: "5.0000", fxContractorLvl: "-15.0000",
	}
	for id, adjust := range wantDealer {
		l, ok := levelByID[id]
		if !ok {
			t.Fatalf("dealer level %s lost by the migration", id)
		}
		if l.adjust != adjust {
			t.Errorf("level %s (%s): adjust_percent %s, want %s (the exact percent of its multiplier)", l.code, l.name, l.adjust, adjust)
		}
	}
	wantCodes := map[string]string{
		fxSilverLevel: "SILVER", fxGoldLevel: "GOLD", fxPlatinumLevel: "PLATINUM",
		fxRetailA: "RETAIL", fxRetailB: "RETAIL1", fxContractorLvl: "CONTRACTOR",
	}
	for id, code := range wantCodes {
		if l, ok := levelByID[id]; !ok || l.code != code {
			t.Errorf("level %s carries code %q, want %q", id, l.code, code)
		}
	}
	if hasColumn(t, db, "price_levels", "multiplier") {
		t.Errorf("price_levels.multiplier is not dropped")
	}

	// P2: the four fresh levels, the settings and the default function.
	dealerLevels := fmt.Sprintf("('%s','%s','%s','%s','%s','%s')",
		fxSilverLevel, fxGoldLevel, fxPlatinumLevel, fxRetailA, fxRetailB, fxContractorLvl)
	freshID := func(code string) string {
		return mustScalar[string](t, db,
			`SELECT id::text FROM price_levels WHERE code = $1 AND id::text NOT IN `+dealerLevels, code)
	}
	silverFresh, goldFresh, platFresh := freshID("TIER_SILVER"), freshID("TIER_GOLD"), freshID("TIER_PLATINUM")
	defLevel := freshID("DEFAULT_RETAIL")
	for id, adjust := range map[string]string{silverFresh: "-10.0000", goldFresh: "-15.0000", platFresh: "-20.0000", defLevel: "0.0000"} {
		if got := levelByID[id].adjust; got != adjust {
			t.Errorf("fresh level %s: adjust %s, want %s", levelByID[id].code, got, adjust)
		}
	}
	setting := func(key string) string {
		return mustScalar[string](t, db, `SELECT value FROM system_settings WHERE key = $1`, key)
	}
	if got := setting("pricing.default_price_level_id"); got != defLevel {
		t.Errorf("pricing.default_price_level_id = %s, want the DEFAULT_RETAIL level %s", got, defLevel)
	}

	// P3: one profile per distinct (level, tier) combination, the backfill
	// map, and every customer on its profile. The combinations were counted
	// before the migration dropped the columns it read.
	profileCount := mustScalar[int](t, db, `SELECT count(*) FROM price_profiles`)
	seenCombos := map[string]bool{}
	for _, v := range before {
		seenCombos[v.tier+"\x00"+v.level] = true
	}
	comboCount := len(seenCombos)
	if profileCount != comboCount {
		t.Errorf("price_profiles holds %d rows for %d distinct (level, tier) combinations", profileCount, comboCount)
	}
	mapRows := mustScalar[int](t, db, `SELECT count(*) FROM price_profile_backfill`)
	if mapRows != comboCount {
		t.Errorf("price_profile_backfill holds %d rows, want %d", mapRows, comboCount)
	}
	defaultProfile := setting("pricing.default_price_profile_id")
	if fnProfile := mustScalar[string](t, db, `SELECT price_profile_default_id()::text`); fnProfile != defaultProfile {
		t.Errorf("price_profile_default_id() = %s, want the setting's %s", fnProfile, defaultProfile)
	}

	for _, c := range fxCustomers {
		var profileID, levelID, tier string
		if err := db.QueryRow(`SELECT c.price_profile_id::text, p.price_level_id::text, b.tier
			FROM customers c
			JOIN price_profiles p ON p.id = c.price_profile_id
			JOIN price_profile_backfill b ON b.price_profile_id = p.id
			WHERE c.account_number = $1`, c.acct).Scan(&profileID, &levelID, &tier); err != nil {
			t.Fatalf("customer %s: %v", c.acct, err)
		}
		b := before[c.acct]
		if b.tier != tier {
			t.Errorf("customer %s: profile tier %q, want %q", c.acct, tier, b.tier)
		}
		if b.level != "" && levelID != b.level {
			t.Errorf("customer %s: profile level %s, want its own level %s", c.acct, levelID, b.level)
		}
		if b.level == "" && b.tier == "RETAIL" && profileID != defaultProfile {
			t.Errorf("customer %s on no level at RETAIL: profile %s, want the default profile", c.acct, profileID)
		}
		if b.level == "" && b.tier != "RETAIL" && levelID == b.level {
			t.Errorf("customer %s on no level at %s: profile sits on a dealer level, want the fresh tier level", c.acct, b.tier)
		}
	}

	for _, c := range fxCustomers {
		for _, p := range []string{fxP1, fxP2, fxP3} {
			want := wantPrice[c.acct+"\x00"+p]
			got := priceAfterMigration(t, db, c.acct, p)
			if want.Cmp(got) != 0 {
				t.Errorf("customer %s product %s: price after the migration %s, want %s (the price before it, to the ten thousandth)",
					c.acct, p, got.FloatString(4), want.FloatString(4))
			}
		}
	}

	// The dropped columns and the enum.
	for _, col := range []string{"tier", "price_level_id"} {
		if hasColumn(t, db, "customers", col) {
			t.Errorf("customers.%s is not dropped", col)
		}
	}
	if typeExists(t, db, "customer_tier") {
		t.Errorf("the customer_tier type is not dropped")
	}
	if n := mustScalar[int](t, db, `SELECT count(*) FROM customers WHERE price_profile_id IS NULL`); n != 0 {
		t.Errorf("%d customers hold no profile", n)
	}

	// P4: the tier rules became LEVEL rules on the fresh tier levels and
	// PROFILE copies on the dealer level profiles; the inert ones moved
	// aside with a reason.
	// The counts are scoped to the fixture's categories: migration 050
	// seeded its own tier rules on the catalogue's other roots, and P4 maps
	// those too.
	if n := mustScalar[int](t, db, `SELECT count(*) FROM category_pricing_rules WHERE target_type = 'LEVEL' AND price_level_id = $1 AND category_id = $2 AND rule_type = 'MARKDOWN' AND rule_value = 10.0000`, silverFresh, fxRootCategory); n != 1 {
		t.Errorf("the SILVER tier rule did not become one LEVEL rule on the fresh SILVER level (n=%d)", n)
	}
	if n := mustScalar[int](t, db, `SELECT count(*) FROM category_pricing_rules WHERE target_type = 'LEVEL' AND price_level_id = $1 AND category_id IN ($2, $3)`, goldFresh, fxRootCategory, fxChildCategory); n != 2 {
		t.Errorf("the GOLD tier rules did not become two LEVEL rules on the fresh GOLD level (n=%d)", n)
	}
	if n := mustScalar[int](t, db, `SELECT count(*) FROM category_pricing_rules r
		JOIN price_profile_backfill b ON b.price_profile_id = r.price_profile_id
		WHERE r.target_type = 'PROFILE' AND b.tier = 'SILVER' AND b.price_level_id = $1 AND r.category_id = $2`, fxSilverLevel, fxRootCategory); n != 1 {
		t.Errorf("want one PROFILE copy of the SILVER rule on the (dealer level, SILVER) profile (n=%d)", n)
	}
	if n := mustScalar[int](t, db, `SELECT count(*) FROM category_pricing_rules_orphaned WHERE id = $1 AND reason <> ''`, fxTierRtl); n != 1 {
		t.Errorf("the inert RETAIL rule did not move to the orphaned table with a reason (n=%d)", n)
	}
	if n := mustScalar[int](t, db, `SELECT count(*) FROM category_pricing_rules_orphaned WHERE id = $1 AND reason <> ''`, fxTierWs); n != 1 {
		t.Errorf("the unknown tier rule did not move to the orphaned table with a reason (n=%d)", n)
	}
	if n := mustScalar[int](t, db, `SELECT count(*) FROM category_pricing_rules WHERE target_type = 'TIER'`); n != 0 {
		t.Errorf("TIER rules remain on the live table (n=%d)", n)
	}
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO category_pricing_rules (target_type, category_id, rule_type, rule_value) VALUES ('TIER', '%s', 'MARKDOWN', 1)`, fxRootCategory)); err == nil {
		t.Errorf("the target check still admits TIER")
	} else if !strings.Contains(err.Error(), "target_type") && !strings.Contains(err.Error(), "check constraint") {
		t.Errorf("inserting a TIER rule failed, but not through the new check: %v", err)
	}

	// P5: the product prices table, the contract columns and the orphaned
	// contract, the rule's price unit.
	if !hasTable(t, db, "product_prices") {
		t.Errorf("product_prices is missing")
	}
	if puom := mustScalar[string](t, db, `SELECT price_uom FROM customer_contracts WHERE id = $1`, fxContractC1P1); puom != "EA" {
		t.Errorf("the contract price unit is %q, want EA", puom)
	}
	var bid sql.NullString
	if err := db.QueryRow(`SELECT branch_id::text FROM customer_contracts WHERE id = $1`, fxContractC1P1).Scan(&bid); err != nil {
		t.Errorf("the contract branch column: %v", err)
	} else if bid.Valid {
		t.Errorf("the fixture contract carries a branch, want NULL (every branch)")
	}
	if n := mustScalar[int](t, db, `SELECT count(*) FROM customer_contracts_orphaned WHERE id = $1 AND customer_id IS NULL`, fxContractOrph); n != 1 {
		t.Errorf("the null customer contract did not move to customer_contracts_orphaned (n=%d)", n)
	}
	if puom := mustScalar[string](t, db, `SELECT price_uom FROM pricing_rules WHERE id = $1`, fxRuleFixedP3); puom != "EA" {
		t.Errorf("the fixed price rule's price unit is %q, want EA", puom)
	}
	var rpuom sql.NullString
	if err := db.QueryRow(`SELECT price_uom FROM pricing_rules WHERE id = $1`, fxRulePromoP1).Scan(&rpuom); err != nil || rpuom.Valid {
		t.Errorf("a percent rule carries no price unit, got %v (%v)", rpuom, err)
	}

	// The seed's kept upsert answers on the new key, twice in a row.
	upsert := `INSERT INTO customer_contracts (customer_id, product_id, contract_price, price_uom)
		SELECT c.id, p.id, 8.0000, p.uom_primary FROM customers c, products p
		WHERE c.account_number = 'M122-C02' AND p.id = '` + fxP1 + `'
		ON CONFLICT ON CONSTRAINT customer_contracts_customer_product_branch_key
		DO UPDATE SET contract_price = EXCLUDED.contract_price`
	mustExec(t, db, upsert)
	mustExec(t, db, upsert)
	if n := mustScalar[int](t, db, `SELECT count(*) FROM customer_contracts WHERE product_id = $1`, fxP1); n != 2 {
		t.Errorf("the upsert on the new key left %d contracts on p1", n)
	}

	// The hold: a raw branch base price in another unit is refused by the
	// trigger. The product first gains a second price unit, as a dealer's
	// unit set PUT would, so the composite FK does not answer first.
	mustExec(t, db, fmt.Sprintf(`INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
		VALUES ('%s', 'LF', 8, 1, TRUE, FALSE, TRUE)`, fxP1))
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO product_prices (product_id, price_level_id, branch_id, price_uom, unit_price)
		VALUES ('%s', NULL, (SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1), 'LF', 1.0000)`, fxP1)); err == nil {
		t.Errorf("a branch base price in a unit other than the stocking unit was admitted")
	} else if !strings.Contains(err.Error(), "stocking unit") {
		t.Errorf("the refusal does not name the stocking unit: %v", err)
	}

	// P6: one LIST level per vendor, no costs.
	if n := mustScalar[int](t, db, `SELECT count(*) FROM vendor_price_levels WHERE code = 'LIST' AND is_default`); n != 1 {
		t.Errorf("want exactly one default LIST vendor level (n=%d)", n)
	}
	if n := mustScalar[int](t, db, `SELECT count(*) FROM vendor_product_costs`); n != 0 {
		t.Errorf("vendor costs were backfilled (n=%d)", n)
	}
}

func TestMigration122NegativeMultiplierAborts(t *testing.T) {
	db := scratch122(t)
	mustExec(t, db, `INSERT INTO price_levels (id, name, multiplier) VALUES
		('00000000-0122-4000-8000-00000000aaaa', 'm122 negative level', -0.5000)`)
	files, err := filepath.Glob("../../migrations/122_price_levels_profiles_and_costs.sql")
	if err != nil || len(files) != 1 {
		t.Fatalf("migration 122 not found: %v", err)
	}
	sqlText, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, execErr := db.Exec(string(sqlText)); execErr == nil {
		t.Fatalf("the migration admitted a negative multiplier")
	} else if !strings.Contains(execErr.Error(), "m122 negative level") {
		t.Errorf("the refusal does not name the level: %v", execErr)
	}
}

func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	return mustScalar[int](t, db, `SELECT count(*) FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, table, column) > 0
}

func hasTable(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	return mustScalar[int](t, db, `SELECT count(*) FROM pg_tables WHERE tablename = $1`, table) > 0
}

func typeExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	return mustScalar[int](t, db, `SELECT count(*) FROM pg_type WHERE typname = $1`, name) > 0
}
