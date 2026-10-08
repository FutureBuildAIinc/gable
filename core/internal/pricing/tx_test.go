// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// The transaction proofs for C3-1's writes (the recipe's tx_test set). The
// writes that hold a transaction across statements are the category rule
// writes: create, update and delete each write the rule and its audit entry
// in one transaction, and the bulk upsert and bulk delete write many. These
// tests prove, for each kind: a failing statement (the audit write, the
// recipe's failing event write for a module whose write records an audit
// entry in place of an event) rolls the write back; three racers on one
// revision have one winner at pool size 4; and as many contenders as pool
// connections, held inside their transactions at a gate, finish without a
// second connection. C3-1 writes no outbox events (ADR 0006 gives pricing
// its events with C3-2A), which the pull request states.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// txCategoryID makes one category row for the bulk tests.
func txCategoryID(t *testing.T, db *database.DB) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.Pool.QueryRow(context.Background(),
		`INSERT INTO product_categories (name, slug, path) VALUES ('C3-1 tx test', 'c31_tx_test', 'c31_tx_test')
		 RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM product_categories WHERE id = $1`, id)
	})
	return id
}

func pctOf(f float64) *httpx.Quantity {
	q, err := httpx.ParseQuantity(strconv.FormatFloat(f, 'f', -1, 64))
	if err != nil {
		panic(err)
	}
	return &q
}

// TestBulkUpsert_FailingStatementRollsTheBatchBack: one rule row that cannot
// be written (a category that does not exist) refuses the whole batch, and
// the rules that would have been created are not there.
func TestBulkUpsert_FailingStatementRollsTheBatchBack(t *testing.T) {
	db := testutil.RequireDB(t)
	svc := pricing.NewCategoryPricingService(pricing.NewCategoryRepository(db)).WithTxRunner(db)
	ctx := context.Background()
	catID := txCategoryID(t, db)

	good := pricing.CategoryPricingRule{
		ID: uuid.New(), TargetType: pricing.TargetTypeTier, Tier: "GOLD",
		CategoryID: catID, RuleType: pricing.CategoryRuleMarkdown,
		ValuePct: pctOf(10), IsActive: true,
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM category_pricing_rules WHERE tier = 'GOLD' AND category_id = $1`, catID)
	})
	broken := good
	broken.ID = uuid.New()
	broken.Tier = "SILVER"
	broken.CategoryID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff") // no such category

	if err := svc.BulkUpsertRules(ctx, []pricing.CategoryPricingRule{good, broken}, nil); err == nil {
		t.Fatal("the batch with a foreign key violation succeeded")
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM category_pricing_rules WHERE id = ANY($1)`,
		[]uuid.UUID{good.ID, broken.ID}).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d of the batch's rows survived the rollback; the whole batch must go", n)
	}
}

// gatedRunner gates the transactions the bulk upsert runs in: the first
// `want` contenders meet inside their transactions before any statement
// runs, each holding its one connection while it waits.
type gatedRunner struct {
	db      *database.DB
	want    int32
	entered int32
	gate    sync.WaitGroup
}

func newGatedRunner(db *database.DB, want int) *gatedRunner {
	g := &gatedRunner{db: db, want: int32(want)}
	g.gate.Add(want)
	return g
}

func (g *gatedRunner) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return g.db.RunInTx(ctx, func(txCtx context.Context) error {
		if atomic.AddInt32(&g.entered, 1) <= g.want {
			g.gate.Done()
			g.gate.Wait()
		}
		return fn(txCtx)
	})
}

// TestBulkUpsert_SaturationNeedsNoSecondConnection: four contenders at pool
// size 4, each held inside its transaction at a gate before its first
// statement. A statement that reached for the pool would leave four holders
// each waiting for a fifth connection that never frees. This is the test
// that fails when code inside a transaction uses the pool.
func TestBulkUpsert_SaturationNeedsNoSecondConnection(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	catID := txCategoryID(t, db)

	const contenders = 4
	svc := pricing.NewCategoryPricingService(pricing.NewCategoryRepository(db)).WithTxRunner(newGatedRunner(db, contenders))
	rules := make([]pricing.CategoryPricingRule, contenders)
	for i := range rules {
		// Each contender its own tier: the table's partial unique index
		// refuses two active rules for one (tier, category).
		rules[i] = pricing.CategoryPricingRule{
			ID: uuid.New(), TargetType: pricing.TargetTypeTier,
			Tier: "SATURATION-" + strconv.Itoa(i), CategoryID: catID, RuleType: pricing.CategoryRuleMarkdown,
			ValuePct: pctOf(float64(5 + i)), IsActive: true,
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(),
			`DELETE FROM category_pricing_rules WHERE tier LIKE 'SATURATION-%'`)
	})

	var wg sync.WaitGroup
	errs := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := svc.BulkUpsertRules(ctx, []pricing.CategoryPricingRule{rules[i]}, nil); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("contender: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("four contenders at pool size 4 did not finish: a transaction waited on a second pool connection")
	}
}

