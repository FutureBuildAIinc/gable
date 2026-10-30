// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package millwork_test

// The millwork module on the wire contract (ADR 0001), tested end to end: a
// real Postgres, the real handler on a real mux behind the real idempotency
// middleware, requests as JSON and responses read back as JSON. Nothing here
// touches a module type, so each test states a wire fact.

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

	"github.com/gablelbm/gable/internal/millwork"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type fixture struct {
	t      *testing.T
	db     *database.DB
	srv    *httptest.Server
	prefix string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db, prefix: "MW-" + uuid.NewString()[:8]}
	// The product catalog is behind the module, the way serve wires it: a
	// configured item becomes a product row of its own.
	svc := millwork.NewService(millwork.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db)).
		WithProducts(product.NewService(product.NewRepository(db)))
	mux := http.NewServeMux()
	millwork.NewHandler(svc).RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(func() {
		f.srv.Close()
		ctx := context.Background()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'millwork_option' AND entity_id IN (SELECT id FROM millwork_options WHERE category LIKE $1)`, f.prefix+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type = 'millwork_option' AND entity_id IN (SELECT id FROM millwork_options WHERE category LIKE $1)`, f.prefix+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM millwork_options WHERE category LIKE $1`, f.prefix+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE sku LIKE 'NS-DR-%' AND description LIKE $1`, f.prefix+"%")
	})
	return f
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *fixture) do(t *testing.T, method, path, body string, hdr map[string]string) resp {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	if strings.Contains(res.Header.Get("Content-Type"), "json") && len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.body)
	}
	return out
}

func (f *fixture) create(t *testing.T, body string, hdr map[string]string) resp {
	t.Helper()
	return f.do(t, http.MethodPost, "/api/v1/millwork/options", body, hdr)
}

// The create shape: integer cents, optional attributes present as null,
// revision and the ETag, Location, an unknown field refused.
func TestCreate_Shape(t *testing.T) {
	testutil.LockOutboxTables(t) // this test writes events_outbox rows
	f := newFixture(t)
	res := f.create(t, `{"category":"`+f.prefix+`-door","name":"Clear pine","price_adjustment_cents":1250,"attributes":{"grade":"Clear"}}`, nil)
	if res.status != http.StatusCreated {
		t.Fatalf("create = %d %s", res.status, res.raw)
	}
	if loc := res.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/millwork/options/") {
		t.Errorf("Location = %q", loc)
	}
	if etag := res.header.Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", etag)
	}
	if res.body["price_adjustment_cents"] != float64(1250) {
		t.Errorf("price_adjustment_cents = %v, want 1250", res.body["price_adjustment_cents"])
	}
	if v, ok := res.body["price_adjustment"]; ok {
		t.Errorf("the float price_adjustment survived: %v", v)
	}
	if res.body["revision"] != float64(1) {
		t.Errorf("revision = %v", res.body["revision"])
	}

	// attributes absent is null, present.
	res = f.create(t, `{"category":"`+f.prefix+`-none","name":"No attributes","price_adjustment_cents":0}`, nil)
	if v, ok := res.body["attributes"]; !ok || v != nil {
		t.Errorf("attributes = %v (present %v), want null present", v, ok)
	}

	if res := f.create(t, `{"category":"`+f.prefix+`-x","name":"x","price_adjustment_cents":1,"smoke":"signal"}`, nil); res.status != http.StatusBadRequest {
		t.Errorf("an unknown body field = %d, want 400", res.status)
	}
}

// One 400 with every offending field; a negative adjustment (a discount) is
// a value, not a refusal; a body the route cannot consume is bad_request.
func TestCreate_FieldValidation(t *testing.T) {
	testutil.LockOutboxTables(t) // this test writes events_outbox rows
	f := newFixture(t)
	res := f.create(t, `{"category":"  ","name":"","price_adjustment_cents":12.5}`, nil)
	if res.status != http.StatusBadRequest {
		t.Fatalf("validation = %d %s", res.status, res.raw)
	}
	env := res.body["error"].(map[string]any)
	if env["code"] != "validation_failed" {
		t.Errorf("code = %v", env["code"])
	}
	fields := map[string]bool{}
	for _, d := range env["details"].([]any) {
		fields[d.(map[string]any)["field"].(string)] = true
	}
	for _, want := range []string{"category", "name", "price_adjustment_cents"} {
		if !fields[want] {
			t.Errorf("details = %v, want a %s entry", env["details"], want)
		}
	}
	res = f.create(t, `{"category":"`+f.prefix+`-disc","name":"Discount","price_adjustment_cents":-250}`, nil)
	if res.status != http.StatusCreated || res.body["price_adjustment_cents"] != float64(-250) {
		t.Errorf("a discount option = %d %s", res.status, res.raw)
	}
	if res := f.create(t, `not-json`, nil); res.status != http.StatusBadRequest ||
		res.body["error"].(map[string]any)["code"] != "bad_request" {
		t.Errorf("an unparseable body = %d %s, want 400 bad_request", res.status, res.raw)
	}
}

