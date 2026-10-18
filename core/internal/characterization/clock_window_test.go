// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/dealerprofile"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// TestNormaliseRunTimingEta pins the normalisation that makes the route
// goldens independent of the wall clock near UTC midnight: an ETA is the
// server's now plus minutes, so the same script run at 23:59:50 and at midday
// must produce the same transcript. The end-of-run guard cannot catch it (the
// run ends before midnight; only the ETA crosses it).
func TestNormaliseRunTimingEta(t *testing.T) {
	// The captured instant sits just before midnight: every timestamp the run
	// writes after it counts from here.
	n := &normaliser{ids: map[string]string{}, seedDay: "2031-01-15", base: time.Date(2031, 1, 15, 23, 59, 50, 0, time.UTC)}
	for _, eta := range []string{
		"2031-01-15T12:15:00Z", // midday
		"2031-01-16T00:09:50Z", // 23:59:50 plus a 10 minute leg
		"2031-01-15T23:59:50-08:00",
	} {
		body := map[string]any{"eta": eta, "estimated_arrival": eta, "other": "2031-02-14T23:59:55Z"}
		got := n.value(body, "").(map[string]any)
		if got["eta"] != "<eta>" || got["estimated_arrival"] != "<eta>" {
			t.Errorf("eta %q normalised to %v / %v, want <eta>", eta, got["eta"], got["estimated_arrival"])
		}
		if got["other"] != "<ts+30d>" {
			t.Errorf("a field that is not run timing must keep its day offset, got %v", got["other"])
		}
	}
	if got := n.value(map[string]any{"eta": nil}, "").(map[string]any)["eta"]; got != nil {
		t.Errorf("a null eta must stay null, got %v", got)
	}
}

// TestNormaliseCountsFromTheCapturedInstant pins the day-boundary fix: with
// the capture taken just before UTC midnight, a timestamp the run writes just
// after midnight counts as the same day (+0d), not the next calendar day
// (+1d), because it falls inside the run's window from the captured instant,
// while everything outside that window (date arithmetic a month out, the
// seed's backdated fixtures, a value a full day later) keeps the calendar
// day count the goldens recorded.
func TestNormaliseCountsFromTheCapturedInstant(t *testing.T) {
	base := time.Date(2031, 1, 15, 23, 59, 30, 0, time.UTC)
	n := &normaliser{ids: map[string]string{}, seedDay: base.Format(dayLayout), base: base}
	for _, c := range []struct {
		ts   string
		want string
		why  string
	}{
		{"2031-01-15T23:59:45Z", "<ts+0d>", "written seconds after the capture, same side of midnight"},
		{"2031-01-16T00:00:20Z", "<ts+0d>", "written seconds after the capture, across midnight"},
		{"2031-01-16T00:09:50Z", "<ts+0d>", "written minutes after the capture, across midnight"},
		{"2031-02-14T23:59:45Z", "<ts+30d>", "a net-30 style due date keeps its day count"},
		{"2031-01-16T23:59:40Z", "<ts+1d>", "a full day later, still +1d"},
		{"2031-01-15T23:59:29Z", "<ts+0d>", "dated before the capture on the seed day, calendar count"},
		{"2031-01-15 00:05:00", "<ts+0d>", "the Postgres text form, dated before the capture on the seed day"},
		{"2030-11-01T00:00:00Z", "<ts-75d>", "a backdated fixture keeps its calendar day count"},
	} {
		if got := n.string(c.ts); got != c.want {
			t.Errorf("ts %q (%s) normalised to %s, want %s", c.ts, c.why, got, c.want)
		}
	}
}

// TestNormaliseSeedFixedDriverDates pins that the demo seed's hardcoded driver
// dates carry no seed-day offset: the same row normalises identically on any
// day the suite runs.
func TestNormaliseSeedFixedDriverDates(t *testing.T) {
	row := func() any {
		return map[string]any{"hire_date": "2022-04-15T00:00:00Z", "cdl_expiry": "2027-01-20T00:00:00Z"}
	}
	a := (&normaliser{ids: map[string]string{}, seedDay: "2031-01-15"}).value(row(), "")
	b := (&normaliser{ids: map[string]string{}, seedDay: "2031-01-16"}).value(row(), "")
	for _, got := range []any{a, b} {
		m := got.(map[string]any)
		if m["hire_date"] != "<ts>" || m["cdl_expiry"] != "<ts>" {
			t.Errorf("driver dates normalised to %v, want <ts>", m)
		}
	}
}

