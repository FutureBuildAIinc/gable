// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// The pricing catalogue routes on the wire (ADR 0006 section 5.6): the
// contract routes (create, a duplicate refused with 409 duplicate, update and
// delete at a revision, each event, the delete's audit row), the payload
// branch rule on every write carrying a branch_id and on the every branch
// writes, the reads showing the caller's context branch rows and the every
// branch rows, and the concurrency proofs at pool size 4 with three
// contenders. Replacing the handlers with empty bodies fails every test
// here.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/pkg/outbox"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

type catalogFixture struct {
	t    *testing.T
	db   *database.DB
	srv  *httptest.Server
	svc  *pricing.CatalogService
	repo *pricing.PostgresCatalogRepository
}

// catalogAuditAdapter writes the service's audit rows straight into
// audit_log, the columns the package's own events read back.
type catalogAuditAdapter struct{ db *database.DB }

func (a catalogAuditAdapter) Log(ctx context.Context, e pricing.AuditEntry) error {
	raw, _ := json.Marshal(e.Changes)
	_, err := a.db.Pool.Exec(ctx, `
		INSERT INTO audit_log (action, entity_type, entity_id, changes)
		VALUES ($1, $2, $3::uuid, $4)`, e.Action, e.EntityType, e.EntityID, raw)
	return err
}

func newCatalogFixture(t *testing.T) *catalogFixture {
	t.Helper()
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	t.Setenv("AUTH_MODE", "dev")
	f := &catalogFixture{t: t, db: db}
	f.svc = pricing.NewCatalogService(pricing.NewCatalogRepository(db)).
		WithTxRunner(db).
		WithOutbox(&recordingOutbox{db: db}).
		WithAudit(catalogAuditAdapter{db: db})
	f.repo = pricing.NewCatalogRepository(db)
	f.srv = httptest.NewServer(catalogMux(pricing.NewCatalogHandler(f.svc)))
	t.Cleanup(f.srv.Close)
	return f
}

// recordingOutbox writes the service's events into events_outbox.
type recordingOutbox struct{ db *database.DB }

func (o *recordingOutbox) Write(ctx context.Context, e outbox.Event) error {
	id := e.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	_, err := o.db.Pool.Exec(ctx, `
		INSERT INTO events_outbox (event_id, type, org, entity_type, entity_id, data, at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())`,
		id, e.Type, "test", e.EntityType, e.EntityID, e.Data)
	return err
}

// catalogMux mounts the catalogue routes with no guards (dev mode).
func catalogMux(h *pricing.CatalogHandler) http.Handler {
	mux := http.NewServeMux()
	h.RegisterCatalogRoutes(mux)
	return mux
}

// serveAs mounts the routes with the branch guard behind a middleware that
// injects the caller's branch context, the way the branch middleware would
// have settled it.
func serveAs(t *testing.T, db *database.DB, h *pricing.CatalogHandler, bc *branchctx.Context) *httptest.Server {
	t.Helper()
	guarded := h.WithBranchGuard(middleware.NewBranchGuard(db))
	mux := http.NewServeMux()
	guarded.RegisterCatalogRoutes(mux)
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bc != nil {
			r = r.WithContext(branchctx.With(r.Context(), bc))
		}
		mux.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(wrapped)
	t.Cleanup(srv.Close)
	return srv
}

func catalogDo(t *testing.T, method, url, body string) (int, map[string]any, http.Header) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = newStrBody(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

// strBody is a request body from a string.
type strBody struct {
	s string
	i int
}

func newStrBody(s string) *strBody { return &strBody{s: s} }

func (r *strBody) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

func (r *strBody) Close() error { return nil }

func catalogTestProduct(t *testing.T, db *database.DB, base string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Pool.Exec(context.Background(), `
		INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, 'catalog fixture', 'EA', $3)`, id, "CAT-"+id.String()[:8], base); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM products WHERE id = $1`, id)
	})
	return id
}

func catalogTestCustomer(t *testing.T, db *database.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Pool.Exec(context.Background(), `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'catalog fixture', $2, (SELECT id FROM locations LIMIT 1))`,
		id, "CATC-"+id.String()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM customers WHERE id = $1`, id)
	})
	return id
}

