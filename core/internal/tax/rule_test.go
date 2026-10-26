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

// RULE (CP-10): the window's words follow the one rule
// (ExemptionCovering): a certificate exempts a sale on a day when it is
// active, its first day is on or before that day, and its last day is that
// day or later; StatusOf names each edge the screen shows.
func TestExemptionWindowStatus(t *testing.T) {
	start := day(-30)
	cases := []struct {
		name   string
		ex     *TaxExemption
		when   time.Time
		status string
	}{
		{"active open window", cert(true, &start, nil), day(0), "active"},
		{"active closed window ending today", cert(true, &start, ptr(day(0))), day(0), "active"},
		{"active window starting today", cert(true, ptr(day(0)), nil), day(0), "active"},
		{"expired yesterday", cert(true, &start, ptr(day(-1))), day(0), "expired"},
		{"starts tomorrow", cert(true, ptr(day(1)), nil), day(0), "future"},
		{"inactive", cert(false, &start, nil), day(0), "ended"},
		{"nil", nil, day(0), ""},
	}
	for _, c := range cases {
		if got := StatusOf(c.ex, c.when); got != c.status {
			t.Errorf("%s: StatusOf = %q, want %q", c.name, got, c.status)
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
	repo.customers[customer] = true
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
	r.SetPathValue("id", r.URL.Path[len("/api/v1/tax/exemptions/"):len(r.URL.Path)-4])
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("end unknown = %d, want 404", w.Code)
	}
}

// RULE (fix round): the create's rules live on the server, not only in the
// dialog: a bad date, an expiry before the effective day, a state that is
// not two letters, a missing certificate number and an unknown customer are
// each a 400 naming the field, and nothing is written.
func TestExemptionCreateValidates(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	repo := newMockExemptionRepo()
	svc := NewService(repo, nil, "", 0, logger)
	h := NewHandler(svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	customer := uuid.New()
	repo.customers[customer] = true
	post := func(body string) int {
		r := httptest.NewRequest("POST", "/api/v1/tax/exemptions", strings.NewReader(body))
		r.SetPathValue("customerID", customer.String())
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	base := `{"customer_id":"` + customer.String() + `","exempt_reason":"RESALE","certificate_number":"C-1"`
	if got := post(base + `,"effective_date":"31/12/2026"}`); got != http.StatusBadRequest {
		t.Errorf("bad effective_date = %d, want 400", got)
	}
	if got := post(base + `,"effective_date":"2026-01-01","expiry_date":"2025-12-31"}`); got != http.StatusBadRequest {
		t.Errorf("expiry before effective = %d, want 400", got)
	}
	if got := post(base + `,"issuing_state":"CTA"}`); got != http.StatusBadRequest {
		t.Errorf("three letter state = %d, want 400", got)
	}
	if got := post(`{"customer_id":"` + customer.String() + `","exempt_reason":"RESALE"}`); got != http.StatusBadRequest {
		t.Errorf("missing certificate number = %d, want 400", got)
	}
	if got := post(`{"customer_id":"` + uuid.NewString() + `","exempt_reason":"RESALE","certificate_number":"C-1"}`); got != http.StatusBadRequest {
		t.Errorf("unknown customer = %d, want 400", got)
	}
	if list, _ := svc.GetExemptions(context.Background(), customer); len(list) != 0 {
		t.Errorf("%d exemptions written by refused creates, want 0", len(list))
	}
}

// RULE (fix round): ending a certificate that was already ended answers 409
// already_ended, not a 404 as if it never existed, and the list carries the
// server's word for each certificate's standing.
func TestExemptionEndTwiceAnswersConflict(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	repo := newMockExemptionRepo()
	svc := NewService(repo, nil, "", 0, logger)
	mux := http.NewServeMux()
	NewHandler(svc).RegisterRoutes(mux)
	customer := uuid.New()
	repo.customers[customer] = true
	made, err := svc.SaveExemption(context.Background(), &CreateExemptionRequest{
		CustomerID: customer, ExemptReason: "RESALE", CertificateNumber: "C-2"})
	if err != nil {
		t.Fatal(err)
	}
	end := func() int {
		r := httptest.NewRequest("POST", "/api/v1/tax/exemptions/"+made.ID.String()+"/end", nil)
		r.SetPathValue("id", made.ID.String())
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if got := end(); got != http.StatusNoContent {
		t.Fatalf("first end = %d, want 204", got)
	}
	if got := end(); got != http.StatusConflict {
		t.Errorf("second end = %d, want 409", got)
	}
	list, err := svc.GetExemptions(context.Background(), customer)
	if err != nil || len(list) != 1 || list[0].Status != "ended" {
		t.Errorf("list = %+v, %v, want the one certificate with status ended", list, err)
	}
}