// The list: the envelope with [] for an empty category, the cursor walks
// every row once, include=total, an unknown parameter and a missing
// category refused.
func TestList_EnvelopeCursor(t *testing.T) {
	testutil.LockOutboxTables(t) // this test writes events_outbox rows
	f := newFixture(t)
	cat := f.prefix + "-cat"
	for _, name := range []string{"one", "two", "three"} {
		if res := f.create(t, `{"category":"`+cat+`","name":"`+name+`","price_adjustment_cents":100}`, nil); res.status != http.StatusCreated {
			t.Fatalf("create %s = %d %s", name, res.status, res.raw)
		}
	}
	base := "/api/v1/millwork/options?category=" + cat
	res := f.do(t, http.MethodGet, base+"&limit=2", "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("list = %d %s", res.status, res.raw)
	}
	if len(res.body["items"].([]any)) != 2 || res.body["limit"] != float64(2) {
		t.Errorf("page = %s", res.raw)
	}
	next, _ := res.body["next_cursor"].(string)
	if next == "" {
		t.Fatal("no next_cursor with another page present")
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		q := base + "&limit=2"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		page := f.do(t, http.MethodGet, q, "", nil)
		for _, it := range page.body["items"].([]any) {
			id := it.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("option %s served twice", id)
			}
			seen[id] = true
		}
		nc, _ := page.body["next_cursor"].(string)
		if nc == "" {
			break
		}
		cursor = nc
	}
	if len(seen) != 3 {
		t.Errorf("the cursor walk served %d options, want 3", len(seen))
	}
	if res = f.do(t, http.MethodGet, base+"&include=total", "", nil); res.body["total"] != float64(3) {
		t.Errorf("include=total = %v, want 3", res.body["total"])
	}
	if res = f.do(t, http.MethodGet, "/api/v1/millwork/options?category="+f.prefix+"-empty", "", nil); !strings.Contains(string(res.raw), `"items":[]`) {
		t.Errorf("an empty category = %s, want items []", res.raw)
	}
	if res = f.do(t, http.MethodGet, "/api/v1/millwork/options", "", nil); res.status != http.StatusBadRequest {
		t.Errorf("a missing category = %d, want 400", res.status)
	}
	if res = f.do(t, http.MethodGet, base+"&zzz=1", "", nil); res.status != http.StatusBadRequest {
		t.Errorf("an unknown parameter = %d, want 400", res.status)
	} else if code := res.body["error"].(map[string]any)["code"]; code != "unsupported_query_parameter" {
		t.Errorf("code = %v, want unsupported_query_parameter", code)
	}
}

// The by-id read: the option with its ETag; a malformed id a 400 naming id;
// an unknown option a 404.
func TestGet_Errors(t *testing.T) {
	testutil.LockOutboxTables(t) // this test writes events_outbox rows
	f := newFixture(t)
	created := f.create(t, `{"category":"`+f.prefix+`-g","name":"by id","price_adjustment_cents":5}`, nil)
	id := created.body["id"].(string)
	res := f.do(t, http.MethodGet, "/api/v1/millwork/options/"+id, "", nil)
	if res.status != http.StatusOK || res.header.Get("ETag") != `"1"` {
		t.Errorf("get = %d %s", res.status, res.raw)
	}
	if res = f.do(t, http.MethodGet, "/api/v1/millwork/options/not-a-uuid", "", nil); res.status != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", res.status)
	} else if d := res.body["error"].(map[string]any)["details"].([]any)[0].(map[string]any); d["field"] != "id" {
		t.Errorf("details = %v, want the field id", d)
	}
	if res = f.do(t, http.MethodGet, "/api/v1/millwork/options/"+uuid.NewString(), "", nil); res.status != http.StatusNotFound {
		t.Errorf("unknown option = %d, want 404", res.status)
	}
}

