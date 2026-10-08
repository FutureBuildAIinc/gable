// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// The transaction proofs for C3-1's writes (the recipe's tx_test set). The
// module's writes are single conditional statements (the revision check and
// the write are one database act), proved by the wire tests' contender race;
// the one write that holds a transaction across statements is the category
// rules bulk upsert, which these tests exercise: a failing statement rolls
// the whole batch back, and as many contenders as pool connections, held
// inside their transactions at a gate, finish without a second connection.
// C3-1 writes no events (ADR 0006 gives pricing its events with C3-2A), so
// the failing-event-write proof has nothing to bite on here; that is stated
// in the pull request.

import (
	"context"
	"strconv"
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
	svc := pricing.NewCategoryPricingService(pricing.NewCategoryRepository(db))
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

	if err := svc.BulkUpsertRules(ctx, []pricing.CategoryPricingRule{good, broken}); err == nil {
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
	repo := pricing.NewCategoryRepository(db).WithTxRunner(newGatedRunner(db, contenders))
	svc := pricing.NewCategoryPricingService(repo)
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
			if err := svc.BulkUpsertRules(ctx, []pricing.CategoryPricingRule{rules[i]}); err != nil {
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
