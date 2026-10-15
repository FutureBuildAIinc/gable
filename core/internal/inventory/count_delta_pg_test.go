// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// CORRECTNESS (item 130, N20, round 3): a delta that names no location, from a
// caller with no branch context, would create a location less row (stock in no
// yard): the same defect as an absolute count with no location. It is refused
// 422 location_required in words, writing nothing.
func TestAdjust_ADeltaWithNoLocationIsRefusedWithoutABranchContext(t *testing.T) {
	granted := "u-granted-" + uuid.NewString()[:8]
	callers := map[string]func(w *countWorld) *branchctx.Context{
		"administrator with no header": func(*countWorld) *branchctx.Context {
			return &branchctx.Context{UserSub: "boss", IsAdmin: true}
		},
		"unbound key": func(*countWorld) *branchctx.Context { return nil },
		"granted user with no header": func(w *countWorld) *branchctx.Context {
			db := testutil.RequireDB(w.t)
			if _, err := db.Pool.Exec(context.Background(),
				`INSERT INTO user_locations (user_sub, branch_id) VALUES ($1, $2)`, granted, w.yard); err != nil {
				w.t.Fatalf("grant: %v", err)
			}
			return &branchctx.Context{UserSub: granted}
		},
	}
	for name, mk := range callers {
		t.Run(name, func(t *testing.T) {
			w := newCountWorld(t)
			w.stock(w.yard, 50, 0, true)
			code, body := w.count(mk(w), w.countBody(5, true))
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("delta with no location = %d, want 422: %s", code, body)
			}
			if w.rows() != 1 || w.sum() != 50 {
				t.Errorf("rows = %g, total = %g after a refused delta, want 1 row of 50", w.rows(), w.sum())
			}
		})
	}
}

// A user granted yard A who counts a product held only at yard B, naming no
// location, is refused (absolute or delta): nothing is written for a yard the
// user has no grant on, and no location less row appears.
func TestCount_AUserGrantedYardACountingAProductHeldAtYardBIsRefused(t *testing.T) {
	db := testutil.RequireDB(t)
	granted := "u-granted-" + uuid.NewString()[:8]
	for name, isDelta := range map[string]bool{"absolute": false, "delta": true} {
		t.Run(name, func(t *testing.T) {
			w := newCountWorld(t) // the product's yard is B
			yardA := uuid.New()
			if _, err := db.Pool.Exec(context.Background(),
				`INSERT INTO locations (id, type, code, branch_id) VALUES ($1, 'BRANCH', $2, $1)`, yardA, "CA-"+yardA.String()[:8]); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				bg := context.Background()
				_, _ = db.Pool.Exec(bg, `DELETE FROM user_locations WHERE branch_id = $1`, yardA)
				_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, yardA)
			})
			if _, err := db.Pool.Exec(context.Background(),
				`INSERT INTO user_locations (user_sub, branch_id) VALUES ($1, $2)`, granted, yardA); err != nil {
				t.Fatal(err)
			}
			w.stock(w.yard, 50, 0, true)
			code, body := w.count(&branchctx.Context{UserSub: granted}, w.countBody(45, isDelta))
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("%s no location = %d, want 422: %s", name, code, body)
			}
			if w.rows() != 1 || w.sum() != 50 {
				t.Errorf("rows = %g, total = %g, want 1 row of 50 untouched", w.rows(), w.sum())
			}
		})
	}
}
