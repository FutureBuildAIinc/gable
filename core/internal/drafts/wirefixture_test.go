// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts_test

// The drafts core on the wire contract (ADR 0007), tested end to end like
// the quote module's wire tests: a real Postgres, the real handlers on a
// real mux behind the real idempotency middleware, requests as JSON and
// responses read back as JSON. Nothing here touches a drafts type, so each
// test states a wire fact.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/drafts"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// fixture wires the drafts core the way serve does: the quote module's
// service (with the branch guard), the quotes kind, the drafts registry,
// service and handler, the feed hub and endpoint, behind the global
// idempotency layer.
type fixture struct {
	t          *testing.T
	db         *database.DB
	srv        *httptest.Server
	customerID uuid.UUID
	productID  uuid.UUID
	sku        string
	hub        *drafts.Hub
	draftsRepo *drafts.PostgresRepository
	svc        *drafts.Service
}

func newFixture(t *testing.T, db *database.DB) *fixture {
	return newFixtureWithAudit(t, db, nil)
}

// newFixtureWithAudit is newFixture with the drafts service's audit sink
// replaced (the rollback tests fault it before the handler is built, which
// is the only time the swap reaches the wire).
func newFixtureWithAudit(t *testing.T, db *database.DB, sink drafts.AuditSink) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	f := &fixture{t: t, db: db, customerID: uuid.New(), productID: uuid.New(), sku: "DRAFT-" + uuid.NewString()[:8]}
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Drafts Wire Co', $2, `+branch+`)`, f.customerID, "DRAFT-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5)`, f.productID, f.sku); err != nil {
		t.Fatalf("seed product: %v", err)
	}

	orderSvc := order.NewService(order.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	quoteSvc := quote.NewService(quote.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db)).
		WithBranchGuard(middleware.NewBranchGuard(db)).
		WithOrderCreator(orderSvc)
	quoteKind := quote.NewDraftKind(quoteSvc)

	draftsRepo := drafts.NewRepository(db)
	registry, err := drafts.NewRegistry(quoteKind)
	if err != nil {
		t.Fatal(err)
	}
	hub := drafts.NewHub(draftsRepo, 50*time.Millisecond, nil)
	var auditor drafts.AuditSink = audit.NewLogger(db)
	if sink != nil {
		auditor = sink
	}
	svc := drafts.NewService(draftsRepo, registry).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(auditor).
		WithBranchGuard(middleware.NewBranchGuard(db)).
		WithFeed(hub)
	handler := drafts.NewHandler(svc).WithFeedHandler(drafts.NewFeedHandler(svc, draftsRepo, hub,
		fastFeedSettings(), nil))
	f.hub = hub
	f.draftsRepo = draftsRepo
	f.svc = svc

	// The guard is the branch middleware, as serve mounts it (the kill
	// switch off in the seeded database makes every caller an
	// administrator, the dev mode the quote fixture runs in too). The
	// quote module's own routes ride along (the promotion tests move a
	// subject quote directly), and the actor middleware sits outside as in
	// serve, so an X-Acting-As marker lands on the audit rows.
	mux := http.NewServeMux()
	branchMw := middleware.NewBranchMiddleware(db).Handler
	quote.RegisterDraftRoutes(mux, handler, quoteKind, branchMw)
	quote.NewHandler(quoteSvc).RegisterRoutes(mux, branchMw)
	f.srv = httptest.NewServer(actor.Middleware(middleware.Idempotency(db)(mux)))

	t.Cleanup(func() {
		f.srv.Close()
		hub.Stop()
		// Only this package's tests write drafts and their change rows, so
		// the cleanup is the whole module's rows; the shared database's other
		// tables are cleaned by customer like the quote fixture's.
		_, _ = db.Pool.Exec(ctx, `DELETE FROM draft_events WHERE module='quotes'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM drafts WHERE module='quotes'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type='draft'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type='draft'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, f.customerID)
	})
	return f
}

// fastFeedSettings shortens the heartbeat so feed tests run promptly; the
// write deadline is fast so the stalled reader is closed in test time; the
// lifetime bound runs seconds, paced by the test's own await bound.
func fastFeedSettings() drafts.FeedSettings {
	s := drafts.DefaultFeedSettings()
	s.Heartbeat = 40 * time.Millisecond
	s.Poll = 20 * time.Millisecond
	s.WriteTimeout = 750 * time.Millisecond
	s.MaxLifetime = 5 * time.Second
	return s
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *fixture) do(method, path string, body any, headers ...string) resp {
	return f.doReq(method, path, body, headers...)
}

func (f *fixture) doReq(method, path string, body any, headers ...string) resp {
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
	req.Header.Set("X-Request-ID", "req-drafts-test")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

// payload is a minimal valid quotes payload.
func (f *fixture) payload() map[string]any {
	return map[string]any{
		"customer_id":   f.customerID.String(),
		"delivery_type": "pickup",
		"lines": []map[string]any{{
			"product_id": f.productID.String(), "sku": f.sku, "description": "2x4x8 SPF",
			"quantity": "10", "uom": "PCS", "unit_price_ten_thousandths": 55000,
		}},
	}
}

// create makes one open create draft and returns its id.
func (f *fixture) create(headers ...string) string {
	f.t.Helper()
	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()}, headers...)
	if r.status != http.StatusCreated {
		f.t.Fatalf("create draft = %d: %s", r.status, r.raw)
	}
	return str(f.t, r.body, "id")
}

func str(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	s, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s is %T (%v), want a string", key, m[key], m[key])
	}
	return s
}

func num(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	n, ok := m[key].(json.Number)
	if !ok {
		t.Fatalf("%s is %T (%v), want an integer", key, m[key], m[key])
	}
	v, err := n.Int64()
	if err != nil {
		t.Fatalf("%s = %s: %v", key, n, err)
	}
	return v
}

func errorOf(t *testing.T, r resp) (code, message string, details []map[string]any) {
	t.Helper()
	e, ok := r.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope in %s", r.raw)
	}
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	if ds, ok := e["details"].([]any); ok {
		for _, d := range ds {
			details = append(details, d.(map[string]any))
		}
	}
	return code, message, details
}

func hasDetailField(details []map[string]any, field string) bool {
	for _, d := range details {
		if d["field"] == field {
			return true
		}
	}
	return false
}

func hasBlocker(details []map[string]any, code string) bool {
	for _, d := range details {
		if d["code"] == code {
			return true
		}
	}
	return false
}

// auditRows reads the draft's audit rows in time order.
func (f *fixture) auditRows(entityID string) []map[string]any {
	f.t.Helper()
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT action, actor_kind, actor_id, acting_as, tool, changes FROM audit_log
		  WHERE entity_type='draft' AND entity_id = $1 ORDER BY created_at, id`, entityID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var action, kind, actorID, actingAs, tool *string
		var changes []byte
		if err := rows.Scan(&action, &kind, &actorID, &actingAs, &tool, &changes); err != nil {
			f.t.Fatal(err)
		}
		row := map[string]any{"action": deref(action)}
		if kind != nil {
			row["actor_kind"] = *kind
		}
		if actorID != nil {
			row["actor_id"] = *actorID
		}
		if actingAs != nil {
			row["acting_as"] = *actingAs
		}
		if tool != nil {
			row["tool"] = *tool
		}
		var ch map[string]any
		_ = json.Unmarshal(changes, &ch)
		row["changes"] = ch
		out = append(out, row)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// eventsFor reads the outbox types for one entity in position order.
func eventsForEntity(t *testing.T, db *database.DB, entityType, entityID string) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT type FROM events_outbox WHERE entity_type = $1 AND entity_id = $2 ORDER BY position`, entityType, entityID)
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
