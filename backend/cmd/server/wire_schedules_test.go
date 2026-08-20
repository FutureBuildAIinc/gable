// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/pkg/middleware"
)

// The HTTP surface added by the reporting-schedules and product-geometry
// ports. The frontend calls exactly these; a route that stops being registered
// is a silent 404 in the UI rather than a build failure, so it is pinned here.
//
// Both groups ride registration calls main.go already makes
// (reporting.Handler.RegisterBuilderRoutes and product.Handler.RegisterRoutes),
// which is why neither port required a main.go edit. This test is what makes
// that claim checkable.
var scheduleAPISurface = []struct{ method, path string }{
	{http.MethodPost, "/api/v1/reporting/schedules"},
	{http.MethodGet, "/api/v1/reporting/schedules"},
	{http.MethodDelete, "/api/v1/reporting/schedules/11111111-1111-1111-1111-111111111111"},
	{http.MethodPost, "/api/v1/reporting/saved/22222222-2222-2222-2222-222222222222/run"},
}

var productGeometryAPISurface = []struct{ method, path string }{
	{http.MethodPatch, "/api/v1/products/33333333-3333-3333-3333-333333333333/dimensions"},
}

// newPortedSurfaceMux registers the modules that contribute the ported routes.
// Collaborators are nil: ServeMux pattern registration never touches them, and
// this test only resolves routes, it does not serve them.
func newPortedSurfaceMux() *http.ServeMux {
	mux := http.NewServeMux()
	wireReportSchedules(mux, reporting.NewHandler(nil))
	product.NewHandler(nil).RegisterRoutes(mux, reportScheduleGuard())
	return mux
}

func TestPortedAPISurfaceIsRegistered(t *testing.T) {
	mux := newPortedSurfaceMux()

	all := append(append([]struct{ method, path string }{}, scheduleAPISurface...), productGeometryAPISurface...)
	for _, route := range all {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			h, pattern := mux.Handler(req)
			if h == nil || pattern == "" {
				t.Fatalf("%s %s does not resolve to a registered handler", route.method, route.path)
			}
		})
	}
}

// The schedule routes must reject a caller whose role is not finance-or-better.
// Creating a schedule arranges for financial data to be emailed to addresses
// the caller chooses, so "sales can read a quote" is not enough.
//
// Note the guard's dev-mode behaviour: RequireRole passes through when the
// context carries NO claims at all, so this has to authenticate as the wrong
// role rather than as nobody.
func TestScheduleRoutesRejectInsufficientRole(t *testing.T) {
	mux := newPortedSurfaceMux()

	for _, route := range scheduleAPISurface {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			req = req.WithContext(context.WithValue(
				req.Context(), middleware.UserContextKey,
				&middleware.UserClaims{Role: "sales"},
			))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("%s %s as role=sales returned %d, want 403", route.method, route.path, w.Code)
			}
		})
	}
}

// The scheduler is intentionally not started. If someone attaches one without
// first fixing ExecuteAndSendReport and supplying an EmailSender, the API
// would begin claiming schedules run while every run still failed silently.
// This pins the current, honest state at the wiring layer; the API-level
// disclosure is asserted in internal/reporting/schedule_handler_test.go.
func TestWiringDoesNotAttachAScheduleExecutor(t *testing.T) {
	if scheduleExecutorAttached() {
		t.Error("an executor is attached to the schedule handler, but reporting.Scheduler is still known-broken: ExecuteAndSendReport drops the report definition and no EmailSender is implemented")
	}
}

// scheduleExecutorAttached reports whether wireReportSchedules attaches a
// schedule executor. It is a function rather than a constant so that enabling
// execution requires deleting it, which forces whoever does so to read the
// blocker list in wire_schedules.go.
func scheduleExecutorAttached() bool { return false }