type stubDealerProfile struct {
	locale string
}

func (s stubDealerProfile) Get(context.Context) (*dealerprofile.Profile, error) {
	return &dealerprofile.Profile{
		LegalName:   "Granite Yard Holdings Ltd",
		TradingName: "Granite Yard",
		RemitTo:     "Remit Dept",
		Locale:      s.locale,
	}, nil
}

func (s stubDealerProfile) GetLogo(context.Context) (*dealerprofile.Logo, error) {
	return nil, nil
}

func renderInvoiceAndPickTicket(t *testing.T, locale string, base time.Time, lineTotalCents int64) (invPDF, pickPDF, quotePDF []byte) {
	t.Helper()
	svc := document.NewService(nil).WithProfile(stubDealerProfile{locale: locale})
	cust := &customer.Customer{Name: "Tarnwick Construction", AccountNumber: "TARNWICK-001"}
	sku, uom := "LUM-248-PREM", "PCS"
	qty := httpx.Quantity(10000)
	price := httpx.Price(lineTotalCents * 100)
	lineTotal := httpx.Cents(lineTotalCents)

	inv := &invoice.Invoice{
		InvoiceSummary: invoice.InvoiceSummary{
			ID:            uuid.New(),
			Number:        "IN-000001",
			InvoiceDate:   base.Format(dayLayout),
			SubtotalCents: lineTotal,
			TotalCents:    lineTotal,
		},
		Lines: []invoice.InvoiceLine{{
			Line: salesdoc.Line{
				SKU:         &sku,
				Description: "SPF Stud",
				Quantity:    &qty,
				UOM:         &uom,
				PriceUOM:    &uom,
				UnitPrice:   &price,
				LineTotal:   &lineTotal,
				LineType:    salesdoc.LineProduct,
			},
		}},
	}
	var err error
	invPDF, err = svc.GenerateInvoicePDF(context.Background(), inv, cust)
	if err != nil {
		t.Fatalf("generate invoice pdf: %v", err)
	}

	ord := &order.Order{
		OrderSummary: order.OrderSummary{
			ID:        uuid.New(),
			Number:    "SO-000001",
			CreatedAt: httpx.TimestampOf(base),
		},
		Lines: []order.OrderLine{{
			Line: salesdoc.Line{
				SKU:         &sku,
				Description: "SPF Stud",
				Quantity:    &qty,
				UOM:         &uom,
				LineType:    salesdoc.LineProduct,
			},
		}},
	}
	pickPDF, err = svc.GeneratePickTicketPDF(context.Background(), ord, cust)
	if err != nil {
		t.Fatalf("generate pick ticket pdf: %v", err)
	}

	exp := httpx.TimestampOf(base.AddDate(0, 0, 30))
	qt := &quote.Quote{
		QuoteSummary: quote.QuoteSummary{
			ID:         uuid.New(),
			Number:     "Q-000001",
			TotalCents: lineTotal,
			CreatedAt:  httpx.TimestampOf(base),
			ExpiresAt:  &exp,
		},
		Lines: []quote.QuoteLine{{
			SKU:         sku,
			Description: "SPF Stud",
			Quantity:    qty,
			UOM:         product.UOM(uom),
			PriceUOM:    uom,
			UnitPrice:   price,
			LineTotal:   lineTotal,
		}},
	}
	quotePDF, err = svc.GenerateQuotePDF(context.Background(), qt, cust)
	if err != nil {
		t.Fatalf("generate quote pdf: %v", err)
	}
	return invPDF, pickPDF, quotePDF
}

