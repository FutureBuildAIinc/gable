// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/google/uuid"
)

// The aging by customer pages through its cursor: a page that is not the last
// answers a cursor the next page reads (the group id of a customer row is
// empty, which a cursor keyset part may not be).
func TestARAgingByCustomerPagesThroughItsCursor(t *testing.T) {
	w := newARWorld(t)
	today := w.businessDay()
	// A second customer owing, so a page of one has a next page.
	other := uuid.New()
	w.exec(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'AR Core Second Co', $2, `+arBranch+`)`,
		other, "ARC-"+uuid.NewString()[:8])
	w.invoice(10000, today.AddDate(0, 0, -3))
	w.invoiceOn(other, 20000, today.AddDate(0, 0, -3))

	mux := http.NewServeMux()
	account.NewHandler(w.svc).RegisterRoutes(mux, nil, nil)
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 100; pages++ {
		path := "/api/v1/ar/aging?group_by=customer&basis=due_date&limit=1"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
		var page struct {
			Items      []account.AgingItem `json:"items"`
			NextCursor *string             `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, it := range page.Items {
			seen[it.CustomerID.String()] = true
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
	}
	if !seen[w.customerID.String()] || !seen[other.String()] {
		t.Errorf("the pages hold %d customers, want both of the world's", len(seen))
	}
}

// A whole account read names its customer: with none it would age every
// customer with both walls dropped.
func TestARWholeAccountAgingRefusesToRunWithoutACustomer(t *testing.T) {
	w := newARWorld(t)
	w.invoice(10000, daysFromNow(-3))
	_, err := w.svc.AgingSummary(context.Background(), account.AgingQuery{AsOf: w.businessDay(), Basis: "due_date", WholeAccount: true})
	if err == nil {
		t.Fatal("a whole account aging with no customer was answered")
	}
}