// failingAudit is the real repository with an audit write that always fails:
// the stand-in for the failing event write of the recipe's first proof.
type failingAudit struct {
	*pricing.PostgresCategoryRepository
}

func (failingAudit) CreateAuditEntry(context.Context, *pricing.CategoryPricingAudit) error {
	return errors.New("audit write failed")
}

func newTierRule(catID uuid.UUID, tier string, pct float64) pricing.CategoryPricingRule {
	return pricing.CategoryPricingRule{
		ID: uuid.New(), TargetType: pricing.TargetTypeTier, Tier: tier,
		CategoryID: catID, RuleType: pricing.CategoryRuleMarkdown,
		ValuePct: pctOf(pct), IsActive: true,
	}
}

func ruleCount(t *testing.T, db *database.DB, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM category_pricing_rules WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRuleWrites_FailingAuditRollsTheWriteBack: a create, an update and a
// delete whose audit entry cannot be written leave the rule exactly as it
// was, and the bulk upsert whose audit fails leaves none of its rules.
func TestRuleWrites_FailingAuditRollsTheWriteBack(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	catID := txCategoryID(t, db)
	real := pricing.NewCategoryRepository(db)
	good := pricing.NewCategoryPricingService(real).WithTxRunner(db)
	bad := pricing.NewCategoryPricingService(failingAudit{real}).WithTxRunner(db)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM category_pricing_audit WHERE category_id = $1`, catID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM category_pricing_rules WHERE category_id = $1`, catID)
	})

	// create
	rule := newTierRule(catID, "TXA", 10)
	if err := bad.CreateCategoryRule(ctx, &rule); err == nil {
		t.Fatal("create with a failing audit write succeeded")
	}
	if n := ruleCount(t, db, rule.ID); n != 0 {
		t.Fatalf("the rule of a create whose audit failed is there (%d rows)", n)
	}

	// update and delete act on a rule that exists
	rule = newTierRule(catID, "TXB", 10)
	if err := good.CreateCategoryRule(ctx, &rule); err != nil {
		t.Fatal(err)
	}
	upd := rule
	upd.ValuePct = pctOf(25)
	rev := rule.Revision
	if err := bad.UpdateCategoryRule(ctx, &upd, pricing.Precondition{Revision: &rev}); err == nil {
		t.Fatal("update with a failing audit write succeeded")
	}
	var pct string
	var revision int64
	if err := db.Pool.QueryRow(ctx, `SELECT rule_value::text, revision FROM category_pricing_rules WHERE id = $1`, rule.ID).Scan(&pct, &revision); err != nil {
		t.Fatal(err)
	}
	if pct != "10.0000" || revision != rule.Revision {
		t.Fatalf("rule after a failed update = %s at revision %d, want 10.0000 at %d", pct, revision, rule.Revision)
	}
	if err := bad.DeleteCategoryRule(ctx, rule.ID, pricing.Precondition{Revision: &rev}); err == nil {
		t.Fatal("delete with a failing audit write succeeded")
	}
	if n := ruleCount(t, db, rule.ID); n != 1 {
		t.Fatalf("the rule of a delete whose audit failed is gone (%d rows)", n)
	}

	// bulk upsert and bulk delete
	a, b := newTierRule(catID, "TXC", 5), newTierRule(catID, "TXD", 6)
	if err := bad.BulkUpsertRules(ctx, []pricing.CategoryPricingRule{a, b}, nil); err == nil {
		t.Fatal("bulk upsert with a failing audit write succeeded")
	}
	if ruleCount(t, db, a.ID)+ruleCount(t, db, b.ID) != 0 {
		t.Fatal("rules of a bulk upsert whose audit failed are there")
	}
	if err := bad.BulkDeleteRules(ctx, []uuid.UUID{rule.ID}); err == nil {
		t.Fatal("bulk delete with a failing audit write succeeded")
	}
	if n := ruleCount(t, db, rule.ID); n != 1 {
		t.Fatalf("the rule of a bulk delete whose audit failed is gone (%d rows)", n)
	}
}

