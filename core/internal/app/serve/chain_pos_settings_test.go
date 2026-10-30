// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
)

// cashierSession resolves one session id to a cashier and refuses the rest.
type cashierSession struct{}

func (cashierSession) Resolve(_ context.Context, id string) (*middleware.UserClaims, error) {
	if id != "cashier" {
		return nil, middleware.ErrSessionInvalid
	}
	c := &middleware.UserClaims{Roles: []string{"cashier"}}
	c.Subject = "00000000-0000-0000-0000-0000000000c1"
	return c, nil
}

// A core with staff sessions answers PUT /pos/settings 401 with no
// credential and 403 for a session resolved cashier, through the real chain
// and the production pos wiring: the no claims bypass in
// PutPosSettingsHandler is the dev mode path, unreachable outside
// AUTH_MODE=dev (walk B P1 read, P3 1).
func TestPutPosSettingsIsRefusedOutsideDevMode(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	am, err := middleware.NewAuthMiddleware(context.Background(), middleware.AuthConfig{
		PublicPaths: publicAPIPaths,
		Sessions:    cashierSession{},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	newBranchWall(db).pos(mux, pos.NewHandler(pos.NewService(db, pos.NewRepository(db), logger)), "admin", "owner", "cashier")
	h := buildChain(ChainDeps{Mux: mux, DB: db, Logger: logger, AuthMw: am})

	// a valid body, so the 403 is the role refusal and not a 400 on decode
	const body = `{"revision":1,"rules":{"check_limit_cents":null,"check_id_required":{"mode":"never","over_cents":null},` +
		`"restocking_fees":{"defective":0,"damaged":0,"wrong_item":0,"ordered_wrong":0,"changed_mind":0,"overbought":0,"mis_tint":0,"other":0}}}`
	put := func(session string) int {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/pos/settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(middleware.CSRFHeaderName, "1")
		if session != "" {
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: session})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := put(""); got != http.StatusUnauthorized {
		t.Errorf("no credential = %d, want 401", got)
	}
	if got := put("cashier"); got != http.StatusForbidden {
		t.Errorf("a cashier session = %d, want 403", got)
	}
}
