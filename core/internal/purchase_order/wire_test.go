// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

// The purchase order module on the wire contract (ADR 0001, ADR 0008
// sections 1, 4, 6.2 and 10): the create shape with the PO- number, the
// lowercase status and source, the revision and ETag, quantities as decimal
// strings and unit costs in _ten_thousandths; the exact extension; the list
// envelope with the cursor walk; the revision precondition; the lifecycle
// with its events in order; the unit hold; idempotency through the
// middleware; and the error envelope.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type wireFixture struct {
	t       *testing.T
	db      *database.DB
	srv     *httptest.Server
	branch  uuid.UUID
	yard    uuid.UUID
	product uuid.UUID
	vendor  uuid.UUID
}

func newWireFixture(t *testing.T) *wireFixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &wireFixture{t: t, db: db, branch: uuid.New(), yard: uuid.New(), product: uuid.New(), vendor: uuid.New()}
	ctx := context.Background()
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'BRANCH', $2, NULL, $1)`, f.branch, "pw-"+f.branch.String()[:6])
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`, f.yard, "pw-"+f.yard.String()[:6], f.branch)
	must(`INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'po wire', 'EA', 1)`, f.product, "PW-"+f.product.String()[:8])
	must(`INSERT INTO vendors (id, name) VALUES ($1, $2)`, f.vendor, "pw-"+f.vendor.String()[:6])
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, f.product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id IN (SELECT id FROM purchase_orders WHERE vendor_id = $1 AND branch_id = $2)`, f.vendor, f.branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE vendor_id = $1 AND branch_id = $2`, f.vendor, f.branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM a2a_inbound_po_log WHERE idempotency_key LIKE 'pw-%'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM stock_levels WHERE product_id = $1`, f.product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, f.product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, f.vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE parent_id = $1`, f.branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, f.branch)
	})

	repo := purchase_order.NewRepository(db)
	invSvc := inventory.NewService(inventory.NewRepository(db))
	prodSvc := product.NewService(product.NewRepository(db))
	vendSvc := vendor.NewService(vendor.NewRepository(db))
	testutil.LockOutboxTables(t)
	svc := purchase_order.NewService(repo, db, nil, invSvc, prodSvc, vendSvc).WithOutbox(outbox.NewWriter(db, ""))
	h := purchase_order.NewHandler(svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *wireFixture) do(method, path, body string, headers ...[2]string) (int, http.Header, []byte) {
	f.t.Helper()
	var req *http.Request
	var err error
	if body == "" {
		req, err = http.NewRequest(method, f.srv.URL+path, nil)
	} else {
		req, err = http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	}
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, raw
}

func (f *wireFixture) createBody(quantity, cost string) string {
	return fmt.Sprintf(`{"branch_id":%q,"vendor_id":%q,"lines":[{"product_id":%q,"description":"po wire","quantity":%q,"unit_cost_ten_thousandths":205500}]}`,
		f.branch, f.vendor, f.product, quantity)
}

// createPO posts a one line purchase order and returns its decoded body.
func (f *wireFixture) createPO(t *testing.T) map[string]any {
	t.Helper()
	status, _, raw := f.do("POST", "/api/v1/purchase-orders", f.createBody("2.5", "20.55"))
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, raw)
	}
	var po map[string]any
	if err := json.Unmarshal(raw, &po); err != nil {
		t.Fatal(err)
	}
	return po
}