// TestNormaliseDocumentWordDates pins that binarySummary produces the same
// text_sha256 for a printed document across different seed days in every
// locale the document formatter supports, and that changing a line amount or
// date offset still changes the digest.
func TestNormaliseDocumentWordDates(t *testing.T) {
	savedDay, savedInstant := seedDay, seedInstant
	defer func() {
		seedDay, seedInstant = savedDay, savedInstant
	}()

	epoch := time.Unix(0, 0).UTC()
	var sampleDays []time.Time
	for m := 0; m < 12; m++ {
		sampleDays = append(sampleDays,
			epoch.AddDate(56, m, 4),
			epoch.AddDate(56, m, 17),
		)
	}

	locales := []string{"en-US", "en-CA", "en-GB", "en-NZ", "en-IE", "en-IN", "en-AU", "en-ZA", "", "fr-CA"}
	for _, locale := range locales {
		var wantInv, wantPick, wantQuote string
		for i, d := range sampleDays {
			seedInstant = d
			seedDay = d.Format(dayLayout)
			invPDF, pickPDF, quotePDF := renderInvoiceAndPickTicket(t, locale, d, 392997)
			gotInv := binarySummary(invPDF)["text_sha256"].(string)
			gotPick := binarySummary(pickPDF)["text_sha256"].(string)
			gotQuote := binarySummary(quotePDF)["text_sha256"].(string)
			if i == 0 {
				wantInv, wantPick, wantQuote = gotInv, gotPick, gotQuote
				continue
			}
			if gotInv != wantInv {
				t.Fatalf("locale %q: invoice text_sha256 differed between seed days (%s vs %s)", locale, gotInv, wantInv)
			}
			if gotPick != wantPick {
				t.Fatalf("locale %q: pick ticket text_sha256 differed between seed days (%s vs %s)", locale, gotPick, wantPick)
			}
			if gotQuote != wantQuote {
				t.Fatalf("locale %q: quote text_sha256 differed between seed days (%s vs %s)", locale, gotQuote, wantQuote)
			}
		}

		// Real content changes (a changed line amount or a shifted date offset)
		// must still change text_sha256.
		d := sampleDays[0]
		seedInstant = d
		seedDay = d.Format(dayLayout)
		invChangedAmt, _, _ := renderInvoiceAndPickTicket(t, locale, d, 392998)
		if got := binarySummary(invChangedAmt)["text_sha256"].(string); got == wantInv {
			t.Fatalf("locale %q: invoice text_sha256 did not change when line amount changed", locale)
		}
		invShiftedDay, _, _ := renderInvoiceAndPickTicket(t, locale, d.AddDate(0, 0, 1), 392997)
		if got := binarySummary(invShiftedDay)["text_sha256"].(string); got == wantInv {
			t.Fatalf("locale %q: invoice text_sha256 did not change when document date offset shifted", locale)
		}
	}
}

// TestWaitReadyRejectsStubAndRequiresCoreLiveResponse pins the server readiness
// check: the AI stub server must not answer GET /healthz/live with 200, and
// isCoreLiveResponse must reject a stub or foreign 200 response that lacks the
// core serve liveness payload and X-Request-ID header.
func TestWaitReadyRejectsStubAndRequiresCoreLiveResponse(t *testing.T) {
	stub := newAIStubServer()
	defer stub.Close()

	resp, err := http.Get(stub.URL + "/healthz/live")
	if err != nil {
		t.Fatalf("GET stub /healthz/live: %v", err)
	}
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("AI stub answered 200 to GET /healthz/live")
	}
	if isCoreLiveResponse(resp) {
		t.Fatalf("isCoreLiveResponse accepted AI stub response")
	}

	fakeOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"stub","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer fakeOK.Close()
	respFake, err := http.Get(fakeOK.URL + "/healthz/live")
	if err != nil {
		t.Fatalf("GET fakeOK /healthz/live: %v", err)
	}
	if isCoreLiveResponse(respFake) {
		t.Fatalf("isCoreLiveResponse accepted foreign 200 response")
	}

	realOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "req-1")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer realOK.Close()
	respReal, err := http.Get(realOK.URL + "/healthz/live")
	if err != nil {
		t.Fatalf("GET realOK /healthz/live: %v", err)
	}
	if !isCoreLiveResponse(respReal) {
		t.Fatalf("isCoreLiveResponse rejected valid core liveness response")
	}
}