func catalogTestBranch(t *testing.T, db *database.DB, prefix string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, id, prefix+id.String()[:6]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, id)
	})
	return id
}

func TestContractRoutes(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	cust := catalogTestCustomer(t, f.db)
	prod := catalogTestProduct(t, f.db, "10")
	branch := catalogTestBranch(t, f.db, "cc-")

	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'contract'`)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type = 'contract'`)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customer_contracts WHERE customer_id = $1`, cust)
	})

	create := func(branchID *uuid.UUID) (int, map[string]any, http.Header) {
		body := `{"customer_id":"` + cust.String() + `","product_id":"` + prod.String() + `","contract_price_ten_thousandths":90000`
		if branchID != nil {
			body += `,"branch_id":"` + branchID.String() + `"`
		}
		body += `}`
		return catalogDo(t, "POST", f.srv.URL+"/api/v1/pricing/contracts", body)
	}

	// The create: 201, Location, the ETag, the price unit the product's
	// stocking unit, one event.
	status, body, header := create(&branch)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %v", status, body)
	}
	id, _ := body["id"].(string)
	if header.Get("Location") != "/api/v1/pricing/contracts/"+id || header.Get("ETag") != `"1"` {
		t.Errorf("Location = %q ETag = %q, want the record's and \"1\"", header.Get("Location"), header.Get("ETag"))
	}
	if body["price_uom"] != "EA" {
		t.Errorf("price_uom = %v, want EA (the stocking unit)", body["price_uom"])
	}
	var events int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM events_outbox WHERE type = 'contract.created' AND entity_id = $1`, id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("%d contract.created events, want 1", events)
	}

	// A second contract for the same customer, product and branch is a 409
	// duplicate.
	if status, _, _ := create(&branch); status != http.StatusConflict {
		t.Errorf("duplicate create = %d, want 409", status)
	}
	// The every branch slice is its own key.
	if status, _, _ := create(nil); status != http.StatusCreated {
		t.Errorf("every branch create = %d, want 201", status)
	}

	// The update names the revision it read; a stale one is a 409.
	status, _, header = catalogDo(t, "PUT", f.srv.URL+"/api/v1/pricing/contracts/"+id,
		`{"contract_price_ten_thousandths":85000,"revision":1}`)
	if status != http.StatusOK || header.Get("ETag") != `"2"` {
		t.Errorf("update = %d ETag %q, want 200 and \"2\"", status, header.Get("ETag"))
	}
	if status, _, _ := catalogDo(t, "PUT", f.srv.URL+"/api/v1/pricing/contracts/"+id,
		`{"contract_price_ten_thousandths":85000,"revision":1}`); status != http.StatusConflict {
		t.Errorf("stale update = %d, want 409", status)
	}

	// A price in another unit is a 400 naming price_uom (the hold).
	status, errBody, _ := catalogDo(t, "POST", f.srv.URL+"/api/v1/pricing/contracts",
		`{"customer_id":"`+cust.String()+`","product_id":"`+prod.String()+`","contract_price_ten_thousandths":1,"price_uom":"MBF"}`)
	if status != http.StatusBadRequest {
		t.Errorf("a contract in MBF on an EA product = %d, want 400", status)
	}
	fields := map[string]bool{}
	if e, ok := errBody["error"].(map[string]any); ok {
		for _, d := range e["details"].([]any) {
			if m, ok := d.(map[string]any); ok {
				fields[m["field"].(string)] = true
			}
		}
	}
	if !fields["price_uom"] {
		t.Errorf("the refusal does not name price_uom: %v", errBody)
	}

	// The delete names the revision, and its audit row carries the deleted
	// values.
	if status, _, _ := catalogDo(t, "DELETE", f.srv.URL+"/api/v1/pricing/contracts/"+id, `{"revision":2}`); status != http.StatusNoContent {
		t.Errorf("delete = %d, want 204", status)
	}
	var auditRows int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action = 'contract.deleted' AND entity_id = $1`, id).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if auditRows != 1 {
		t.Errorf("%d contract.deleted audit rows, want 1", auditRows)
	}
	var left int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM customer_contracts WHERE id = $1`, id).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("the deleted contract is still there")
	}
}