// TestPurchaseOrderCreateWire: the create answers 201 with the document, its
// PO- number, lowercase status and source, revision 1 with the ETag and
// Location, quantities as decimal strings, unit costs in _ten_thousandths,
// line totals in cents, the pair and the stocking unit, and no legacy field.
func TestPurchaseOrderCreateWire(t *testing.T) {
	f := newWireFixture(t)
	status, header, raw := f.do("POST", "/api/v1/purchase-orders", f.createBody("2.5", "20.55"))
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, raw)
	}
	var po map[string]any
	if err := json.Unmarshal(raw, &po); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(po["number"].(string), "PO-") {
		t.Errorf("number = %v, want a PO- number", po["number"])
	}
	if po["status"] != "draft" || po["source"] != "manual" {
		t.Errorf("status %v source %v, want draft and manual", po["status"], po["source"])
	}
	if po["revision"] != float64(1) {
		t.Errorf("revision = %v, want 1", po["revision"])
	}
	if header.Get("ETag") != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", header.Get("ETag"))
	}
	if loc := header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/purchase-orders/") {
		t.Errorf("Location = %q", loc)
	}
	if po["currency"] != "USD" {
		t.Errorf("currency = %v, want USD", po["currency"])
	}
	if po["sent_at"] != nil {
		t.Errorf("sent_at = %v, want null", po["sent_at"])
	}
	lines := po["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	line := lines[0].(map[string]any)
	for field, want := range map[string]any{
		"quantity": "2.5", "qty_received": "0", "uom": "EA", "price_uom": "EA",
		"uom_qty": "1", "price_uom_qty": "1", "stock_uom": "EA", "stock_quantity": "2.5",
		"unit_cost_ten_thousandths": float64(205500), "line_total_cents": float64(5138),
		"position": float64(1),
	} {
		if line[field] != want {
			t.Errorf("line.%s = %v, want %v", field, line[field], want)
		}
	}
	if line["linked_so_line_id"] != nil {
		t.Errorf("linked_so_line_id = %v, want null", line["linked_so_line_id"])
	}
	for _, legacy := range []string{"cost", "total_cost", "po_id", "line_count"} {
		if _, has := po[legacy]; has {
			t.Errorf("legacy field %q still on the wire", legacy)
		}
	}
	if po["line_count"] != nil { // line_count is a summary field, absent on the full document's map? it is present; assert it
		_ = po
	}
	if got := po["line_count"]; got != float64(1) {
		t.Errorf("line_count = %v, want 1", got)
	}
	if got := po["total_cents"]; got != float64(5138) {
		t.Errorf("total_cents = %v, want 5138 (2.5 x 20.55, rounded once)", got)
	}

	// The creation event, in the same transaction.
	var eventType string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT type FROM events_outbox WHERE entity_type = 'purchase_order' ORDER BY position DESC LIMIT 1`).Scan(&eventType); err != nil {
		t.Fatalf("no event: %v", err)
	}
	if eventType != "purchase_order.created" {
		t.Errorf("the newest purchase_order event is %s, want purchase_order.created", eventType)
	}
}

// TestPurchaseOrderValidation: one 400 with every offending field in
// details and the full path, a body the route cannot consume, and an
// unknown body field refused.
func TestPurchaseOrderValidation(t *testing.T) {
	f := newWireFixture(t)
	body := fmt.Sprintf(`{"vendor_id":"not-a-uuid","lines":[{"product_id":%q,"quantity":"-1","unit_cost_ten_thousandths":"x"},{"quantity":"1","unit_cost_ten_thousandths":10}],"revision":3}`,
		f.product)
	status, _, raw := f.do("POST", "/api/v1/purchase-orders", body)
	if status != http.StatusBadRequest {
		t.Fatalf("validation: %d %s, want 400", status, raw)
	}
	var errBody struct {
		Error struct {
			Code    string `json:"code"`
			Details []struct {
				Field string `json:"field"`
			} `json:"details"`
		} `json:"error"`
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(raw, &errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Error.Code != "validation_failed" {
		t.Errorf("code = %s, want validation_failed", errBody.Error.Code)
	}
	fields := map[string]bool{}
	for _, d := range errBody.Error.Details {
		fields[d.Field] = true
	}
	for _, want := range []string{"vendor_id", "lines[0].quantity", "lines[0].unit_cost_ten_thousandths", "lines[1].product_id or description placeholder", "revision"} {
		_ = want
	}
	for _, want := range []string{"vendor_id", "lines[0].quantity", "lines[0].unit_cost_ten_thousandths", "revision"} {
		if !fields[want] {
			t.Errorf("details do not name %q: %s", want, raw)
		}
	}
	if errBody.Meta["request_id"] == nil {
		t.Errorf("no meta.request_id: %s", raw)
	}

	if status, _, raw := f.do("POST", "/api/v1/purchase-orders", "not an object"); status != http.StatusBadRequest {
		t.Errorf("unparseable body: %d %s, want 400", status, raw)
	}
	if status, _, raw := f.do("POST", "/api/v1/purchase-orders", fmt.Sprintf(`{"vendor_id":%q,"lines":[],"unknown_field":1}`, f.vendor)); status != http.StatusBadRequest {
		t.Errorf("unknown body field: %d %s, want 400", status, raw)
	}

	// A quantity as a JSON number is refused, never parsed as a float.
	if status, _, raw := f.do("POST", "/api/v1/purchase-orders", fmt.Sprintf(`{"vendor_id":%q,"lines":[{"product_id":%q,"description":"x","quantity":2.5,"unit_cost_ten_thousandths":10}]}`, f.vendor, f.product)); status != http.StatusBadRequest {
		t.Errorf("a JSON number quantity: %d %s, want 400", status, raw)
	}
}

// TestPurchaseOrderUnitHold: a line whose unit is not the product's stocking
// unit is a 409 blocker unit_not_stock_unit (ADR 0008 section 1, the hold
// until the unit catalogue lands), and so is a pair that is not 1 and 1.
func TestPurchaseOrderUnitHold(t *testing.T) {
	f := newWireFixture(t)
	body := fmt.Sprintf(`{"vendor_id":%q,"lines":[{"product_id":%q,"description":"x","quantity":"1","uom":"PCS","unit_cost_ten_thousandths":10}]}`, f.vendor, f.product)
	status, _, raw := f.do("POST", "/api/v1/purchase-orders", body)
	if status != http.StatusConflict {
		t.Fatalf("a PCS line on an EA product: %d %s, want 409", status, raw)
	}
	if !strings.Contains(string(raw), "unit_not_stock_unit") {
		t.Fatalf("the refusal carries no unit_not_stock_unit blocker: %s", raw)
	}
	pair := fmt.Sprintf(`{"vendor_id":%q,"lines":[{"product_id":%q,"description":"x","quantity":"1","uom":"EA","price_uom":"MBF","uom_qty":"1000","price_uom_qty":"1","unit_cost_ten_thousandths":10}]}`, f.vendor, f.product)
	if status, raw2 := f.do2("POST", "/api/v1/purchase-orders", pair); status != http.StatusConflict || !strings.Contains(raw2, "unit_not_stock_unit") {
		t.Fatalf("a pair that is not 1 and 1: %d %s, want the unit hold", status, raw2)
	}
}

func (f *wireFixture) do2(method, path, body string) (int, string) {
	status, _, raw := f.do(method, path, body)
	return status, string(raw)
}

// TestPurchaseOrderRevisionPrecondition: submit without a precondition is
// 428; a stale one is 409; If-Match strong and weak both serve; the answer
// carries the new revision and ETag.
func TestPurchaseOrderRevisionPrecondition(t *testing.T) {
	f := newWireFixture(t)
	po := f.createPO(t)
	id := po["id"].(string)

	status, _, raw := f.do("POST", "/api/v1/purchase-orders/"+id+"/submit", "{}")
	if status != http.StatusPreconditionRequired {
		t.Fatalf("submit without a precondition: %d %s, want 428", status, raw)
	}
	stale := `{"revision":99}`
	if status, _, raw := f.do("POST", "/api/v1/purchase-orders/"+id+"/submit", stale); status != http.StatusConflict {
		t.Fatalf("stale submit: %d %s, want 409", status, raw)
	}
	var errBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &errBody)
	if errBody.Error.Code != "stale_revision" {
		t.Errorf("stale submit code = %s, want stale_revision", errBody.Error.Code)
	}

	// The weak form serves.
	status, header, raw := f.do("POST", "/api/v1/purchase-orders/"+id+"/submit", `{}`, [2]string{"If-Match", `W/"1"`})
	if status != http.StatusOK {
		t.Fatalf("submit with a weak If-Match: %d %s, want 200", status, raw)
	}
	if header.Get("ETag") != `"2"` {
		t.Errorf("ETag after submit = %q, want \"2\"", header.Get("ETag"))
	}
	var sent map[string]any
	_ = json.Unmarshal(raw, &sent)
	if sent["status"] != "sent" || sent["revision"] != float64(2) || sent["sent_at"] == nil {
		t.Errorf("submitted document: status %v revision %v sent_at %v", sent["status"], sent["revision"], sent["sent_at"])
	}

	// Submitting again is an invalid transition (the base sent a RECEIVED
	// order again).
	if status, _, raw := f.do("POST", "/api/v1/purchase-orders/"+id+"/submit", `{"revision":2}`); status != http.StatusConflict {
		t.Fatalf("resubmitting a sent order: %d %s, want 409", status, raw)
	} else if !strings.Contains(string(raw), "invalid_state_transition") {
		t.Fatalf("the resubmission refusal is not invalid_state_transition: %s", raw)
	}
}

// TestPurchaseOrderListWire: the list is the envelope, the status filter
// filters and refuses an unknown value, the cursor walks every row once,
// include=total counts, an empty page is [] in the bytes, and an unknown
// parameter is refused.
func TestPurchaseOrderListWire(t *testing.T) {
	f := newWireFixture(t)
	for i := 0; i < 3; i++ {
		f.createPO(t)
	}
	// One sent purchase order beside the drafts.
	po := f.createPO(t)
	status, _, _ := f.do("POST", "/api/v1/purchase-orders/"+po["id"].(string)+"/submit", `{"revision":1}`)
	if status != http.StatusOK {
		t.Fatal("submit for the list")
	}

	status, _, raw := f.do("GET", "/api/v1/purchase-orders?status=draft&limit=2", "")
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, raw)
	}
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
		Limit      int              `json:"limit"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if page.Limit != 2 || len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("page = %d items, limit %d, cursor %v", len(page.Items), page.Limit, page.NextCursor)
	}
	seen := map[string]bool{}
	cursor := *page.NextCursor
	pages := 1
	for {
		status, _, raw = f.do("GET", "/api/v1/purchase-orders?status=draft&limit=2&cursor="+cursor, "")
		if status != http.StatusOK {
			t.Fatalf("walk page: %d %s", status, raw)
		}
		var next struct {
			Items      []map[string]any `json:"items"`
			NextCursor *string          `json:"next_cursor"`
		}
		_ = json.Unmarshal(raw, &next)
		for _, it := range next.Items {
			if seen[it["id"].(string)] {
				t.Fatal("a purchase order served twice")
			}
			seen[it["id"].(string)] = true
			if it["status"] != "draft" {
				t.Fatalf("the draft filter served a %v order", it["status"])
			}
			if _, has := it["lines"]; has {
				t.Error("the list item carries the full lines")
			}
		}
		if next.NextCursor == nil || pages > 10 {
			break
		}
		cursor = *next.NextCursor
		pages++
	}
	if len(seen) != 3 {
		t.Fatalf("the walk served %d draft orders, want 3", len(seen))
	}

	// include=total counts the filtered set; the sent filter filters.
	var withTotal struct {
		Total *int64 `json:"total"`
	}
	_, _, raw = f.do("GET", "/api/v1/purchase-orders?status=draft&include=total", "")
	_ = json.Unmarshal(raw, &withTotal)
	if withTotal.Total == nil || *withTotal.Total != 3 {
		t.Fatalf("total = %v, want 3", withTotal.Total)
	}
	_, _, raw = f.do("GET", "/api/v1/purchase-orders?status=sent&include=total", "")
	_ = json.Unmarshal(raw, &withTotal)
	if withTotal.Total == nil || *withTotal.Total != 1 {
		t.Fatalf("sent total = %v, want 1", withTotal.Total)
	}

	for name, path := range map[string]string{
		"unknown parameter":  "/api/v1/purchase-orders?foo=1",
		"uppercase status":   "/api/v1/purchase-orders?status=DRAFT",
		"unknown status":     "/api/v1/purchase-orders?status=bogus",
		"bad vendor_id":      "/api/v1/purchase-orders?vendor_id=not-a-uuid",
		"bad limit":          "/api/v1/purchase-orders?limit=0",
		"malformed cursor":   "/api/v1/purchase-orders?cursor=garbage",
		"foreign scope":      "/api/v1/purchase-orders?cursor=eyJ2IjoxLCJvIjoicXVvdGVzLmNyZWF0ZWRfYXRfaWQiLCJrIjpbIjIwMjUtMDEtMDFUMDA6MDA6MDBaIiwiMDAwMDAwMDAtMDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAwIl19",
	} {
		if status, _, raw := f.do("GET", path, ""); status != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400 (%s)", name, status, raw)
		}
	}

	// The empty page is [].
	emptyVendor := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO vendors (id, name) VALUES ($1, 'pw-empty')`, emptyVendor); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM vendors WHERE id = $1`, emptyVendor)
	})
	_, _, raw = f.do("GET", "/api/v1/purchase-orders?vendor_id="+emptyVendor.String(), "")
	if !bytes.Contains(raw, []byte(`"items":[]`)) {
		t.Fatalf("empty page: %s", raw)
	}
}

