// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

// The live failures ADR 0008 section 14 names for C4-1a, each stated as a
// test that fails at the base commit (the recipe's live failure step):
//
//   - a receipt accepts any quantity, including more than was ordered
//     (TestOverReceiptRefused);
//   - a second purchase order from one A2A key under concurrency, because the
//     idempotency check reads before the purchase order writes and the log
//     row lands only after it (TestA2AOnePurchaseOrderPerKeyUnderConcurrency);
//   - recommendations that ignore on order (TestRecommendationsCountOnOrder).
//
// Each test keeps the base commit's request shapes and states the new
// expected outcome, so running it against the base shows the failure.

import (
	"bytes"
	"context"
	"crypto/rand"
	"math"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
)

// liveFixture seeds one branch, one yard, one product and one vendor, and
// mounts the purchase order handler on a real mux behind the idempotency
// middleware, the way serve does.
type liveFixture struct {
	t       *testing.T
	db      *database.DB
	srv     *httptest.Server
	svc     *purchase_order.Service
	branch  uuid.UUID
	yard    uuid.UUID
	product uuid.UUID
	vendor  uuid.UUID
}

func newLiveFixture(t *testing.T) *liveFixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &liveFixture{t: t, db: db, branch: uuid.New(), yard: uuid.New(),
		product: uuid.New(), vendor: uuid.New()}
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id) VALUES ($1, 'BRANCH', $2, NULL)`,
		f.branch, "lf-"+f.branch.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id) VALUES ($1, 'YARD', $2, $3)`,
		f.yard, "lf-"+f.yard.String()[:8], f.branch); err != nil {
		t.Fatalf("seed yard: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price, reorder_point, reorder_qty) VALUES ($1, $2, 'live failure', 'EA', 1, 5, 10)`,
		f.product, "LF-"+f.product.String()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO vendors (id, name) VALUES ($1, $2)`,
		f.vendor, "lf-vendor-"+f.vendor.String()[:8]); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id IN (SELECT id FROM purchase_orders WHERE vendor_id = $1)`, f.vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE vendor_id = $1`, f.vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM a2a_inbound_po_log WHERE idempotency_key LIKE 'lf-test-%'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM reorder_recommendations WHERE product_id = $1`, f.product)
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
	f.svc = purchase_order.NewService(repo, db, nil, invSvc, prodSvc, vendSvc).
		WithVelocityRepo(purchase_order.NewVelocityRepository(db))
	h := purchase_order.NewHandler(f.svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *liveFixture) call(method, path, body string) (int, []byte) {
	f.t.Helper()
	res, err := http.Post(f.srv.URL+path, "application/json", strings.NewReader(body))
	if method != "POST" {
		var req *http.Request
		req, err = http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
		if err != nil {
			f.t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err = http.DefaultClient.Do(req)
	}
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

// sentPOWithLine creates a purchase order of one line for quantity ordered,
// submits it, and returns the purchase order and its line id.
func (f *liveFixture) sentPOWithLine(t *testing.T, ordered float64) (poID, lineID string, revision int64) {
	t.Helper()
	quantity := httpx.Quantity(math.Round(ordered * 10000))
	po, err := f.svc.CreateFromLines(context.Background(), f.vendor, purchase_order.SourceManual,
		[]purchase_order.LineInput{{ProductID: &f.product, Description: "live failure", Quantity: quantity, UnitCost: 20000}}, nil)
	if err != nil {
		t.Fatalf("create PO: %v", err)
	}
	submitted, err := f.svc.SubmitPO(context.Background(), po.ID, "", &po.Revision)
	if err != nil {
		t.Fatalf("submit PO: %v", err)
	}
	return submitted.ID.String(), submitted.Lines[0].ID.String(), submitted.Revision
}

// TestOverReceiptRefused: the base commit's receive accepts more than was
// ordered; ADR 0008 section 4 caps a line's total received at its ordered
// quantity (the purchasing.over_receipt_percent setting defaulting to 0), a
// 409 blocker over_receipt naming the line.
func TestOverReceiptRefused(t *testing.T) {
	f := newLiveFixture(t)
	poID, lineID, revision := f.sentPOWithLine(t, 5)

	body := fmt.Sprintf(`{"revision":%d,"lines":[{"line_id":%q,"qty_received":"10","location_id":%q}]}`, revision, lineID, f.yard)
	status, raw := f.call("POST", "/api/v1/purchase-orders/"+poID+"/receive", body)
	if status != http.StatusConflict {
		t.Fatalf("receiving 10 against an ordered 5: %d %s, want 409", status, raw)
	}
	var errBody struct {
		Error struct {
			Code    string `json:"code"`
			Details []struct {
				Code string `json:"code"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &errBody); err != nil {
		t.Fatalf("the refusal is not JSON: %s", raw)
	}
	found := false
	for _, d := range errBody.Error.Details {
		if d.Code == "over_receipt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the refusal carries no over_receipt blocker: %s", raw)
	}

	// Nothing was received by the refused act.
	var qty float64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(qty_received, 0) FROM purchase_order_lines WHERE id = $1`, lineID).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != 0 {
		t.Fatalf("the refused receipt wrote qty_received %v, want 0", qty)
	}
}

// TestA2AOnePurchaseOrderPerKeyUnderConcurrency: two concurrent webhooks
// carrying one idempotency key must end with exactly one purchase order, one
// 201 and one of today's 409 duplicate answers (ADR 0008 section 11: the log
// row is inserted first, ON CONFLICT DO NOTHING, in the same transaction as
// the purchase order it creates).
func TestA2AOnePurchaseOrderPerKeyUnderConcurrency(t *testing.T) {
	f := newLiveFixture(t)
	testutil.RequireDBMaxConns(t, 4)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	repo := purchase_order.NewRepository(f.db)
	invSvc := inventory.NewService(inventory.NewRepository(f.db))
	svc := purchase_order.NewService(repo, f.db, nil, invSvc, nil, nil)
	receiver := purchase_order.NewA2AReceiver(&key.PublicKey, svc, f.db.Pool, slog.Default())
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/a2a/purchase-order", receiver.ReceiveWebhook)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	payload, err := json.Marshal(map[string]any{
		"event_type": "create_purchase_order",
		"payload": map[string]any{
			"vendor_id": f.vendor.String(),
			"lines":     []map[string]any{{"product_id": f.product.String(), "description": "a2a", "quantity": 2, "cost": 3}},
		},
		"trace_id":         "lf-trace",
		"idempotency_key":  "lf-test-" + uuid.NewString(),
		"timestamp":        "2026-01-01T00:00:00Z",
		"iss":              "lf-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := obj.DetachedCompactSerialize()
	if err != nil {
		t.Fatal(err)
	}

	post := func() int {
		req, err := http.NewRequest("POST", srv.URL+"/api/v1/a2a/purchase-order", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-JWS-Signature", sig)
		req.Header.Set("X-Idempotency-Key", "lf-test-key")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = post()
		}(i)
	}
	wg.Wait()

	created, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		default:
			t.Errorf("a concurrent webhook answered %d, want 201 or 409", c)
		}
	}
	if created != 1 || conflict != 1 {
		t.Errorf("the concurrent pair answered %d created and %d conflict, want 1 and 1 (codes %v)", created, conflict, codes)
	}
	var count int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM purchase_orders WHERE vendor_id = $1 AND source = 'A2A'`, f.vendor).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("%d purchase orders exist for one idempotency key, want 1", count)
	}
}

// TestRecommendationsCountOnOrder: a product below its reorder point with an
// open purchase order covering the need is not due a recommendation, because
// the on order quantity counts (ADR 0008 section 10.3: due when
// available + on_order - backordered <= reorder_point). At the base commit
// the recommendation list still names the product.
func TestRecommendationsCountOnOrder(t *testing.T) {
	f := newLiveFixture(t)
	ctx := context.Background()
	_ = f

	// Stock 1 against a reorder point of 5, and a sent purchase order of 50
	// on order for the same product at this branch.
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'lf', 1, 0)`,
		f.product, f.yard); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
	_, _, _ = f.sentPOWithLine(t, 50)

	status, raw := f.call("GET", "/api/v1/purchase-orders/recommendations", "")
	if status != http.StatusOK {
		t.Fatalf("recommendations: %d %s", status, raw)
	}
	if strings.Contains(string(raw), f.product.String()) {
		t.Fatalf("the recommendations still name the product whose need an open purchase order covers: %s", raw)
	}
}