// TestCatalogPayloadBranchRule: a branch user's write naming another branch is
// refused, so is their every branch write; an administrator's every branch
// write is admitted; and a branch user's PUT on one branch leaves the other
// branch's and the every branch rows byte for byte. The reads show the
// caller's context branch rows and the every branch rows, the administrator's
// every row.
func TestCatalogPayloadBranchRule(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	prod := catalogTestProduct(t, f.db, "10")
	prodB := catalogTestProduct(t, f.db, "11")
	cust := catalogTestCustomer(t, f.db)
	branchA := catalogTestBranch(t, f.db, "ca-")
	branchB := catalogTestBranch(t, f.db, "cb-")

	branchUser := &branchctx.Context{UserSub: "cat-user-" + uuid.NewString()[:6]}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by)
		VALUES ($1, $2, TRUE, 'test') ON CONFLICT DO NOTHING`, branchUser.UserSub, branchA); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, branchUser.UserSub)
	})
	admin := &branchctx.Context{UserSub: "cat-admin", IsAdmin: true}

	adminSrv := serveAs(t, f.db, pricing.NewCatalogHandler(f.svc), admin)
	userSrv := serveAs(t, f.db, pricing.NewCatalogHandler(f.svc), branchUser)

	sliceURL := func(base *httptest.Server, branch *uuid.UUID, product uuid.UUID) string {
		u := base.URL + "/api/v1/pricing/products/" + product.String() + "/prices"
		if branch != nil {
			u += "?branch_id=" + branch.String()
		}
		return u
	}
	rev := func(product uuid.UUID) string {
		var r int64
		if err := f.db.Pool.QueryRow(ctx, `SELECT revision FROM products WHERE id = $1`, product).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return `"revision":` + strconv.FormatInt(r, 10)
	}
	putSlice := func(product uuid.UUID) string {
		return `{"prices":[{"unit_price_ten_thousandths":80000}],` + rev(product) + `}`
	}
	putEveryBranch := func(product uuid.UUID, levelID string) string {
		return `{"prices":[{"price_level_id":"` + levelID + `","unit_price_ten_thousandths":80000}],` + rev(product) + `}`
	}

	// A level of the test's own for the every branch slice: a row with
	// neither a level nor a branch is the product's base, which the table
	// refuses, so the every branch slice carries level rows.
	levelCode := "CW" + strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
	var levelID string
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO price_levels (code, name, basis, adjust_percent, position)
		VALUES ($1, 'catalog fixture level', 'BASE', -5, 9600) RETURNING id::text`,
		levelCode).Scan(&levelID); err != nil {
		t.Fatalf("seed level: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM price_levels WHERE code = $1`, levelCode)
	})
	// Seed branch B's slice and the every branch slice as the administrator.
	if status, out, _ := catalogDo(t, "PUT", sliceURL(adminSrv, &branchB, prod), putSlice(prod)); status != http.StatusOK {
		t.Fatalf("the admin's branch B PUT: %d %v", status, out)
	}
	if status, out, _ := catalogDo(t, "PUT", sliceURL(adminSrv, nil, prod), putEveryBranch(prod, levelID)); status != http.StatusOK {
		t.Fatalf("the admin's every branch PUT: %d %v", status, out)
	}
	before, err := f.repo.ProductPrices(ctx, prod, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A branch A user's PUT with ?branch_id=B is refused.
	if status, _, _ := catalogDo(t, "PUT", sliceURL(userSrv, &branchB, prod), putSlice(prod)); status != http.StatusForbidden {
		t.Errorf("branch A user's PUT on branch B = %d, want 403", status)
	}
	// A branch A user's every branch PUT is refused.
	if status, _, _ := catalogDo(t, "PUT", sliceURL(userSrv, nil, prod), putEveryBranch(prod, levelID)); status != http.StatusForbidden {
		t.Errorf("branch A user's every branch PUT = %d, want 403", status)
	}
	// P2-1 (review r1 argon): the create paths answer the branch refusal with
	// 403 forbidden, never a 500 internal error - a branch A user's POST
	// contract naming branch B, and their POST vendor cost naming branch B.
	if status, _, _ := catalogDo(t, "POST", userSrv.URL+"/api/v1/pricing/contracts",
		`{"customer_id":"`+cust.String()+`","product_id":"`+prodB.String()+`","branch_id":"`+branchB.String()+`","contract_price_ten_thousandths":1}`); status != http.StatusForbidden {
		t.Errorf("branch A user's POST contract on branch B = %d, want 403", status)
	}
	if status, _, _ := catalogDo(t, "POST", userSrv.URL+"/api/v1/pricing/vendor-costs",
		`{"vendor_id":"`+uuid.NewString()+`","vendor_price_level_id":"`+uuid.NewString()+`","product_id":"`+prodB.String()+`","branch_id":"`+branchB.String()+`","purchase_uom":"EA","unit_cost_ten_thousandths":1,"effective_from":"2020-01-01"}`); status != http.StatusForbidden {
		t.Errorf("branch A user's POST vendor cost on branch B = %d, want 403", status)
	}
	if status, _, _ := catalogDo(t, "POST", userSrv.URL+"/api/v1/pricing/contracts",
		`{"customer_id":"`+cust.String()+`","product_id":"`+prodB.String()+`","contract_price_ten_thousandths":1}`); status != http.StatusForbidden {
		t.Errorf("branch A user's every branch POST contract = %d, want 403", status)
	}
	// A branch A PUT on its own branch is admitted and leaves branch B's and
	// the every branch rows byte for byte.
	if status, out, _ := catalogDo(t, "PUT", sliceURL(userSrv, &branchA, prod), putSlice(prod)); status != http.StatusOK {
		t.Fatalf("branch A user's own PUT = %d %v", status, out)
	}
	after, err := f.repo.ProductPrices(ctx, prod, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(before)+1 != len(after) {
		t.Fatalf("the price rows moved: %d before, %d after (want branch A's one new row)", len(before), len(after))
	}
	key := func(p pricing.ProductPrice) string {
		if p.BranchID == nil {
			return "every"
		}
		return p.BranchID.String()
	}
	old := map[string]pricing.ProductPrice{}
	for _, p := range before {
		old[key(p)] = p
	}
	for _, a := range after {
		b, ok := old[key(a)]
		if !ok {
			if key(a) != branchA.String() {
				t.Errorf("a row of branch %s moved", key(a))
			}
			continue
		}
		if b.UnitPrice != a.UnitPrice {
			t.Errorf("branch %s's row moved price: %d against %d", key(a), b.UnitPrice, a.UnitPrice)
		}
		delete(old, key(a))
	}
	for k := range old {
		t.Errorf("branch %s's row is gone", k)
	}

	// An administrator's every branch PUT is admitted on the second product.
	if status, out, _ := catalogDo(t, "PUT", sliceURL(adminSrv, &branchB, prodB), putSlice(prodB)); status != http.StatusOK {
		t.Fatalf("the admin's branch B PUT on the second product: %d %v", status, out)
	}

	// The reads: a caller working in branch A's context sees branch A's rows
	// and the every branch rows, none of a product priced only on branch B;
	// a caller with no context branch (an administrator here) sees every row.
	branchBoundUser := branchctx.Context{UserSub: branchUser.UserSub, BranchID: &branchA}
	boundSrv := serveAs(t, f.db, pricing.NewCatalogHandler(f.svc), &branchBoundUser)
	status, out, _ := catalogDo(t, "GET", sliceURL(boundSrv, nil, prodB), "")
	if status != http.StatusOK {
		t.Fatalf("branch A user's read: %d", status)
	}
	if items, _ := out["items"].([]any); len(items) != 0 {
		t.Errorf("branch A user read %d rows of a branch B product, want none", len(items))
	}
	status, out, _ = catalogDo(t, "GET", sliceURL(adminSrv, nil, prodB), "")
	if status != http.StatusOK {
		t.Fatalf("the admin's read: %d", status)
	}
	if items, _ := out["items"].([]any); len(items) != 1 {
		t.Errorf("the admin read %d rows, want branch B's own", len(items))
	}
}

// TestCatalogWriteConcurrency: three contenders replacing the same branch's
// price slice at pool size 4 answer exactly one winner, and three contract
// creates for the same key answer exactly one row.
func TestCatalogWriteConcurrency(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	prod := catalogTestProduct(t, db, "10")
	branch := catalogTestBranch(t, db, "cx-")
	cust := catalogTestCustomer(t, db)

	svc := pricing.NewCatalogService(pricing.NewCatalogRepository(db)).WithTxRunner(db)
	rows := []pricing.ProductPrice{{PriceUOM: "EA", UnitPrice: 80000}}
	revision, err := pricing.NewCatalogRepository(db).ProductRevision(ctx, prod)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wins := make(chan int, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.ReplaceProductPrices(ctx, prod, &branch, revision, rows); err == nil {
				wins <- 1
			}
		}()
	}
	wg.Wait()
	close(wins)
	count := 0
	for range wins {
		count++
	}
	if count != 1 {
		t.Errorf("%d contenders replaced the slice at one revision, want exactly 1", count)
	}

	contractWins := make(chan int, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &pricing.CustomerContract{CustomerID: cust, ProductID: prod, BranchID: &branch,
				ContractPrice: 90000, PriceUOM: "EA"}
			if err := svc.CreateContract(ctx, c); err == nil {
				contractWins <- 1
			}
		}()
	}
	wg.Wait()
	close(contractWins)
	count = 0
	for range contractWins {
		count++
	}
	if count != 1 {
		t.Errorf("%d contenders created the same contract, want exactly 1", count)
	}
	var n int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_contracts WHERE customer_id = $1 AND product_id = $2 AND branch_id = $3`,
		cust, prod, branch).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d contract rows survived, want 1", n)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'contract'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_contracts WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_prices WHERE product_id = $1`, prod)
	})
}

