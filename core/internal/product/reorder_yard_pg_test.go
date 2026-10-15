// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// reorderWorld is two yards (A with a zone under it) and one product with a
// reorder point of 150.
type reorderWorld struct {
	t                   *testing.T
	repo                *product.PostgresRepository
	yardA, zoneA, yardB uuid.UUID
	prod                uuid.UUID
	sku                 string
}

func newReorderWorld(t *testing.T) *reorderWorld {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()
	w := &reorderWorld{t: t, repo: product.NewRepository(db), yardA: uuid.New(), zoneA: uuid.New(), yardB: uuid.New(), prod: uuid.New()}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO locations (id, type, code, name, branch_id) VALUES ($1, 'BRANCH', $2, 'Reorder Yard A', $1)`, w.yardA, "RYA-"+w.yardA.String()[:8])
	exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'ZONE', $2, $3, $3)`, w.zoneA, "RZA-"+w.zoneA.String()[:8], w.yardA)
	exec(`INSERT INTO locations (id, type, code, name, branch_id) VALUES ($1, 'BRANCH', $2, 'Reorder Yard B', $1)`, w.yardB, "RYB-"+w.yardB.String()[:8])
	w.sku = "RY-" + w.prod.String()[:8]
	exec(`INSERT INTO products (id, sku, description, uom_primary, base_price, reorder_point, reorder_qty) VALUES ($1, $2, 'Reorder yard hanger', 'EA', 4, 150, 200)`, w.prod, w.sku)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = db.Pool.Exec(bg, `DELETE FROM user_locations WHERE branch_id IN ($1, $2)`, w.yardA, w.yardB)
		_, _ = db.Pool.Exec(bg, `DELETE FROM inventory WHERE product_id = $1`, w.prod)
		_, _ = db.Pool.Exec(bg, `DELETE FROM products WHERE id = $1`, w.prod)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, w.zoneA)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id IN ($1, $2)`, w.yardA, w.yardB)
	})
	return w
}

func (w *reorderWorld) stock(loc *uuid.UUID, qty int) {
	w.t.Helper()
	if _, err := testutil.RequireDB(w.t).Pool.Exec(context.Background(),
		`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'row', $3, 0)`, w.prod, loc, qty); err != nil {
		w.t.Fatalf("stock: %v", err)
	}
}

// rows is this product's reorder rows for ctx as (yard, current stock).
func (w *reorderWorld) rows(ctx context.Context) map[string]float64 {
	w.t.Helper()
	alerts, err := w.repo.ListBelowReorder(ctx)
	if err != nil {
		w.t.Fatalf("list: %v", err)
	}
	out := map[string]float64{}
	for _, a := range alerts {
		if a.SKU != w.sku {
			continue
		}
		raw, _ := json.Marshal(a)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		yard, _ := m["branch_id"].(string)
		cur, _ := m["current_stock"].(float64)
		if cs, ok := m["current_stock"].(string); ok {
			f, _ := strconv.ParseFloat(cs, 64)
			cur = f
		}
		out[yard] = cur
	}
	return out
}

func (w *reorderWorld) as(bc *branchctx.Context) context.Context {
	if bc == nil {
		return context.Background()
	}
	return branchctx.With(context.Background(), bc)
}

// A product exactly at its reorder point at the chosen yard is listed: the
// comparison is at or below, as the low stock alert's.
func TestReorderList_AProductAtItsPointIsListed(t *testing.T) {
	w := newReorderWorld(t)
	w.stock(&w.yardA, 150)
	got := w.rows(w.as(&branchctx.Context{UserSub: "u", BranchID: &w.yardA}))
	if len(got) != 1 {
		t.Fatalf("rows = %v, want the yard's one row at its point", got)
	}
}

// 100 at yard A and 100 at yard B against a point of 150: each yard is listed
// for itself, and the company's 200 suppresses neither.
func TestReorderList_EachYardIsListedForItself(t *testing.T) {
	w := newReorderWorld(t)
	w.stock(&w.yardA, 100)
	w.stock(&w.yardB, 100)
	got := w.rows(w.as(&branchctx.Context{UserSub: "boss", IsAdmin: true}))
	if len(got) != 2 || got[w.yardA.String()] != 100 || got[w.yardB.String()] != 100 {
		t.Fatalf("rows by yard = %v, want yard A 100 and yard B 100", got)
	}
	system := w.rows(branchctx.WithSystem(context.Background()))
	if len(system) != 2 {
		t.Fatalf("the cron engine's rows by yard = %v, want two", system)
	}
}

// A yard whose stock sits in a zone is read as the yard's sum.
func TestReorderList_ABinsOnlyYardReadsItsSum(t *testing.T) {
	w := newReorderWorld(t)
	w.stock(&w.zoneA, 100)
	got := w.rows(w.as(&branchctx.Context{UserSub: "u", BranchID: &w.yardA}))
	if got[w.yardA.String()] != 100 {
		t.Fatalf("rows = %v, want yard A at the zone's 100", got)
	}
}

// A row with no location is in no yard's sum.
func TestReorderList_ALocationLessRowIsInNoYard(t *testing.T) {
	w := newReorderWorld(t)
	w.stock(&w.yardA, 100)
	w.stock(nil, 80)
	got := w.rows(w.as(&branchctx.Context{UserSub: "boss", IsAdmin: true}))
	if len(got) != 1 || got[w.yardA.String()] != 100 {
		t.Fatalf("rows = %v, want only yard A at 100 (the location-less 80 is in no yard)", got)
	}
}

// A user granted two yards, with no header, sees each granted yard's row; a
// user granted one sees only that yard's.
func TestReorderList_AGrantedUserSeesOnlyTheirYards(t *testing.T) {
	w := newReorderWorld(t)
	w.stock(&w.yardA, 100)
	w.stock(&w.yardB, 100)
	db := testutil.RequireDB(t)
	both, one := "ry-both-"+uuid.NewString()[:8], "ry-one-"+uuid.NewString()[:8]
	for sub, yards := range map[string][]uuid.UUID{both: {w.yardA, w.yardB}, one: {w.yardA}} {
		for i, y := range yards {
			if _, err := db.Pool.Exec(context.Background(),
				`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, $3, 'test')`, sub, y, i == 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM user_locations WHERE user_sub IN ($1, $2)`, both, one)
	})
	if got := w.rows(w.as(&branchctx.Context{UserSub: both})); len(got) != 2 {
		t.Errorf("two grants: rows = %v, want a row per granted yard", got)
	}
	if got := w.rows(w.as(&branchctx.Context{UserSub: one})); len(got) != 1 || got[w.yardA.String()] != 100 {
		t.Errorf("one grant: rows = %v, want only yard A", got)
	}
}