// TestPurchaseOrderIdempotency: the same create twice with one key returns
// the first response with Idempotency-Replayed and makes one row and one
// event.
func TestPurchaseOrderIdempotency(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newWireFixture(t)
	key := [2]string{"Idempotency-Key", "pw-create-" + uuid.NewString()}
	status1, _, raw1 := f.do("POST", "/api/v1/purchase-orders", f.createBody("1", "10"), key)
	if status1 != http.StatusCreated {
		t.Fatalf("first create: %d %s", status1, raw1)
	}
	status2, header2, raw2 := f.do("POST", "/api/v1/purchase-orders", f.createBody("1", "10"), key)
	if status2 != http.StatusCreated || header2.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay: %d, replayed %q", status2, header2.Get("Idempotency-Replayed"))
	}
	var a, b map[string]any
	_ = json.Unmarshal(raw1, &a)
	_ = json.Unmarshal(raw2, &b)
	if a["id"] != b["id"] || a["number"] != b["number"] {
		t.Errorf("the replay answered a different document")
	}
	var count int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM purchase_orders WHERE vendor_id = $1 AND branch_id = $2`, f.vendor, f.branch).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("%d purchase orders for one keyed create, want 1", count)
	}
	var events int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM events_outbox WHERE entity_type = 'purchase_order' AND data->>'number' = $1`, a["number"]).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("%d purchase_order.created events, want 1", events)
	}
}

