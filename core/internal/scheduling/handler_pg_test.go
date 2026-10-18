// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package scheduling

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

type stubBranchGuard struct {
	allowed uuid.UUID
}

func (g stubBranchGuard) CheckPayloadLocation(ctx context.Context, locationID uuid.UUID) error {
	if b := middleware.BranchIDForQuery(ctx); b != nil && *b != locationID {
		return fmt.Errorf("outside branch reach")
	}
	if g.allowed != uuid.Nil && g.allowed != locationID {
		return fmt.Errorf("outside branch reach")
	}
	return nil
}

func TestSchedulingHandler_CRUDAndBranchWall(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	yardA, yardB := uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO locations (id, type, code, name, timezone) VALUES
		($1, 'BRANCH', 'SCHA', 'Sched Yard A', 'America/Vancouver'),
		($2, 'BRANCH', 'SCHB', 'Sched Yard B', 'America/Toronto')
	`, yardA, yardB); err != nil {
		t.Fatalf("insert yards: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = db.Pool.Exec(c, `DELETE FROM yard_schedule_windows WHERE yard_id = ANY($1)`, []uuid.UUID{yardA, yardB})
		_, _ = db.Pool.Exec(c, `DELETE FROM locations WHERE id = ANY($1)`, []uuid.UUID{yardA, yardB})
	})

	repo := NewRepository(db)
	svc := NewService(repo)

	muxForBranch := func(branch uuid.UUID) *http.ServeMux {
		h := NewHandler(svc).WithBranchWall(stubBranchGuard{allowed: branch})
		mux := http.NewServeMux()
		guard := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c := r.Context()
				if branch != uuid.Nil {
					b := branch
					c = context.WithValue(c, branchctx.Key, &branchctx.Context{BranchID: &b})
				}
				next.ServeHTTP(w, r.WithContext(c))
			})
		}
		h.RegisterRoutes(mux, guard)
		return mux
	}

	muxA := muxForBranch(yardA)
	muxB := muxForBranch(yardB)

	call := func(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Yard A manager cannot create a window for Yard B (403).
	rec := call(muxA, "POST", "/api/v1/scheduling/windows", fmt.Sprintf(
		`{"yard_id":"%s","kind":"delivery","weekday":1,"start_time":"08:00","end_time":"12:00","capacity":4,"cutoff_minutes":60}`, yardB))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create on foreign yard answered %d, want 403", rec.Code)
	}

	// Yard A manager creates a window on Yard A (201), Yard B manager creates one on Yard B (201).
	rec = call(muxA, "POST", "/api/v1/scheduling/windows", fmt.Sprintf(
		`{"yard_id":"%s","kind":"delivery","weekday":1,"start_time":"08:00","end_time":"12:00","capacity":4,"cutoff_minutes":60}`, yardA))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create on own yard answered %d: %s", rec.Code, rec.Body.String())
	}
	var winA Window
	_ = json.Unmarshal(rec.Body.Bytes(), &winA)

	rec = call(muxB, "POST", "/api/v1/scheduling/windows", fmt.Sprintf(
		`{"yard_id":"%s","kind":"pickup","weekday":2,"start_time":"07:00","end_time":"10:00","capacity":3,"cutoff_minutes":30}`, yardB))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create B answered %d: %s", rec.Code, rec.Body.String())
	}
	var winB Window
	_ = json.Unmarshal(rec.Body.Bytes(), &winB)

	// Listing as Yard A manager only returns Yard A's windows, never Yard B's.
	rec = call(muxA, "GET", "/api/v1/scheduling/windows", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list A answered %d", rec.Code)
	}
	var listed []Window
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	for _, w := range listed {
		if w.YardID == yardB {
			t.Errorf("yard A manager listed yard B's window %s", w.ID)
		}
	}

	// Reading, updating or deleting Yard B's window as Yard A manager answers 404.
	if rec := call(muxA, "GET", "/api/v1/scheduling/windows/"+winB.ID.String(), ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET foreign window answered %d, want 404", rec.Code)
	}
	if rec := call(muxA, "PUT", "/api/v1/scheduling/windows/"+winB.ID.String(), `{"capacity":9}`); rec.Code != http.StatusNotFound {
		t.Errorf("PUT foreign window answered %d, want 404", rec.Code)
	}
	if rec := call(muxA, "DELETE", "/api/v1/scheduling/windows/"+winB.ID.String(), ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE foreign window answered %d, want 404", rec.Code)
	}

	// Updating and deactivating own window succeeds.
	rec = call(muxA, "PUT", "/api/v1/scheduling/windows/"+winA.ID.String(), `{"capacity":6}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT own window answered %d: %s", rec.Code, rec.Body.String())
	}
	rec = call(muxA, "DELETE", "/api/v1/scheduling/windows/"+winA.ID.String(), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE own window answered %d", rec.Code)
	}
}