// A row at a location whose branch_id is null is in no yard's sum. The
// location trigger refuses such a location on insert, so only a legacy row
// makes one: the trigger is off for the one insert (a pin: the guard
// l.branch_id IS NOT NULL in pkg/yardstock is what keeps the row from
// grouping under a null yard and failing the scan).
func TestReorderList_ALocationWithNoBranchIsInNoYard(t *testing.T) {
	w := newReorderWorld(t)
	db := testutil.RequireDB(t)
	orphan := uuid.New()
	ctx := context.Background()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("trigger off: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BIN', $2)`, orphan, "RO-"+orphan.String()[:8]); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("orphan location: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM inventory WHERE location_id = $1`, orphan)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, orphan)
	})
	var branch *uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT branch_id FROM locations WHERE id = $1`, orphan).Scan(&branch); err != nil || branch != nil {
		t.Fatalf("orphan location branch = %v (err %v), want null", branch, err)
	}
	w.stock(&w.yardA, 100)
	w.stock(&orphan, 60)
	got := w.rows(w.as(&branchctx.Context{UserSub: "boss", IsAdmin: true}))
	if len(got) != 1 || got[w.yardA.String()] != 100 {
		t.Fatalf("rows = %v, want only yard A at 100 (the branchless location's 60 is in no yard)", got)
	}
}