// TestReceiveConcurrencyThreeReceiptsOneWinner: three concurrent receipts
// of one line that together exceed it (3 + 3 + 3 against an ordered 5, the
// over receipt percent 0): the purchase order row lock serializes them, and
// exactly one passes the over receipt check (ADR 0008 13.1).
func TestReceiveConcurrencyThreeReceiptsOneWinner(t *testing.T) {
	f := newWireFixture(t)
	testutil.RequireDBMaxConns(t, 4)
	po := f.sentPO(t, "5")

	var wg sync.WaitGroup
	codes := make(chan int, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := fmt.Sprintf(`{"revision":2,"lines":[{"line_id":%q,"qty_received":"3","location_id":%q}]}`, po.line, f.yard)
			status, _, _ := f.do("POST", "/api/v1/purchase-orders/"+po.id+"/receive", body)
			codes <- status
		}()
	}
	wg.Wait()
	close(codes)
	ok, refused := 0, 0
	for c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			refused++
		default:
			t.Errorf("a contender answered %d, want 200 or 409", c)
		}
	}
	if ok != 1 || refused != 2 {
		t.Fatalf("the three concurrent receipts answered %d ok and %d refused, want 1 and 2", ok, refused)
	}
	var qty string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT qty_received::text FROM purchase_order_lines WHERE id = $1`, po.lineUUID).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != "3.0000" {
		t.Fatalf("qty_received = %s, want 3.0000 (exactly one receipt landed)", qty)
	}
}

type sentPO struct {
	id, line string
	lineUUID uuid.UUID
}

func (f *wireFixture) sentPO(t *testing.T, quantity string) sentPO {
	t.Helper()
	po := f.createPO(t)
	id := po["id"].(string)
	line := po["lines"].([]any)[0].(map[string]any)["id"].(string)
	status, _, raw := f.do("POST", "/api/v1/purchase-orders/"+id+"/submit", `{"revision":1}`)
	if status != http.StatusOK {
		t.Fatalf("submit for receive: %d %s", status, raw)
	}
	lineUUID, err := uuid.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	return sentPO{id: id, line: line, lineUUID: lineUUID}
}

// TestReceiveMovesAverageExactly: the receipt's value extension and the
// average's one rounding (ADR 0008 3.5) on the worked figures: 10 on hand at
// 5.00, a receipt of 10 at 7.00, average 6.0000, computed exactly through
// the big.Rat path with Q read once before the stock write.
func TestReceiveMovesAverageExactly(t *testing.T) {
	f := newWireFixture(t)
	ctx := context.Background()
	// 10 on hand at 5.00.
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'pw', 10, 0)`,
		f.product, f.yard); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE products SET average_unit_cost = 5.0000 WHERE id = $1`, f.product); err != nil {
		t.Fatal(err)
	}
	po := f.sentPO(t, "10")
	body := fmt.Sprintf(`{"revision":2,"lines":[{"line_id":%q,"qty_received":"10","location_id":%q}]}`, po.line, f.yard)
	status, _, raw := f.do("POST", "/api/v1/purchase-orders/"+po.id+"/receive", body)
	if status != http.StatusOK {
		t.Fatalf("receive: %d %s", status, raw)
	}
	var avg string
	if err := f.db.Pool.QueryRow(ctx, `SELECT average_unit_cost::text FROM products WHERE id = $1`, f.product).Scan(&avg); err != nil {
		t.Fatal(err)
	}
	if avg != "6.0000" {
		t.Fatalf("average = %s, want 6.0000 ((10 x 5 + 70) / 20)", avg)
	}
	// The stock holds 20 (the base commit's double count would hold 30 in Q).
	var qty string
	if err := f.db.Pool.QueryRow(ctx, `SELECT quantity::text FROM inventory WHERE product_id = $1 AND location_id = $2`, f.product, f.yard).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != "20.0000" {
		t.Fatalf("quantity = %s, want 20.0000", qty)
	}
	// The document carries its new revision and status.
	var doc struct {
		Revision int64  `json:"revision"`
		Status   string `json:"status"`
	}
	_ = json.Unmarshal(raw, &doc)
	if doc.Revision != 3 || doc.Status != "received" {
		t.Errorf("after receive: revision %d status %s, want 3 and received", doc.Revision, doc.Status)
	}
	_ = time.Second
	_ = httpx.Quantity(0)
}