// The create writes its audit row and its event, in one transaction.
// The estimate route on the wire: the price is the stored options' sum, in
// the wire's integer units, and the answer carries the SKU, the description
// and the configuration it priced.
func TestEstimate_PricesFromTheStoredOptions(t *testing.T) {
	testutil.LockOutboxTables(t) // this test writes events_outbox rows
	f := newFixture(t)
	door := f.create(t, `{"category":"`+f.prefix+`-door_type","name":"Panel Door","base_price_cents":25000,"base_cost_cents":15000,"price_adjustment_cents":0,"cost_adjustment_cents":0}`, nil)
	if door.status != http.StatusCreated {
		t.Fatalf("create the door type = %d %s", door.status, door.raw)
	}
	material := f.create(t, `{"category":"`+f.prefix+`-material","name":"Mahogany","base_price_cents":0,"base_cost_cents":0,"price_adjustment_cents":15000,"cost_adjustment_cents":8000}`, nil)
	if material.status != http.StatusCreated {
		t.Fatalf("create the material = %d %s", material.status, material.raw)
	}

	res := f.do(t, http.MethodPost, "/api/v1/millwork/estimate",
		`{"product_type":"Door","selections":{"`+f.prefix+`-door_type":"Panel Door","`+f.prefix+`-material":"Mahogany"},"width":36,"height":80}`, nil)
	if res.status != http.StatusOK {
		t.Fatalf("estimate = %d %s", res.status, res.raw)
	}
	if got := res.body["unit_price_ten_thousandths"]; got != float64((25000+15000)*100) {
		t.Errorf("unit_price_ten_thousandths = %v, want %d (the stored options' sum)", got, (25000+15000)*100)
	}
	if got := res.body["unit_cost_ten_thousandths"]; got != float64((15000+8000)*100) {
		t.Errorf("unit_cost_ten_thousandths = %v, want %d (the stored options' sum)", got, (15000+8000)*100)
	}
	if sku, _ := res.body["sku"].(string); !strings.HasPrefix(sku, "NS-DR-") {
		t.Errorf("sku = %q, want the NS- form", sku)
	}
	if desc, _ := res.body["description"].(string); !strings.Contains(desc, "Panel Door") {
		t.Errorf("description = %q, want it to name the choices", desc)
	}
	if res.body["configuration_json"] == nil {
		t.Error("the estimate answered no configuration")
	}
}

// The option update on the wire: the money the server prices from is the
// dealer's to set, the write is guarded by the revision, and the answer
// carries the new revision.
func TestUpdate_SetsTheMoneyOnItsRevision(t *testing.T) {
	testutil.LockOutboxTables(t) // this test writes events_outbox rows
	f := newFixture(t)
	created := f.create(t, `{"category":"`+f.prefix+`-door_type","name":"Panel Door","base_price_cents":25000,"base_cost_cents":15000,"price_adjustment_cents":0,"cost_adjustment_cents":0}`, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d %s", created.status, created.raw)
	}
	id := created.body["id"].(string)

	res := f.do(t, http.MethodPut, "/api/v1/millwork/options/"+id,
		`{"category":"`+f.prefix+`-door_type","name":"Panel Door","base_price_cents":26000,"base_cost_cents":15000,"price_adjustment_cents":0,"cost_adjustment_cents":0,"revision":1}`,
		map[string]string{"If-Match": `"1"`})
	if res.status != http.StatusOK {
		t.Fatalf("update = %d %s", res.status, res.raw)
	}
	if res.body["base_price_cents"] != float64(26000) {
		t.Errorf("base_price_cents = %v, want 26000", res.body["base_price_cents"])
	}
	if res.body["revision"] != float64(2) {
		t.Errorf("revision = %v, want 2", res.body["revision"])
	}
	if etag := res.header.Get("ETag"); etag != `"2"` {
		t.Errorf("ETag = %q, want \"2\"", etag)
	}

	// A stale revision is refused.
	res = f.do(t, http.MethodPut, "/api/v1/millwork/options/"+id,
		`{"category":"`+f.prefix+`-door_type","name":"Panel Door","base_price_cents":27000,"base_cost_cents":15000,"price_adjustment_cents":0,"cost_adjustment_cents":0,"revision":1}`,
		map[string]string{"If-Match": `"1"`})
	if res.status != http.StatusConflict {
		t.Errorf("a stale revision = %d, want 409", res.status)
	}

	// The read shows the money the dealer set.
	res = f.do(t, http.MethodGet, "/api/v1/millwork/options/"+id, "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("get = %d %s", res.status, res.raw)
	}
	if res.body["base_price_cents"] != float64(26000) {
		t.Errorf("the read base_price_cents = %v, want 26000", res.body["base_price_cents"])
	}
}

func TestCreate_AuditRowAndEvent(t *testing.T) {
	testutil.LockOutboxTables(t) // this test writes events_outbox rows
	f := newFixture(t)
	created := f.create(t, `{"category":"`+f.prefix+`-a","name":"audited","price_adjustment_cents":1}`, nil)
	id := created.body["id"].(string)
	ctx := context.Background()
	var n int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'millwork_option' AND entity_id = $1 AND action = 'millwork_option.created'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d audit rows, want 1", n)
	}
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM events_outbox WHERE entity_type = 'millwork_option' AND entity_id = $1 AND type = 'millwork_option.created'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d events, want 1", n)
	}
}

