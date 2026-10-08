// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/notification"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/pkg/middleware"
)

// The HTTP surface added by the reporting-schedules and product-geometry
// ports. The frontend calls exactly these; a route that stops being registered
// is a silent 404 in the UI rather than a build failure, so it is pinned here.
//
// The product routes ride a registration call main.go already makes; the
// reporting ones go through wireReportSchedules, which main.go calls so the
// route registration and the scheduler attachment have one home. This test is
// what makes that claim checkable.
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
	wireReportSchedules(mux, reporting.NewHandler(nil), nil)
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

// CORRECTNESS: attaching an executor is the whole switch. The schedule API's
// claim that stored schedules run is read off the attached executor rather than
// stated independently, so this asserts the two halves of that: pass a
// scheduler and the handler reports execution enabled; pass nil and it does
// not. Nothing else in the tree may set that flag.
//
// This replaces a test that pinned the opposite — that no executor was ever
// attached — which was correct while reporting.Scheduler was known-broken and
// is now the wrong expectation: main.go attaches a working one. The
// API-response shape of the disclosure is asserted in
// internal/reporting/schedule_handler_test.go.
func TestWireReportSchedules_ExecutorDrivesTheExecutionDisclosure(t *testing.T) {
	t.Run("a scheduler enables execution", func(t *testing.T) {
		h := reporting.NewHandler(nil)
		wireReportSchedules(http.NewServeMux(), h, reporting.NewScheduler(nil, nil))

		if !h.ScheduleExecutionEnabled() {
			t.Error("an executor was wired but the handler still reports that schedules do not run")
		}
	})

	t.Run("no scheduler leaves the honest disclosure in place", func(t *testing.T) {
		h := reporting.NewHandler(nil)
		wireReportSchedules(http.NewServeMux(), h, nil)

		if h.ScheduleExecutionEnabled() {
			t.Error("the handler claims schedules run with no executor attached")
		}
	})
}

// The server wires notification.LogEmailService as its EmailService, and the
// scheduler passes that same value on as its EmailSender. This pins the
// composition — if the two interfaces drift apart, main.go stops compiling, but
// if someone swaps the email service for one that no longer satisfies
// reporting.EmailSender the failure should be here and legible.
func TestLogEmailServiceIsAUsableReportEmailSender(t *testing.T) {
	var sender reporting.EmailSender = notification.NewLogEmailService(
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := sender.SendEmailWithAttachment(context.Background(),
		[]string{"controller@example.com"}, "Scheduled Report: AR aging",
		"Attached.", "ar_aging.csv", []byte("Customer,Total\n")); err != nil {
		t.Fatalf("SendEmailWithAttachment: %v", err)
	}

	// And it tells the schedule API what it really does, so execution.delivery
	// cannot claim mail was sent when it was logged.
	desc := reporting.NewScheduler(nil, sender).DeliveryDescription()
	if !strings.Contains(desc, "log-only") {
		t.Errorf("delivery description = %q, want it to disclose that email is log-only", desc)
	}
}
