// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product_test

// The kit component PUT on the wire (review r2, N-1): one transaction that
// takes the product's revision (428 without, 409 stale), bumps it, and
// writes its audit row and its product.updated event in the same
// transaction, so a fault partway leaves the kit unchanged. The route's
// "last write wins" text is gone with it.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// kitOutboxWriter and kitAuditLogger are the production recorders over the
// test database: what serve wires, the kit tests wire.
func kitOutboxWriter(t *testing.T, db *database.DB) product.EventRecorder {
	return outbox.NewWriter(db, "")
}

func kitAuditLogger(t *testing.T, db *database.DB) product.AuditLogger {
	return audit.NewLogger(db)
}

// kitFixture serves the product routes over a fully wired service (the
// transaction runner, the outbox writer and the audit logger serve wires),
// so the kit PUT behaves here as it does in production. A caller may pass
// its own service (the fault injection test does).
type kitFixture struct {
	t   *testing.T
	db  *database.DB
	srv *httptest.Server
	svc *product.Service
}

func newKitFixture(t *testing.T, svc *product.Service, db *database.DB) *kitFixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	if svc == nil {
		svc = product.NewService(product.NewRepository(db)).
			WithOutbox(kitOutboxWriter(t, db)).
			WithTxRunner(db).
			WithAudit(kitAuditLogger(t, db))
	}
	f := &kitFixture{t: t, db: db, svc: svc}
	mux := http.NewServeMux()
	product.NewHandler(svc).RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(func() { f.srv.Close() })
	return f
}

func (f *kitFixture) do(method, path string, body any, headers ...string) resp {
	f.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = bytes.NewBufferString(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		f.t.Fatal(err)
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	httpRes, err := f.srv.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer httpRes.Body.Close()
	raw, _ := io.ReadAll(httpRes.Body)
	out := resp{status: httpRes.StatusCode, header: httpRes.Header, raw: raw}
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		_ = dec.Decode(&out.body)
	}
	return out
}

// createKit makes a kit product and two component products and returns the
// kit's id, the components' ids and the kit's current revision.
func (f *kitFixture) createKit() (kitID, compA, compB string, revision int64) {
	f.t.Helper()
	compA = f.createPlain("C")
	compB = f.createPlain("D")
	kit := f.do("POST", "/api/v1/products", map[string]any{
		"sku":                        "WIRE-" + uuid.NewString()[:12],
		"description":                "bundle kit",
		"stock_uom":                  "PCS",
		"base_price_ten_thousandths": 100000,
	}, "Idempotency-Key", uuid.NewString())
	if kit.status != http.StatusCreated {
		f.t.Fatalf("create kit product: %d %s", kit.status, kit.raw)
	}
	return str(kit.body["id"]), compA, compB, 1
}

func (f *kitFixture) createPlain(prefix string) string {
	f.t.Helper()
	res := f.do("POST", "/api/v1/products", map[string]any{
		"sku":                        "WIRE-" + prefix + uuid.NewString()[:11],
		"description":                "component",
		"stock_uom":                  "PCS",
		"base_price_ten_thousandths": 5000,
	}, "Idempotency-Key", uuid.NewString())
	if res.status != http.StatusCreated {
		f.t.Fatalf("create component product: %d %s", res.status, res.raw)
	}
	return str(res.body["id"])
}

func kitPath(kitID string) string { return "/api/v1/products/" + kitID + "/kit-components" }

func putBody(components ...[2]string) map[string]any {
	list := make([]map[string]any, 0, len(components))
	for _, c := range components {
		list = append(list, map[string]any{"component_product_id": c[0], "quantity": c[1]})
	}
	return map[string]any{"components": list}
}

func withRevision(body map[string]any, revision int64) map[string]any {
	out := map[string]any{}
	for k, v := range body {
		out[k] = v
	}
	out["revision"] = revision
	return out
}