// Idempotency: the same create twice with one key returns the first response
// and makes one row; the same key with another body is 422.
func TestCreate_IdempotentReplay(t *testing.T) {
	testutil.LockOutboxTables(t) // this test writes events_outbox rows
	f := newFixture(t)
	key := map[string]string{"Idempotency-Key": "mw-wire-" + uuid.NewString()}
	body := `{"category":"` + f.prefix + `-idem","name":"once","price_adjustment_cents":1}`
	first := f.create(t, body, key)
	if first.status != http.StatusCreated {
		t.Fatalf("first = %d %s", first.status, first.raw)
	}
	second := f.create(t, body, key)
	if second.status != http.StatusCreated || second.header.Get("Idempotency-Replayed") != "true" || second.body["id"] != first.body["id"] {
		t.Fatalf("replay = %d %s", second.status, second.raw)
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM millwork_options WHERE category = $1`, f.prefix+"-idem").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d rows after a replayed create, want 1", n)
	}
	if res := f.create(t, `{"category":"`+f.prefix+`-idem","name":"different","price_adjustment_cents":2}`, key); res.status != http.StatusUnprocessableEntity {
		t.Errorf("the same key with another body = %d, want 422", res.status)
	}
}

// RULE (ADR 0003 section 3): a create whose event cannot be recorded does
// not happen; three contenders at pool size 4 create distinct rows; the
// gated saturation test holds every connection inside its transaction.
func TestTransactionProofs(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := &fixture{t: t, db: db, prefix: "MWTX-" + uuid.NewString()[:8]}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'millwork_option' AND entity_id IN (SELECT id FROM millwork_options WHERE category LIKE $1)`, f.prefix+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type = 'millwork_option' AND entity_id IN (SELECT id FROM millwork_options WHERE category LIKE $1)`, f.prefix+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM millwork_options WHERE category LIKE $1`, f.prefix+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE sku LIKE 'NS-DR-%' AND description LIKE $1`, f.prefix+"%")
	})
	svc := millwork.NewService(millwork.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAudit(audit.NewLogger(db))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	draft := func() *millwork.Draft {
		return &millwork.Draft{Category: f.prefix + "-c", Name: "tx", PriceAdjustmentCents: 1}
	}

	// A failing event write rolls the create back.
	failing := millwork.NewService(millwork.NewRepository(db)).WithTxRunner(db).
		WithOutbox(failingEvents{}).WithAudit(audit.NewLogger(db))
	if _, err := failing.Create(ctx, draft()); err == nil {
		t.Error("create succeeded though its event could not be written")
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM millwork_options WHERE category = $1`, f.prefix+"-c").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows survived a rolled back create", n)
	}

	// Concurrent creates: distinct rows, one event each.
	const contenders, each = 3, 2
	var wg sync.WaitGroup
	errs := make(chan error, contenders*each)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if _, err := svc.Create(ctx, draft()); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent create: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM millwork_options WHERE category = $1`, f.prefix+"-c").Scan(&n); err != nil || n != contenders*each {
		t.Fatalf("%d rows, want %d (%v)", n, contenders*each, err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM events_outbox WHERE type = 'millwork_option.created' AND entity_id IN (SELECT id FROM millwork_options WHERE category = $1)`, f.prefix+"-c").Scan(&n); err != nil || n != contenders*each {
		t.Errorf("%d events for %d rows (%v)", n, contenders*each, err)
	}

	// Saturation: four gated transactions, no second pool connection.
	gated := newGatedTx(db, 4)
	satSvc := millwork.NewService(millwork.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(gated).WithAudit(audit.NewLogger(db))
	wg = sync.WaitGroup{}
	satErrs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := satSvc.Create(ctx, draft()); err != nil {
				satErrs <- err
			}
		}()
	}
	wg.Wait()
	close(satErrs)
	for err := range satErrs {
		t.Errorf("gated create: %v", err)
	}
}

type failingEvents struct{}

func (failingEvents) Write(context.Context, outbox.Event) error {
	return errOutbox{}
}

type errOutbox struct{}

func (errOutbox) Error() string { return "outbox insert failed" }

type gatedTx struct {
	db      *database.DB
	want    int32
	entered int32
	gate    sync.WaitGroup
}

func newGatedTx(db *database.DB, want int) *gatedTx {
	g := &gatedTx{db: db, want: int32(want)}
	g.gate.Add(want)
	return g
}

func (g *gatedTx) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return g.db.RunInTx(ctx, func(txCtx context.Context) error {
		if atomic.AddInt32(&g.entered, 1) <= g.want {
			g.gate.Done()
			g.gate.Wait()
		}
		return fn(txCtx)
	})
}
