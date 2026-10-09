// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// The payload branch rule on POST /api/v1/quotes (ADR 0007 section 2.3):
// a body's branch_id may not widen or move the branch context the caller
// holds. The quote create used to let the body override the context with no
// grant check, so a caller bound to branch A could write a quote at branch B.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// callerMiddleware stands in for the branch middleware: the headers X-Test-Sub,
// X-Test-Admin and X-Test-Branch set the branch context a real request would
// carry (a user, an administrator, a bound caller). No header is an unbound
// machine key: no sub, not an administrator, no branch.
func callerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bc := &branchctx.Context{UserSub: r.Header.Get("X-Test-Sub"), IsAdmin: r.Header.Get("X-Test-Admin") == "1"}
		if b := r.Header.Get("X-Test-Branch"); b != "" {
			id := uuid.MustParse(b)
			bc.BranchID = &id
		}
		next.ServeHTTP(w, r.WithContext(branchctx.With(r.Context(), bc)))
	})
}

func TestCreate_PayloadBranchRule(t *testing.T) {
	// The route under test writes quote events into events_outbox, which the
	// events and outbox packages' tests truncate under this lock; take it so
	// this test's writes and their truncates serialise across packages.
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()

	own, foreign := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{own, foreign} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, id, "QB-"+id.String()[:8]); err != nil {
			t.Fatalf("seed branch: %v", err)
		}
	}
	sub := "user-" + uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, sub, own); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, sub)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, own, foreign)
	})

	svc := quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithBranchGuard(middleware.NewBranchGuard(db))
	mux := http.NewServeMux()
	quote.NewHandler(svc).RegisterRoutes(mux)
	srv := httptest.NewServer(callerMiddleware(mux))
	t.Cleanup(srv.Close)
	f.srv = srv

	create := func(payloadBranch string, headers ...string) resp {
		body := f.createBody()
		if payloadBranch != "" {
			body["branch_id"] = payloadBranch
		}
		return f.do("POST", "/api/v1/quotes", body, headers...)
	}
	refused := func(name string, r resp) {
		t.Helper()
		if r.status != http.StatusForbidden {
			t.Fatalf("%s: status %d, want 403: %s", name, r.status, r.raw)
		}
		code, _, details := errorOf(t, r)
		if code != "forbidden" || len(details) == 0 || details[0]["field"] != "branch_id" {
			t.Fatalf("%s: want forbidden naming branch_id, got %s", name, r.raw)
		}
	}
	created := func(name string, r resp, want uuid.UUID) {
		t.Helper()
		if r.status != http.StatusCreated {
			t.Fatalf("%s: status %d, want 201: %s", name, r.status, r.raw)
		}
		if got := str(t, r.body, "branch_id"); got != want.String() {
			t.Fatalf("%s: quote at branch %s, want %s", name, got, want)
		}
	}

	// A user bound to one branch (the middleware set it from X-Branch-Id).
	bound := []string{"X-Test-Sub", sub, "X-Test-Branch", own.String()}
	refused("bound user, foreign payload", create(foreign.String(), bound...))
	created("bound user, own payload", create(own.String(), bound...), own)
	created("bound user, no payload", create("", bound...), own)

	// A user with no context branch may target only a granted branch.
	unbound := []string{"X-Test-Sub", sub}
	refused("granted user, ungranted payload", create(foreign.String(), unbound...))
	created("granted user, granted payload", create(own.String(), unbound...), own)

	// An administrator across branches may target any branch; one holding a
	// context branch is held to it like anyone.
	created("admin, no context", create(foreign.String(), "X-Test-Sub", "boss", "X-Test-Admin", "1"), foreign)
	refused("admin in a branch, other payload", create(foreign.String(), "X-Test-Sub", "boss", "X-Test-Admin", "1", "X-Test-Branch", own.String()))

	// A machine key: unbound reaches any branch; a bound one (a context
	// branch, as the key core sets it) is held to its branch.
	created("unbound key, any payload", create(foreign.String()), foreign)
	refused("bound key, foreign payload", create(foreign.String(), "X-Test-Branch", own.String()))
	created("bound key, own payload", create(own.String(), "X-Test-Branch", own.String()), own)
}