func errCode(res resp) string {
	e, _ := res.body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func errField(res resp, field string) bool {
	e, _ := res.body["error"].(map[string]any)
	details, _ := e["details"].([]any)
	for _, d := range details {
		m, _ := d.(map[string]any)
		if m["field"] == field {
			return true
		}
	}
	return false
}

func (f *kitFixture) cleanupKit(ids ...string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, id := range ids {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM product_kit_components WHERE kit_product_id = $1 OR component_product_id = $1`, id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
	}
}

// TestKitComponentsPut_RequiresTheProductRevision: the PUT takes the
// product's revision like every write (428 without a precondition, 409
// stale, the new revision and ETag on success). On the review's head the
// PUT answered 200 with no precondition at all.
func TestKitComponentsPut_RequiresTheProductRevision(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newKitFixture(t, nil, testutil.RequireDB(t))
	kitID, compA, compB, _ := f.createKit()
	defer f.cleanupKit(kitID, compA, compB)

	list := putBody([2]string{compA, "2"}, [2]string{compB, "3"})

	// No precondition: 428.
	res := f.do("PUT", kitPath(kitID), list, "Idempotency-Key", uuid.NewString())
	if res.status != http.StatusPreconditionRequired {
		t.Errorf("PUT without a revision: status = %d, want 428 (body %s)", res.status, res.raw)
	}
	if code := errCode(res); code != "precondition_required" {
		t.Errorf("PUT without a revision: code = %q, want precondition_required", code)
	}

	// Stale: 409 stale_revision.
	res = f.do("PUT", kitPath(kitID), withRevision(list, 4), "If-Match", `"4"`, "Idempotency-Key", uuid.NewString())
	if res.status != http.StatusConflict {
		t.Errorf("PUT at a stale revision: status = %d, want 409 (body %s)", res.status, res.raw)
	}
	if code := errCode(res); code != "stale_revision" {
		t.Errorf("PUT at a stale revision: code = %q, want stale_revision", code)
	}

	// The current revision: 200, the new revision and its ETag.
	res = f.do("PUT", kitPath(kitID), withRevision(list, 1), "Idempotency-Key", uuid.NewString())
	if res.status != http.StatusOK {
		t.Fatalf("PUT at the current revision: status = %d, body %s", res.status, res.raw)
	}
	if rev := res.body["revision"]; rev != json.Number("2") {
		t.Errorf("response revision = %v, want 2 (the write bumps the product's revision)", rev)
	}
	if etag := res.header.Get("ETag"); etag != `"2"` {
		t.Errorf("ETag = %q, want \"2\"", etag)
	}

	// The product read moved with it.
	prod := f.do("GET", "/api/v1/products/"+kitID, nil)
	if prod.status != http.StatusOK {
		t.Fatalf("read the kit product: %d %s", prod.status, prod.raw)
	}
	if rev := prod.body["revision"]; rev != json.Number("2") {
		t.Errorf("product revision after the kit PUT = %v, want 2", rev)
	}

	// The old list is gone and a second PUT at the old revision is stale.
	res = f.do("PUT", kitPath(kitID), withRevision(putBody([2]string{compA, "9"}), 1), "Idempotency-Key", uuid.NewString())
	if res.status != http.StatusConflict {
		t.Errorf("second PUT at revision 1: status = %d, want 409", res.status)
	}
}

// TestKitComponentsPut_RepeatedComponentIsA400: one component twice in a
// list is a field error naming the second entry, never a database fault.
func TestKitComponentsPut_RepeatedComponentIsA400(t *testing.T) {
	testutil.LockOutboxTables(t) // the fixture's creates write outbox events
	f := newKitFixture(t, nil, testutil.RequireDB(t))
	kitID, compA, compB, _ := f.createKit()
	defer f.cleanupKit(kitID, compA, compB)

	res := f.do("PUT", kitPath(kitID), withRevision(putBody([2]string{compA, "2"}, [2]string{compA, "3"}), 1),
		"Idempotency-Key", uuid.NewString())
	if res.status != http.StatusBadRequest {
		t.Fatalf("repeated component: status = %d, want 400 (body %s)", res.status, res.raw)
	}
	if !errField(res, "components[1].component_product_id") {
		t.Errorf("repeated component: details do not name components[1].component_product_id: %s", res.raw)
	}
}

// kitTamperer wraps the real kit store so the fault injection test can make
// the repository's replace fail partway through: it duplicates the first
// component on the way down, so the DELETE and the first INSERT run and the
// second INSERT violates the table's primary key. Without the service's
// transaction the delete and the first insert stay committed and the kit is
// half written; with it, nothing moved.
type kitTamperer struct {
	product.Repository
	inner product.KitStore
	armed atomic.Bool
}

func (k *kitTamperer) ListKitComponents(ctx context.Context, kitID uuid.UUID) ([]product.KitComponent, error) {
	return k.inner.ListKitComponents(ctx, kitID)
}

func (k *kitTamperer) LockKitProduct(ctx context.Context, kitID uuid.UUID) (int64, string, error) {
	return k.inner.LockKitProduct(ctx, kitID)
}

func (k *kitTamperer) ProductKitRef(ctx context.Context, id uuid.UUID, sku, description, uom *string, isKit *bool) error {
	return k.inner.ProductKitRef(ctx, id, sku, description, uom, isKit)
}

func (k *kitTamperer) ReplaceKitComponents(ctx context.Context, kitID uuid.UUID, comps []product.KitComponent, revision int64) (int64, error) {
	if k.armed.Load() && len(comps) > 0 {
		dup := comps[0]
		dup.Position = len(comps)
		comps = append(append([]product.KitComponent{}, comps...), dup)
	}
	return k.inner.ReplaceKitComponents(ctx, kitID, comps, revision)
}

// TestKitComponentsPut_FaultPartwayLeavesTheKitUnchanged: a fault between
// the replace's statements rolls the whole write back: the kit list, the
// is_kit flag, the product's revision, the audit row and the event are all
// exactly as they were (review r2 N-1's failure scenario).
func TestKitComponentsPut_FaultPartwayLeavesTheKitUnchanged(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	repo := product.NewRepository(db)
	tamperer := &kitTamperer{Repository: repo, inner: repo}
	svc := product.NewService(tamperer).
		WithOutbox(kitOutboxWriter(t, db)).WithTxRunner(db).WithAudit(kitAuditLogger(t, db))
	f := newKitFixture(t, svc, db)
	kitID, compA, compB, _ := f.createKit()
	defer f.cleanupKit(kitID, compA, compB)

	// An established two component kit.
	res := f.do("PUT", kitPath(kitID), withRevision(putBody([2]string{compA, "2"}, [2]string{compB, "3"}), 1),
		"Idempotency-Key", uuid.NewString())
	if res.status != http.StatusOK {
		t.Fatalf("seed the kit: %d %s", res.status, res.raw)
	}
	before := f.do("GET", kitPath(kitID), nil)
	if len(before.body["components"].([]any)) != 2 {
		t.Fatalf("seeded kit has %v components, want 2", before.body["components"])
	}

	// The replace now fails partway (the tampered duplicate hits the primary
	// key after the delete and the first insert ran).
	tamperer.armed.Store(true)
	fault := f.do("PUT", kitPath(kitID), withRevision(putBody([2]string{compB, "9"}), 2),
		"Idempotency-Key", uuid.NewString())
	if fault.status != http.StatusInternalServerError {
		t.Fatalf("injected fault answered %d, want 500 (body %s)", fault.status, fault.raw)
	}

	after := f.do("GET", kitPath(kitID), nil)
	if comps := after.body["components"].([]any); len(comps) != 2 {
		t.Errorf("kit holds %d components after a fault partway, want the 2 before (%s)", len(comps), after.raw)
	}
	first := compsAfter(after, compA)
	if first != "2" {
		t.Errorf("component A quantity = %q after a rolled back replace, want 2", first)
	}
	if rev := after.body["revision"]; rev != json.Number("2") {
		t.Errorf("product revision = %v after a rolled back replace, want 2", rev)
	}
	var isKit bool
	if err := db.Pool.QueryRow(context.Background(), `SELECT is_kit FROM products WHERE id = $1`, kitID).Scan(&isKit); err != nil || !isKit {
		t.Errorf("is_kit = %v (%v) after a rolled back replace, want true", isKit, err)
	}
	if n := f.countUpdated(kitID, "audit_log", "action"); n != 1 {
		t.Errorf("%d product.updated audit rows for the kit product, want the 1 of the good write (the create's product.created rolled back with it)", n)
	}
	if n := f.countUpdated(kitID, "events_outbox", "type"); n != 1 {
		t.Errorf("%d product.updated events for the kit product, want the 1 of the good write", n)
	}
}

func compsAfter(res resp, productID string) string {
	comps, _ := res.body["components"].([]any)
	for _, c := range comps {
		m, _ := c.(map[string]any)
		if m["component_product_id"] == productID {
			return m["quantity"].(string)
		}
	}
	return ""
}

// countUpdated counts the kit PUT's own rows (product.updated); the create
// writes product.created rows for the same product beside them.
func (f *kitFixture) countUpdated(id, table, column string) int {
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM `+table+` WHERE entity_type = 'product' AND entity_id = $1 AND `+column+` = 'product.updated'`, id).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// TestKitComponentsPut_WritesAuditAndEvent: the successful write records one
// audit row and one product.updated event carrying parts kit_components,
// committed with the write.
func TestKitComponentsPut_WritesAuditAndEvent(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newKitFixture(t, nil, testutil.RequireDB(t))
	kitID, compA, compB, _ := f.createKit()
	defer f.cleanupKit(kitID, compA, compB)

	res := f.do("PUT", kitPath(kitID), withRevision(putBody([2]string{compA, "2"}), 1),
		"Idempotency-Key", uuid.NewString())
	if res.status != http.StatusOK {
		t.Fatalf("PUT: %d %s", res.status, res.raw)
	}

	var auditActions []string
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT action FROM audit_log WHERE entity_type = 'product' AND entity_id = $1 AND action = 'product.updated'`, kitID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		auditActions = append(auditActions, a)
	}
	if len(auditActions) != 1 || auditActions[0] != "product.updated" {
		t.Errorf("audit actions = %v, want one product.updated", auditActions)
	}

	var eventType, data string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT type, data::text FROM events_outbox WHERE entity_type = 'product' AND entity_id = $1 AND type = 'product.updated'`, kitID).
		Scan(&eventType, &data); err != nil {
		t.Fatalf("no product.updated event for the kit write: %v", err)
	}
	if eventType != "product.updated" {
		t.Errorf("event type = %q, want product.updated", eventType)
	}
	if !strings.Contains(data, `"kit_components"`) {
		t.Errorf("event data %s does not name the kit_components part", data)
	}
}

// TestKitComponentsPut_Pool4ThreeContenders: three racers PUT the same kit
// at the same revision; exactly one wins, the losers answer 409, and the kit
// holds the winner's list. Each transaction holds one connection for its
// whole length at pool size 4.
func TestKitComponentsPut_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newKitFixture(t, nil, db)
	kitID, compA, compB, _ := f.createKit()
	defer f.cleanupKit(kitID, compA, compB)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = ctx

	var wg sync.WaitGroup
	states := make(chan int, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var body map[string]any
			if i == 0 {
				body = withRevision(putBody([2]string{compA, "5"}), 1)
			} else {
				body = withRevision(putBody([2]string{compB, "7"}), 1)
			}
			res := f.do("PUT", kitPath(kitID), body, "Idempotency-Key", uuid.NewString())
			states <- res.status
		}(i)
	}
	wg.Wait()
	close(states)
	var ok, stale int
	for s := range states {
		switch s {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			stale++
		default:
			t.Errorf("contender answered %d, want 200 or 409", s)
		}
	}
	if ok != 1 || stale != 2 {
		t.Errorf("%d winners and %d losers, want exactly one winner and two 409s", ok, stale)
	}
	after := f.do("GET", kitPath(kitID), nil)
	if rev := after.body["revision"]; rev != json.Number("2") {
		t.Errorf("product revision = %v after three contenders, want 2 (one winner)", rev)
	}
	if n := len(after.body["components"].([]any)); n != 1 {
		t.Errorf("kit holds %d components, want the winner's 1", n)
	}
}

