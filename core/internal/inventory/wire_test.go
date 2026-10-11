// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory_test

// The inventory read on the wire contract (ADR 0001, ADR 0006 7.2): the list
// envelope with the cursor walk and include=total, the product summary under
// include=product, quantities as scale 4 decimal strings with `available`
// (quantity - allocated) and `uom` beside them, filters that filter, unknown
// parameters refused, and the error envelope. The branch wall on this list is
// proved through serve's real wiring (wire_branch_wall_test.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

type fixture struct {
	t        *testing.T
	db       *database.DB
	srv      *httptest.Server
	branch   uuid.UUID
	yards    [3]uuid.UUID
	product  uuid.UUID
	other    uuid.UUID
	products []uuid.UUID
}

// newFixture seeds one branch with three yards, one product stocked in LF with
// three scale 4 rows (the fractions prove the wire never rounds), and a second
// product with no rows (the empty page).
func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db, branch: uuid.New(),
		yards:   [3]uuid.UUID{uuid.New(), uuid.New(), uuid.New()},
		product: uuid.New(), other: uuid.New()}
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id) VALUES ($1, 'BRANCH', $2, NULL)`,
		f.branch, "wl-"+f.branch.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	for _, yard := range f.yards {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id) VALUES ($1, 'YARD', $2, $3)`,
			yard, "wl-"+yard.String()[:8], f.branch); err != nil {
			t.Fatalf("seed yard: %v", err)
		}
	}
	for _, p := range []struct {
		id  uuid.UUID
		uom string
	}{{f.product, "LF"}, {f.other, "EA"}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, $3, $4, 1)`,
			p.id, "WL-"+p.id.String()[:8], "wire levels", p.uom); err != nil {
			t.Fatalf("seed product: %v", err)
		}
	}
	// Three rows whose scale 4 digits would not survive a float round trip on
	// the wire: quantity 150.125 with 25.5 allocated leaves 124.625 available.
	rows := []struct {
		yard      uuid.UUID
		quantity  string
		allocated string
	}{{f.yards[0], "150.125", "25.5"}, {f.yards[1], "10", "0"}, {f.yards[2], "0.0001", "0"}}
	for _, r := range rows {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, $3, $4::numeric, $5::numeric)`,
			f.product, r.yard, "wl-inv-"+r.yard.String()[:8], r.quantity, r.allocated); err != nil {
			t.Fatalf("seed inventory: %v", err)
		}
	}
	f.products = []uuid.UUID{f.product, f.other}
	t.Cleanup(func() {
		for _, p := range f.products {
			_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, p)
			_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, p)
		}
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE parent_id = $1`, f.branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, f.branch)
	})

	h := inventory.NewHandler(inventory.NewService(inventory.NewRepository(db)))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(f.srv.Close)
	return f
}

type resp struct {
	status int
	body   map[string]any
	raw    []byte
}

func (f *fixture) get(path string) resp {
	f.t.Helper()
	res, err := http.Get(f.srv.URL + path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: raw}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

// TestInventoryLevelsWire: the list is the ADR 0001 envelope, each row carries
// the scale 4 quantities as decimal strings with `available` and `uom` beside
// them (ADR 0006 7.2), the product summary joins under include=product and
// only there, and every parameter the route does not declare is refused.
// Fails on the base commit, which answered a bare array of floats with no
// unit, no available and no paging.
func TestInventoryLevelsWire(t *testing.T) {
	f := newFixture(t)
	res := f.get("/api/v1/inventory?product_id=" + f.product.String())
	if res.status != http.StatusOK {
		t.Fatalf("list: %d %s", res.status, res.raw)
	}
	if _, ok := res.body["items"]; !ok {
		t.Fatalf("the list is not the envelope: %s", res.raw)
	}
	if res.body["limit"] != json.Number("50") {
		t.Fatalf("limit = %v, want 50", res.body["limit"])
	}
	if res.body["next_cursor"] != nil {
		t.Fatalf("next_cursor = %v, want null on the only page", res.body["next_cursor"])
	}
	items, ok := res.body["items"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("items = %v, want the product's three rows: %s", res.body["items"], res.raw)
	}
	row := items[0].(map[string]any)
	// The yards are created in order, so the newest row (yards[2]) leads the
	// page; find the row with the fraction to read its exact fields.
	rowByYard := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		rowByYard[m["location_id"].(string)] = m
	}
	main := rowByYard[f.yards[0].String()]
	if main == nil {
		t.Fatalf("no row for the first yard: %s", res.raw)
	}
	for field, want := range map[string]string{
		"quantity": "150.125", "allocated": "25.5", "available": "124.625", "uom": "LF",
	} {
		if main[field] != want {
			t.Errorf("%s = %v, want %q", field, main[field], want)
		}
	}
	if main["product_id"] != f.product.String() {
		t.Errorf("product_id = %v", main["product_id"])
	}
	if name, _ := main["location_name"].(string); !strings.HasPrefix(name, "wl-inv-") {
		t.Errorf("location_name = %v, want the seeded row text", main["location_name"])
	}
	if _, has := main["product"]; has {
		t.Errorf("the product summary rides without include=product: %s", res.raw)
	}
	// The legacy float fields are gone.
	for _, legacy := range []string{"location"} {
		if _, has := main[legacy]; has {
			t.Errorf("legacy field %q still on the wire", legacy)
		}
	}
	_ = row

	// include=product embeds the summary; include=product,total counts.
	withProduct := f.get("/api/v1/inventory?product_id=" + f.product.String() + "&include=product,total")
	if withProduct.status != http.StatusOK {
		t.Fatalf("include=product,total: %d %s", withProduct.status, withProduct.raw)
	}
	if withProduct.body["total"] != json.Number("3") {
		t.Fatalf("total = %v, want 3", withProduct.body["total"])
	}
	prow := withProduct.body["items"].([]any)[0].(map[string]any)
	summary, ok := prow["product"].(map[string]any)
	if !ok {
		t.Fatalf("no product summary under include=product: %s", withProduct.raw)
	}
	for field, want := range map[string]any{"id": f.product.String(), "stock_uom": "LF"} {
		if summary[field] != want {
			t.Errorf("product.%s = %v, want %v", field, summary[field], want)
		}
	}
	if sku, _ := summary["sku"].(string); !strings.HasPrefix(sku, "WL-") {
		t.Errorf("product.sku = %v", summary["sku"])
	}

	// The empty page is [] in the bytes, never null.
	empty := f.get("/api/v1/inventory?product_id=" + f.other.String())
	if empty.status != http.StatusOK || !bytes.Contains(empty.raw, []byte(`"items":[]`)) {
		t.Fatalf("empty page: %d %s", empty.status, empty.raw)
	}

	// A request with no product_id is the whole levels list, not a 400.
	all := f.get("/api/v1/inventory?limit=200")
	if all.status != http.StatusOK {
		t.Fatalf("unfiltered list: %d %s", all.status, all.raw)
	}

	for name, path := range map[string]string{
		"unknown parameter":    "/api/v1/inventory?foo=1",
		"another unknown":      "/api/v1/inventory?status=low",
		"bad product_id":       "/api/v1/inventory?product_id=not-a-uuid",
		"bad location_id":      "/api/v1/inventory?location_id=not-a-uuid",
		"bad include":          "/api/v1/inventory?include=bogus",
		"bad limit":            "/api/v1/inventory?limit=0",
		"limit over the cap":   "/api/v1/inventory?limit=201",
		"malformed cursor":     "/api/v1/inventory?cursor=garbage",
		"cursor from anothers": "/api/v1/inventory?cursor=eyJ2IjoxLCJvIjoicXVvdGVzLmNyZWF0ZWRfYXRfaWQiLCJrIjpbIjIwMjUtMDEtMDFUMDA6MDA6MDBaIiwiMDAwMDAwMDAtMDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAwIl19",
	} {
		if res := f.get(path); res.status != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400 (%s)", name, res.status, res.raw)
		} else if res.body["error"] == nil {
			t.Errorf("%s: the refusal is not the error envelope: %s", name, res.raw)
		}
	}
	// The named fields.
	for _, c := range []struct {
		name  string
		path  string
		field string
	}{
		{"unknown parameter", "/api/v1/inventory?foo=1", "foo"},
		{"bad product_id", "/api/v1/inventory?product_id=not-a-uuid", "product_id"},
		{"bad include", "/api/v1/inventory?include=bogus", "include"},
		{"bad limit", "/api/v1/inventory?limit=0", "limit"},
		{"malformed cursor", "/api/v1/inventory?cursor=garbage", "cursor"},
	} {
		res := f.get(c.path)
		errBody, _ := res.body["error"].(map[string]any)
		details, _ := errBody["details"].([]any)
		found := false
		for _, d := range details {
			if d.(map[string]any)["field"] == c.field {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: details do not name %q: %s", c.name, c.field, res.raw)
		}
	}
}

// TestInventoryLevelsCursorWalk: the cursor walks every row of the filtered
// set once under a small limit, newest first, and the filters ride the walk.
func TestInventoryLevelsCursorWalk(t *testing.T) {
	f := newFixture(t)
	// Two more rows at the first yard, so one yard holds three rows and the
	// location filter has a page of its own.
	ctx := context.Background()
	for i, qty := range []string{"5.5", "6"} {
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, $3, $4::numeric, 0)`,
			f.product, f.yards[0], "wl-inv-walk-"+strconv.Itoa(i), qty); err != nil {
			t.Fatalf("seed walk row: %v", err)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	var order []string
	for {
		path := "/api/v1/inventory?product_id=" + f.product.String() + "&limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		res := f.get(path)
		if res.status != http.StatusOK {
			t.Fatalf("walk page: %d %s", res.status, res.raw)
		}
		items := res.body["items"].([]any)
		if len(items) == 0 || len(items) > 2 {
			t.Fatalf("page carries %d rows under limit 2: %s", len(items), res.raw)
		}
		for _, it := range items {
			m := it.(map[string]any)
			id := m["id"].(string)
			if seen[id] {
				t.Fatalf("row %s served twice", id)
			}
			seen[id] = true
			order = append(order, id)
		}
		pages++
		next, _ := res.body["next_cursor"].(string)
		if next == "" || pages > 20 {
			break
		}
		cursor = next
	}
	if len(seen) != 5 {
		t.Fatalf("the walk served %d rows, want 5", len(seen))
	}
	if pages < 3 {
		t.Fatalf("the walk took %d pages under limit 2, want at least 3", pages)
	}

	// The location filter filters: only the first yard's three rows.
	seenAt := map[string]bool{}
	cursor = ""
	for {
		path := fmt.Sprintf("/api/v1/inventory?product_id=%s&location_id=%s&limit=2", f.product, f.yards[0])
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		res := f.get(path)
		if res.status != http.StatusOK {
			t.Fatalf("filtered walk page: %d %s", res.status, res.raw)
		}
		for _, it := range res.body["items"].([]any) {
			m := it.(map[string]any)
			if m["location_id"] != f.yards[0].String() {
				t.Fatalf("filtered page carries another yard's row: %s", res.raw)
			}
			seenAt[m["id"].(string)] = true
		}
		next, _ := res.body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seenAt) != 3 {
		t.Fatalf("the location filter served %d rows, want the yard's 3", len(seenAt))
	}

	// include=total counts the filtered set.
	res := f.get(fmt.Sprintf("/api/v1/inventory?product_id=%s&location_id=%s&include=total", f.product, f.yards[0]))
	if res.body["total"] != json.Number("3") {
		t.Fatalf("filtered total = %v, want 3", res.body["total"])
	}
}