// TestRuleUpdate_ThreeRacersOneWinner: three updates built on one revision
// at pool size 4 have exactly one winner, the others 409 stale_revision, and
// the audit trail holds the one update.
func TestRuleUpdate_ThreeRacersOneWinner(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	catID := txCategoryID(t, db)
	svc := pricing.NewCategoryPricingService(pricing.NewCategoryRepository(db)).WithTxRunner(db)
	rule := newTierRule(catID, "RACE", 10)
	if err := svc.CreateCategoryRule(ctx, &rule); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM category_pricing_audit WHERE rule_id = $1`, rule.ID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM category_pricing_rules WHERE id = $1`, rule.ID)
	})

	const racers = 3
	var wins, stale int32
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			upd := rule
			upd.ValuePct = pctOf(float64(11 + i))
			rev := rule.Revision
			err := svc.UpdateCategoryRule(ctx, &upd, pricing.Precondition{Revision: &rev})
			var he *httpx.Error
			switch {
			case err == nil:
				atomic.AddInt32(&wins, 1)
			case errors.As(err, &he) && he.Code == "stale_revision":
				atomic.AddInt32(&stale, 1)
			default:
				t.Errorf("racer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 || stale != racers-1 {
		t.Fatalf("winners = %d, stale = %d, want 1 and %d", wins, stale, racers-1)
	}
	var audits int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM category_pricing_audit WHERE rule_id = $1 AND action = 'UPDATE'`, rule.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("%d UPDATE audit rows, want 1", audits)
	}
}

// TestRuleWrites_SaturationNeedsNoSecondConnection: for each kind of rule
// write (create, update, delete, bulk delete), as many contenders as pool
// connections are held inside their transactions at a gate before their
// first statement, and all finish. A statement that reached for the pool
// would deadlock the gate.
func TestRuleWrites_SaturationNeedsNoSecondConnection(t *testing.T) {
	kinds := []string{"create", "update", "delete", "bulk delete"}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			db := testutil.RequireDBMaxConns(t, 4)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			catID := txCategoryID(t, db)
			plain := pricing.NewCategoryPricingService(pricing.NewCategoryRepository(db)).WithTxRunner(db)
			const contenders = 4
			rules := make([]pricing.CategoryPricingRule, contenders)
			for i := range rules {
				rules[i] = newTierRule(catID, "SAT-"+strings.ReplaceAll(kind, " ", "-")+"-"+strconv.Itoa(i), float64(5+i))
				if kind != "create" {
					if err := plain.CreateCategoryRule(ctx, &rules[i]); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Cleanup(func() {
				_, _ = db.Pool.Exec(context.Background(), `DELETE FROM category_pricing_audit WHERE category_id = $1`, catID)
				_, _ = db.Pool.Exec(context.Background(), `DELETE FROM category_pricing_rules WHERE category_id = $1`, catID)
			})

			gated := pricing.NewCategoryPricingService(pricing.NewCategoryRepository(db)).WithTxRunner(newGatedRunner(db, contenders))
			var wg sync.WaitGroup
			errs := make(chan error, contenders)
			for i := 0; i < contenders; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					rev := rules[i].Revision
					var err error
					switch kind {
					case "create":
						err = gated.CreateCategoryRule(ctx, &rules[i])
					case "update":
						upd := rules[i]
						upd.ValuePct = pctOf(40)
						err = gated.UpdateCategoryRule(ctx, &upd, pricing.Precondition{Revision: &rev})
					case "delete":
						err = gated.DeleteCategoryRule(ctx, rules[i].ID, pricing.Precondition{Revision: &rev})
					case "bulk delete":
						err = gated.BulkDeleteRules(ctx, []uuid.UUID{rules[i].ID})
					}
					if err != nil {
						errs <- err
					}
				}(i)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Errorf("contender: %v", err)
			}
			if ctx.Err() != nil {
				t.Fatalf("%s: four contenders at pool size 4 did not finish: a transaction waited on a second pool connection", kind)
			}
		})
	}
}