// gatedTxRunner holds each transaction INSIDE, before its first statement,
// until every contender has entered: the saturation proof that no statement
// inside the transaction reaches for a second pool connection.
type gatedTxRunner struct {
	db   *database.DB
	want int32
	gate sync.WaitGroup
}

func newGatedKitTx(db *database.DB, want int) *gatedTxRunner {
	g := &gatedTxRunner{db: db, want: int32(want)}
	g.gate.Add(want)
	return g
}

func (g *gatedTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return g.db.RunInTx(ctx, func(txCtx context.Context) error {
		g.gate.Done()
		g.gate.Wait()
		return fn(txCtx)
	})
}

// TestKitComponentsPut_SaturationNeedsNoSecondConnection: as many kit PUTs
// as the pool has connections, each held inside its transaction before its
// first statement. A PUT whose transaction ran a statement through the pool
// would leave four holders each waiting for a fifth connection.
func TestKitComponentsPut_SaturationNeedsNoSecondConnection(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	// The seed runs through a plain fixture (its writes must not pass the
	// gate); only the contenders' PUTs run inside the gated transactions.
	f := newKitFixture(t, nil, db)
	svc := product.NewService(product.NewRepository(db)).
		WithOutbox(kitOutboxWriter(t, db)).
		WithTxRunner(newGatedKitTx(db, 4)).
		WithAudit(kitAuditLogger(t, db))

	kits := make([][3]string, 4)
	for i := range kits {
		kitID, compA, _, _ := f.createKit()
		kits[i] = [3]string{kitID, compA, ""}
		defer f.cleanupKit(kitID, compA)
	}

	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func(i int) {
			kitID, compA, _ := kits[i][0], kits[i][1], kits[i][2]
			_, err := svc.ReplaceKitComponents(context.Background(), mustParseUUID(kitID),
				&product.PutKitComponentsRequest{
					Revision:   ptrInt64(1),
					Components: []product.ComponentRequest{{ComponentProductID: &compA, Quantity: json.RawMessage(`"2"`)}},
				}, product.RevisionPrecondition{Revision: ptrInt64(1)})
			done <- err
		}(i)
	}
	timeout := time.After(30 * time.Second)
	for i := 0; i < 4; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("contender %d: %v", i, err)
			}
		case <-timeout:
			t.Fatal("kit PUTs held inside their transactions did not finish: a statement inside the transaction used the pool")
		}
	}
}

func mustParseUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}

func ptrInt64(v int64) *int64 { return &v }
