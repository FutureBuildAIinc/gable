// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// The orders draft kind (ADR 0007 section 10, the second kind): the same
// seven routes and the same promotion as quotes, over the order module. A
// create draft's promotion runs the create core, so the order is born in
// status draft with its SO- number and order.created beside draft.promoted;
// the confirm stays an order transition. An edit draft's promotion runs the
// update core with subject_revision as the precondition, writing
// order.updated beside draft.promoted, and refuses a subject that moved
// (subject_stale) or left draft (order_not_draft).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/drafts"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// kindFixture wires the orders kind the way serve does: the order module's
// service, the kind, the drafts registry, service and handler, behind the
// idempotency layer and the actor middleware.
type kindFixture struct {
	t          *testing.T
	db         *database.DB
	srv        *httptest.Server
	customerID uuid.UUID
	productID  uuid.UUID
	sku        string
}

func newKindFixture(t *testing.T, db *database.DB) *kindFixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	f := &kindFixture{t: t, db: db, customerID: uuid.New(), productID: uuid.New(), sku: "KIND-" + uuid.NewString()[:8]}
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Order Kind Co', $2, `+branch+`)`, f.customerID, "OKIND-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5)`, f.productID, f.sku); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.088750
		WHERE id = `+branch); err != nil {
		t.Fatalf("seed branch rate: %v", err)
	}

	orderSvc := order.NewService(order.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAuditLog(audit.NewLogger(db))
	orderKind := order.NewDraftKind(orderSvc)
	draftsRepo := drafts.NewRepository(db)
	registry, err := drafts.NewRegistry(orderKind)
	if err != nil {
		t.Fatal(err)
	}
	hub := drafts.NewHub(draftsRepo, 50*1_000_000, nil)
	t.Cleanup(hub.Stop)
	svc := drafts.NewService(draftsRepo, registry).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db)).
		WithBranchGuard(middleware.NewBranchGuard(db)).
		WithFeed(hub)
	handler := drafts.NewHandler(svc).WithFeedHandler(drafts.NewFeedHandler(svc, draftsRepo, hub, drafts.DefaultFeedSettings(), nil))

	mux := http.NewServeMux()
	branchMw := middleware.NewBranchMiddleware(db).Handler
	order.RegisterDraftRoutes(mux, handler, orderKind, branchMw)
	order.NewHandler(orderSvc).RegisterRoutes(mux, branchMw)
	f.srv = httptest.NewServer(actor.Middleware(middleware.Idempotency(db)(mux)))

	t.Cleanup(func() {
		f.srv.Close()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM draft_events WHERE module='orders'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM drafts WHERE module='orders'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type='draft' AND changes::text LIKE '%"orders"%'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type='draft' AND data::text LIKE '%orders%'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, f.customerID)
	})
	return f
}

func (f *kindFixture) do(method, path string, body any, headers ...string) kindResp {
	f.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		f.t.Fatal(err)
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", "req-order-kind-test")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := kindResp{status: res.StatusCode, header: res.Header, raw: raw}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

type kindResp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *kindFixture) payload(qty string) map[string]any {
	return map[string]any{
		"customer_id":   f.customerID.String(),
		"delivery_type": "pickup",
		"lines":         []map[string]any{{"product_id": f.productID.String(), "quantity": qty}},
	}
}

// TestOrderKind_CreateDraftPromotesToDraftOrder pins the create path: the
// promotion runs the create core, the order is born in status draft with
// its SO- number, and the outbox carries order.created before
// draft.promoted.
func TestOrderKind_CreateDraftPromotesToDraftOrder(t *testing.T) {
	f := newKindFixture(t, testutil.RequireDB(t))
	r := f.do("POST", "/api/v1/drafts/orders", map[string]any{"payload": f.payload("10")})
	if r.status != http.StatusCreated {
		t.Fatalf("create order draft = %d: %s", r.status, r.raw)
	}
	id := kindStr(t, r.body, "id")
	if kindStr(t, r.body, "status") != "open" {
		t.Errorf("draft status = %v", r.body["status"])
	}

	r = f.do("POST", "/api/v1/drafts/orders/"+id+"/promote", map[string]any{"revision": 1})
	if r.status != http.StatusCreated {
		t.Fatalf("promote = %d: %s", r.status, r.raw)
	}
	if loc := r.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/orders/") {
		t.Errorf("Location = %q, want the order's URL", loc)
	}
	promoted := r.body["promoted"].(map[string]any)
	number := kindStr(t, promoted, "number")
	if !strings.HasPrefix(number, "SO-") {
		t.Errorf("promoted number = %q, want an SO- number", number)
	}
	orderID := kindStr(t, promoted, "entity_id")

	// The order itself: status draft (the confirm is a later transition),
	// readable on its entity route.
	got := f.do("GET", "/api/v1/orders/"+orderID, nil)
	if got.status != http.StatusOK {
		t.Fatalf("read the promoted order = %d: %s", got.status, got.raw)
	}
	if kindStr(t, got.body, "status") != "draft" {
		t.Errorf("the promoted order's status = %v, want draft (the confirm is a later transition)", got.body["status"])
	}
	if kindStr(t, got.body, "number") != number {
		t.Errorf("the order's number = %v, want %s", got.body["number"], number)
	}

	// The outbox: order.created first, draft.promoted last (section 4.2
	// step 9).
	types := kindEvents(t, f.db, orderID)
	if len(types) < 2 || types[0] != "order.created" || types[len(types)-1] != "draft.promoted" {
		t.Errorf("outbox events = %v, want order.created first and draft.promoted last", types)
	}
}

// TestOrderKind_EditDraftPromotesThroughUpdateCore pins the edit path: an
// edit draft on a draft order promotes through the update core on
// subject_revision, writing order.updated beside draft.promoted; a subject
// that moved is stale (subject_stale); a subject that left draft refuses
// (order_not_draft).
func TestOrderKind_EditDraftPromotesThroughUpdateCore(t *testing.T) {
	f := newKindFixture(t, testutil.RequireDB(t))
	r := f.do("POST", "/api/v1/orders", f.payload("10"))
	if r.status != http.StatusCreated {
		t.Fatalf("create order = %d: %s", r.status, r.raw)
	}
	orderID := kindStr(t, r.body, "id")

	// The edit draft on it, at the order's current revision.
	r = f.do("POST", "/api/v1/drafts/orders", map[string]any{
		"payload": f.payload("20"), "subject_id": orderID, "subject_revision": 1})
	if r.status != http.StatusCreated {
		t.Fatalf("create edit draft = %d: %s", r.status, r.raw)
	}
	id := kindStr(t, r.body, "id")

	r = f.do("POST", "/api/v1/drafts/orders/"+id+"/promote", map[string]any{"revision": 1})
	if r.status != http.StatusOK {
		t.Fatalf("promote edit draft = %d: %s", r.status, r.raw)
	}
	got := f.do("GET", "/api/v1/orders/"+orderID, nil)
	qty := got.body["lines"].([]any)[0].(map[string]any)["quantity"]
	if qty != "20" {
		t.Errorf("the promoted edit's quantity = %v, want 20", qty)
	}
	types := kindEvents(t, f.db, orderID)
	if len(types) < 2 || types[len(types)-2] != "order.updated" || types[len(types)-1] != "draft.promoted" {
		t.Errorf("outbox events = %v, want order.updated immediately before draft.promoted", types)
	}

	// A subject that moved after the draft was built on it: the promotion
	// is stale on the subject (subject_stale), not on the draft.
	r = f.do("POST", "/api/v1/drafts/orders", map[string]any{
		"payload": f.payload("30"), "subject_id": orderID, "subject_revision": kindRev(t, got.body)})
	if r.status != http.StatusCreated {
		t.Fatalf("create edit draft at the current revision = %d: %s", r.status, r.raw)
	}
	moved := kindStr(t, r.body, "id")
	putBody := f.payload("25")
	putBody["revision"] = kindRev(t, got.body)
	if rr := f.do("PUT", "/api/v1/orders/"+orderID, putBody); rr.status != http.StatusOK {
		t.Fatalf("move the subject = %d: %s", rr.status, rr.raw)
	}
	r = f.do("POST", "/api/v1/drafts/orders/"+moved+"/promote", map[string]any{"revision": 1})
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "subject_stale") {
		t.Errorf("promote against a moved subject = %d: %s, want 409 subject_stale", r.status, r.raw)
	}

	// A subject that left draft: the module's own order_not_draft blocker.
	after := f.do("GET", "/api/v1/orders/"+orderID, nil)
	r = f.do("POST", "/api/v1/orders/"+orderID+"/transitions", map[string]any{"to": "confirmed", "revision": kindRev(t, after.body)})
	if r.status != http.StatusOK {
		t.Fatalf("confirm the order = %d: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/drafts/orders", map[string]any{
		"payload": f.payload("30"), "subject_id": orderID})
	if r.status != http.StatusCreated {
		t.Fatalf("create edit draft on the confirmed order = %d: %s", r.status, r.raw)
	}
	confirmed := kindStr(t, r.body, "id")
	r = f.do("POST", "/api/v1/drafts/orders/"+confirmed+"/promote", map[string]any{"revision": 1})
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "order_not_draft") {
		t.Errorf("promote an edit draft on a confirmed order = %d: %s, want 409 order_not_draft", r.status, r.raw)
	}
}

func kindStr(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	s, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s is %T (%v), want a string", key, m[key], m[key])
	}
	return s
}

func kindRev(t *testing.T, m map[string]any) int64 {
	t.Helper()
	n, ok := m["revision"].(json.Number)
	if !ok {
		t.Fatalf("revision is %T (%v), want a number", m["revision"], m["revision"])
	}
	v, err := n.Int64()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// kindEvents reads the outbox types that name the order, in position order.
func kindEvents(t *testing.T, db *database.DB, orderID string) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT type FROM events_outbox
		  WHERE (entity_type='order' AND entity_id = $1::uuid)
		     OR (entity_type='draft' AND data->>'entity_id' = $1::text)
		  ORDER BY position`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var ty string
		if err := rows.Scan(&ty); err != nil {
			t.Fatal(err)
		}
		types = append(types, ty)
	}
	return types
}