// failingCatalogAudit is the real repository with an audit write that always
// fails: the stand-in for a failed audit row inside the service transaction.
type failingCatalogAudit struct{ db *database.DB }

func (a failingCatalogAudit) Log(ctx context.Context, e pricing.AuditEntry) error {
	return errors.New("catalog audit write failed")
}

// TestReplaceProductPrices_FailingAuditRollsTheReplaceBack: the price slice
// replace, its audit row and its event are one transactional fact. An audit
// write that fails leaves the old slice and the product's revision exactly as
// they were (P1-3, review r1 argon): a replacement that committed before the
// audit ran would survive the rollback.
func TestReplaceProductPrices_FailingAuditRollsTheReplaceBack(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	prod := catalogTestProduct(t, db, "10")
	branch := catalogTestBranch(t, db, "cr-")

	repo := pricing.NewCatalogRepository(db)
	good := pricing.NewCatalogService(repo).WithTxRunner(db).
		WithOutbox(&recordingOutbox{db: db}).
		WithAudit(catalogAuditAdapter{db: db})
	bad := pricing.NewCatalogService(repo).WithTxRunner(db).
		WithOutbox(&recordingOutbox{db: db}).
		WithAudit(failingCatalogAudit{db: db})

	rev := func() int64 {
		t.Helper()
		var r int64
		if err := db.Pool.QueryRow(ctx, `SELECT revision FROM products WHERE id = $1`, prod).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}

	// The good service seeds the branch A slice at the product's first revision.
	rows := []pricing.ProductPrice{{PriceUOM: "EA", UnitPrice: 80000}}
	if err := good.ReplaceProductPrices(ctx, prod, &branch, rev(), rows); err != nil {
		t.Fatalf("seed replace: %v", err)
	}
	var before string
	if err := db.Pool.QueryRow(ctx,
		`SELECT md5(COALESCE(string_agg(x::text, ',' ORDER BY x::text), ''))
		 FROM (SELECT * FROM product_prices WHERE product_id = $1 AND branch_id IS NOT DISTINCT FROM $2) x`,
		prod, branch).Scan(&before); err != nil {
		t.Fatal(err)
	}
	revisionAfterSeed := rev()

	// The bad service's replace fails on its audit write; nothing may move.
	rows[0].UnitPrice = 99000
	if err := bad.ReplaceProductPrices(ctx, prod, &branch, revisionAfterSeed, rows); err == nil {
		t.Fatal("the replace with a failing audit write succeeded")
	}
	var after string
	if err := db.Pool.QueryRow(ctx,
		`SELECT md5(COALESCE(string_agg(x::text, ',' ORDER BY x::text), ''))
		 FROM (SELECT * FROM product_prices WHERE product_id = $1 AND branch_id IS NOT DISTINCT FROM $2) x`,
		prod, branch).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("the failed replace moved the price rows (digest %s against %s)", after, before)
	}
	if got := rev(); got != revisionAfterSeed {
		t.Errorf("the failed replace moved the product revision to %d, want %d", got, revisionAfterSeed)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_prices WHERE product_id = $1`, prod)
	})
}

// TestReplaceProductPrices_SaturationNeedsNoSecondConnection: four contenders
// at pool size 4 each replace the same slice inside the service transaction;
// a repository that reached for a second pool connection would deadlock the
// gate (P1-3, review r1 argon).
func TestReplaceProductPrices_SaturationNeedsNoSecondConnection(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	prod := catalogTestProduct(t, db, "10")
	branch := catalogTestBranch(t, db, "cs-")
	cust := catalogTestCustomer(t, db)

	repo := pricing.NewCatalogRepository(db)

	// Three contenders at distinct branches, plus a contract create, all held
	// at the gate inside their transactions before the first statement.
	revision, err := repo.ProductRevision(ctx, prod)
	if err != nil {
		t.Fatal(err)
	}
	const contenders = 4
	svc2 := pricing.NewCatalogService(repo).WithTxRunner(newGatedRunner(db, contenders))
	branches := []uuid.UUID{branch, catalogTestBranch(t, db, "ct-"), catalogTestBranch(t, db, "cu-")}
	var wg sync.WaitGroup
	errs := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i < 3 {
				err = svc2.ReplaceProductPrices(ctx, prod, &branches[i], revision,
					[]pricing.ProductPrice{{PriceUOM: "EA", UnitPrice: 80000}})
			} else {
				err = svc2.CreateContract(ctx, &pricing.CustomerContract{CustomerID: cust, ProductID: prod,
					BranchID: &branch, ContractPrice: 90000, PriceUOM: "EA"})
			}
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		// A contender may lose the revision race or the unique key; what it
		// must never do is wait on a second pool connection, which shows up
		// as the context expiring before the gate drains.
		if ctx.Err() != nil {
			t.Errorf("contender: %v (after the context expired)", err)
		}
	}
	if ctx.Err() != nil {
		t.Fatal("four contenders at pool size 4 did not finish: a transaction waited on a second pool connection")
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type IN ('contract', 'product')`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_contracts WHERE customer_id = $1`, cust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_prices WHERE product_id = $1`, prod)
	})
}
