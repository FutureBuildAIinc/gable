// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tax

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func day(offset int) time.Time {
	return time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).AddDate(0, 0, offset)
}

func cert(active bool, effective, expiry *time.Time) *TaxExemption {
	return &TaxExemption{IsActive: active, EffectiveDate: *effective, ExpiryDate: expiry}
}

// RULE (CP-10): a certificate exempts a sale on a day when it is active,
// its first day is on or before that day, and its last day is that day or
// later. Inactive, expired, future and empty never exempt.
func TestExemptionsWindow(t *testing.T) {
	start := day(-30)
	cases := []struct {
		name string
		ex   *TaxExemption
		when time.Time
		want bool
	}{
		{"active open window", cert(true, &start, nil), day(0), true},
		{"active closed window ending today", cert(true, &start, ptr(day(0))), day(0), true},
		{"active window starting today", cert(true, ptr(day(0)), nil), day(0), true},
		{"expired yesterday", cert(true, &start, ptr(day(-1))), day(0), false},
		{"starts tomorrow", cert(true, ptr(day(1)), nil), day(0), false},
		{"inactive", cert(false, &start, nil), day(0), false},
		{"nil", nil, day(0), false},
	}
	for _, c := range cases {
		if got := Exemptions(c.ex, c.when); got != c.want {
			t.Errorf("%s: Exemptions = %t, want %t", c.name, got, c.want)
		}
	}
}

func ptr(t time.Time) *time.Time { return &t }

// RULE: the create route takes the certificate's own effective date, and the
// end route ends a certificate (inactive, kept on file) and answers 404 for
// an unknown one.
func TestExemptionEffectiveDateAndEndRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	repo := newMockExemptionRepo()
	svc := NewService(repo, nil, "", 0, logger)
	h := NewHandler(svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	customer := uuid.New()
	eff, exp := "2026-01-01", "2027-01-01"
	r := httptest.NewRequest("POST", "/api/v1/tax/exemptions", strings.NewReader(
		`{"customer_id":"`+customer.String()+`","exempt_reason":"RESALE","certificate_number":"CERT-R","effective_date":"`+eff+`","expiry_date":"`+exp+`"}`))
	r.SetPathValue("customerID", customer.String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	list, err := svc.GetExemptions(context.Background(), customer)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v, want the one certificate", list, err)
	}
	if got := list[0].EffectiveDate.Format("2006-01-02"); got != eff {
		t.Errorf("effective_date = %s, want %s", got, eff)
	}
	if got := list[0].ExpiryDate.Format("2006-01-02"); got != exp {
		t.Errorf("expiry_date = %s, want %s", got, exp)
	}

	r = httptest.NewRequest("POST", "/api/v1/tax/exemptions/"+list[0].ID.String()+"/end", nil)
	r.SetPathValue("id", list[0].ID.String())
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("end = %d: %s", w.Code, w.Body.String())
	}
	list, _ = svc.GetExemptions(context.Background(), customer)
	if len(list) != 1 || list[0].IsActive {
		t.Fatalf("after end = %+v, want the certificate kept and inactive", list)
	}

	r = httptest.NewRequest("POST", "/api/v1/tax/exemptions/"+uuid.NewString()+"/end", nil)
	r.SetPathValue("id", r.URL.Path[len("/api/v1/tax/exemptions/") : len(r.URL.Path)-4])
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("end unknown = %d, want 404", w.Code)
	}
}
