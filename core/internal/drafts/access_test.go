// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts_test

// The access table over the key kinds (ADR 0007 section 5.1, section 11's
// Scopes row): a propose only key, a commit key, the coarse read and write
// keys, another module's propose key, a branch bound key, an unbound key
// and a person, each against every new route, by path, by body and on the
// feed. Doubled slashes and dot segments widen nothing. And the cycle 5
// exit line: a quotes:propose key cannot commit.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/drafts"
	"github.com/gablelbm/gable/internal/links"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// accessFixture wires both kinds, the quote and order entity routes and the
// quotes link routes behind the real machine key core, so every new route
// is judged exactly as serve judges it.
type accessFixture struct {
	t      *testing.T
	db     *database.DB
	srv    *httptest.Server // the person's server (no machine key core)
	keySrv *httptest.Server // the same stack behind the machine key core
	keys   *techadmin.Service

	customerID uuid.UUID
	productID  uuid.UUID
	sku        string
	branchID   uuid.UUID
}

func newAccessFixture(t *testing.T, db *database.DB) *accessFixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	f := &accessFixture{t: t, db: db, customerID: uuid.New(), productID: uuid.New(), sku: "ACC-" + uuid.NewString()[:8]}
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if err := db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&f.branchID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Access Wire Co', $2, `+branch+`)`, f.customerID, "ACC-"+uuid.NewString()[:8]); err != nil {
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
	quoteSvc := quote.NewService(quote.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db)).
		WithBranchGuard(middleware.NewBranchGuard(db)).
		WithOrderCreator(orderSvc)
	quoteKind := quote.NewDraftKind(quoteSvc)
	orderKind := order.NewDraftKind(orderSvc)
	draftsRepo := drafts.NewRepository(db)
	registry, err := drafts.NewRegistry(quoteKind, orderKind)
	if err != nil {
		t.Fatal(err)
	}
	hub := drafts.NewHub(draftsRepo, 50_000_000, nil)
	t.Cleanup(hub.Stop)
	draftsSvc := drafts.NewService(draftsRepo, registry).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db)).
		WithBranchGuard(middleware.NewBranchGuard(db)).
		WithFeed(hub)
	draftsHandler := drafts.NewHandler(draftsSvc).WithFeedHandler(
		drafts.NewFeedHandler(draftsSvc, draftsRepo, hub, drafts.DefaultFeedSettings(), nil))

	quoteRow, _ := links.RowFor("quotes", false)
	quoteDraftRow, _ := links.RowFor("quotes", true)
	linksHandler := links.NewHandler(links.Settings{},
		links.Entity{Row: quoteRow, Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			q, err := quoteSvc.GetQuoteByIDOrNumber(ctx, raw)
			if err != nil {
				return uuid.Nil, "", err
			}
			return q.ID, q.Number, nil
		}},
		links.Entity{Row: quoteDraftRow, Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			d, err := draftsSvc.Get(ctx, "quotes", uuid.MustParse(raw))
			if err != nil {
				return uuid.Nil, "", err
			}
			return d.ID, "", nil
		}},
	)

	mux := http.NewServeMux()
	branchMw := middleware.NewBranchMiddleware(db).Handler
	quote.RegisterDraftRoutes(mux, draftsHandler, quoteKind, branchMw)
	order.RegisterDraftRoutes(mux, draftsHandler, orderKind, branchMw)
	quote.NewHandler(quoteSvc).RegisterRoutes(mux, branchMw)
	order.NewHandler(orderSvc).RegisterRoutes(mux, branchMw)
	links.RegisterOne(mux, linksHandler, "quotes", false, branchMw)
	links.RegisterOne(mux, linksHandler, "quotes", true, branchMw)
	// The integration seam stand-in: skipped by the machine key core (the
	// public path, as serve's list arranges), answering its own 401 when the
	// X-Integration-Key is missing.
	mux.HandleFunc("POST /api/integration/quotes", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, &httpx.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "the integration seam authenticates with X-Integration-Key"})
	})
	f.srv = httptest.NewServer(actor.Middleware(middleware.Idempotency(db)(mux)))

	f.keys = techadmin.NewService(techadmin.NewRepository(db)).WithTxRunner(db)
	// The refusal auditor is wired as serve wires it, so the key.branch_refused
	// row lands; the public paths skip the seam as serve's list arranges.
	mkAuth := middleware.NewMachineKeyAuth(machineKeyValidatorForTests{svc: f.keys}, audit.NewLogger(db), []string{"/api/integration/"}, nil)
	f.keySrv = httptest.NewServer(mkAuth.Handler(f.srv.Config.Handler))

	t.Cleanup(func() {
		f.srv.Close()
		f.keySrv.Close()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM draft_events`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM drafts`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type IN ('draft','api_key','quote','order') AND created_at > now() - interval '1 hour' AND entity_id::text IN (SELECT id::text FROM drafts)`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type IN ('draft','quote')`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, f.customerID)
	})
	return f
}

// mint mints a key with the given scopes and optional branch, failing the
// test when the mint refuses.
func (f *accessFixture) mint(name string, scopes []string, branch *uuid.UUID) string {
	f.t.Helper()
	raw, _, err := f.keys.GenerateKey(context.Background(), name, scopes, branch)
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

// doKey runs one request through the machine key core.
func (f *accessFixture) doKey(rawKey, method, path string, body any, headers ...string) resp {
	f.t.Helper()
	return f.doOn(f.keySrv, method, path, body, append(headers, "Authorization", "Bearer "+rawKey)...)
}

// do runs one request as the person (no machine key core in front).
func (f *accessFixture) do(method, path string, body any, headers ...string) resp {
	f.t.Helper()
	return f.doOn(f.srv, method, path, body, headers...)
}

func (f *accessFixture) doOn(srv *httptest.Server, method, path string, body any, headers ...string) resp {
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
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		f.t.Fatal(err)
	}
	if rdr != nil {
		if _, ok := body.(string); ok {
			req.Header.Set("Content-Type", "application/octet-stream")
		} else {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	req.Header.Set("X-Request-ID", "req-access-test")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return parseResp(res.StatusCode, res.Header, raw)
}

// parseResp decodes a response body once, the shared helper of the plain
// and the bounded reads.
func parseResp(status int, header http.Header, raw []byte) resp {
	out := resp{status: status, header: header, raw: raw}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

// doKeyStreamHead opens a keyed stream, reads its first bytes and closes:
// enough to know the scope check admitted or refused it, never waiting for
// an end the stream does not reach inside its lifetime bound.
func (f *accessFixture) doKeyStreamHead(rawKey, path string) resp {
	f.t.Helper()
	req, err := http.NewRequest("GET", f.keySrv.URL+path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+rawKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	head := make([]byte, 512)
	n, _ := res.Body.Read(head)
	return parseResp(res.StatusCode, res.Header, head[:n])
}

// payload is a minimal valid quotes payload.
func (f *accessFixture) payload() map[string]any {
	return map[string]any{
		"customer_id":   f.customerID.String(),
		"delivery_type": "pickup",
		"lines": []map[string]any{{
			"product_id": f.productID.String(), "sku": f.sku, "description": "2x4x8 SPF",
			"quantity": "10", "uom": "PCS", "unit_price_ten_thousandths": 55000,
		}},
	}
}

// orderPayload is a minimal valid orders payload.
func (f *accessFixture) orderPayload() map[string]any {
	return map[string]any{
		"customer_id":   f.customerID.String(),
		"delivery_type": "pickup",
		"lines":         []map[string]any{{"product_id": f.productID.String(), "quantity": "10"}},
	}
}

// refuse asserts a keyed refusal: the status and, when the caller expects
// the named scope, the refused scope in the envelope.
func refuse(t *testing.T, r resp, status int, scope string) {
	t.Helper()
	if r.status != status {
		t.Errorf("status = %d (%s), want %d", r.status, pickCode(r), status)
		return
	}
	if scope != "" {
		if code := pickCode(r); code != "forbidden" {
			t.Errorf("code = %q, want forbidden", code)
		}
		if !strings.Contains(string(r.raw), scope) {
			t.Errorf("the refusal does not name %s: %s", scope, r.raw)
		}
	}
}

func pickCode(r resp) string {
	if e, ok := r.body["error"].(map[string]any); ok {
		c, _ := e["code"].(string)
		return c
	}
	return ""
}

// TestAccessTable_KeyKindsAgainstEveryNewRoute runs the whole section 5.1
// table: for each key kind, every draft route, the promotion, the feed, the
// link routes and the entity routes answer exactly the statuses the table
// gives, with the refused scope named.
func TestAccessTable_KeyKindsAgainstEveryNewRoute(t *testing.T) {
	f := newAccessFixture(t, testutil.RequireDB(t))

	// One quote entity (for the entity and link rows) made by a person.
	q := f.do("POST", "/api/v1/quotes", f.payload())
	if q.status != http.StatusCreated {
		t.Fatalf("seed quote = %d: %s", q.status, q.raw)
	}
	quoteID := str(t, q.body, "id")

	kinds := []struct {
		name  string
		scop  []string
		want  map[string]int
		refus map[string]string // route prefix -> the refused scope named
	}{
		{
			name: "propose only",
			scop: []string{"quotes:propose"},
			want: map[string]int{
				"list": 200, "create": 201, "read": 200, "put": 200, "transitions": 200,
				"promote": 403, "feed": 200, "link": 403, "draft_link": 200,
				"entity_read": 403, "entity_create": 403, "entity_put": 403, "entity_transition": 403, "file": 403,
				"orders_list": 403, "orders_create": 403, "orders_promote": 403,
			},
			refus: map[string]string{
				"promote": "quotes:commit", "link": "quotes:read", "entity_read": "quotes:read",
				"entity_create": "quotes:write", "entity_put": "quotes:write", "entity_transition": "quotes:write",
				"file": "quotes:write", "orders_list": "orders:propose", "orders_create": "orders:propose",
				"orders_promote": "orders:commit",
			},
		},
		{
			name: "commit",
			scop: []string{"quotes:commit"},
			want: map[string]int{
				"list": 200, "create": 201, "read": 200, "put": 200, "transitions": 200,
				"promote": 201, "feed": 200, "link": 403, "draft_link": 200,
				"entity_read": 403, "entity_create": 403, "entity_put": 403, "entity_transition": 403, "file": 403,
				"orders_list": 403, "orders_create": 403, "orders_promote": 403,
			},
			refus: map[string]string{
				"link": "quotes:read", "entity_read": "quotes:read",
				"entity_create": "quotes:write", "entity_put": "quotes:write", "entity_transition": "quotes:write",
				"file": "quotes:write", "orders_list": "orders:propose", "orders_create": "orders:propose",
				"orders_promote": "orders:commit",
			},
		},
		{
			name: "coarse read",
			scop: []string{"quotes:read"},
			want: map[string]int{
				"list": 403, "create": 403, "read": 403, "put": 403, "transitions": 403,
				"promote": 403, "feed": 403, "link": 200, "draft_link": 403,
				"entity_read": 200, "entity_create": 403, "entity_put": 403, "entity_transition": 403, "file": 403,
				"orders_list": 403, "orders_create": 403, "orders_promote": 403,
			},
			refus: map[string]string{
				"list": "quotes:propose", "create": "quotes:propose", "read": "quotes:propose", "put": "quotes:propose",
				"transitions": "quotes:propose", "promote": "quotes:commit", "feed": "quotes:propose",
				"draft_link": "quotes:propose", "entity_create": "quotes:write", "entity_put": "quotes:write",
				"entity_transition": "quotes:write", "file": "quotes:write",
				"orders_list": "orders:propose", "orders_create": "orders:propose", "orders_promote": "orders:commit",
			},
		},
		{
			name: "coarse write",
			scop: []string{"quotes:write"},
			want: map[string]int{
				"list": 403, "create": 403, "read": 403, "put": 403, "transitions": 403,
				"promote": 403, "feed": 403, "link": 403, "draft_link": 403,
				"entity_read": 403, "entity_create": 201, "entity_put": 200, "entity_transition": 200, "file": 200,
				"orders_list": 403, "orders_create": 403, "orders_promote": 403,
			},
			refus: map[string]string{
				"list": "quotes:propose", "create": "quotes:propose", "read": "quotes:propose", "put": "quotes:propose",
				"transitions": "quotes:propose", "promote": "quotes:commit", "feed": "quotes:propose",
				"draft_link": "quotes:propose", "link": "quotes:read", "entity_read": "quotes:read",
				"orders_list": "orders:propose", "orders_create": "orders:propose", "orders_promote": "orders:commit",
			},
		},
	}

	for _, kind := range kinds {
		t.Run(kind.name, func(t *testing.T) {
			raw := f.mint("access "+kind.name, kind.scop, nil)

			// A draft of this key's own (create answers the table's create
			// row; the later rows reuse it).
			r := f.doKey(raw, "POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()})
			if r.status != kind.want["create"] {
				t.Errorf("create = %d (%s), want %d", r.status, pickCode(r), kind.want["create"])
			}
			var draftID string
			if r.status == http.StatusCreated {
				draftID = str(t, r.body, "id")
			} else {
				draftID = uuid.NewString() // refusals never reach the handler
			}
			check := func(row string, r resp) {
				t.Helper()
				want := kind.want[row]
				if want == 403 {
					refuse(t, r, http.StatusForbidden, kind.refus[row])
				} else if r.status != want {
					t.Errorf("%s = %d (%s), want %d", row, r.status, pickCode(r), want)
				}
			}
			check("list", f.doKey(raw, "GET", "/api/v1/drafts/quotes", nil))
			check("read", f.doKey(raw, "GET", "/api/v1/drafts/quotes/"+draftID, nil))
			check("put", f.doKey(raw, "PUT", "/api/v1/drafts/quotes/"+draftID, map[string]any{"payload": f.payload(), "revision": 1}))
			check("transitions", f.doKey(raw, "POST", "/api/v1/drafts/quotes/"+draftID+"/transitions", map[string]any{"to": "discarded", "revision": 2}))
			// The promotion row needs an open draft; reopen when the
			// transitions row discarded it, and re-read the revision.
			f.doKey(raw, "POST", "/api/v1/drafts/quotes/"+draftID+"/transitions", map[string]any{"to": "open", "revision": 3})
			promote := f.doKey(raw, "POST", "/api/v1/drafts/quotes/"+draftID+"/promote", map[string]any{"revision": 4})
			if kind.want["promote"] == 403 {
				refuse(t, promote, http.StatusForbidden, kind.refus["promote"])
			} else if promote.status != kind.want["promote"] {
				t.Errorf("promote = %d (%s), want %d", promote.status, pickCode(r), kind.want["promote"])
			}
			// The feed: read the first bytes and close (a stream never
			// ends on its own inside the lifetime bound).
			check("feed", f.doKeyStreamHead(raw, "/api/v1/drafts/quotes/feed"))
			// The link routes, against the person's quote and this key's
			// draft (a refusal never reaches the resolver, so any id does).
			check("link", f.doKey(raw, "GET", "/api/v1/links/quotes/"+quoteID, nil))
			check("draft_link", f.doKey(raw, "GET", "/api/v1/links/drafts/quotes/"+draftID, nil))
			// The entity routes.
			check("entity_read", f.doKey(raw, "GET", "/api/v1/quotes/"+quoteID, nil))
			created := f.doKey(raw, "POST", "/api/v1/quotes", f.payload())
			check("entity_create", created)
			entityID := quoteID
			if created.status == http.StatusCreated {
				entityID = str(t, created.body, "id")
			}
			check("entity_put", f.doKey(raw, "PUT", "/api/v1/quotes/"+entityID, func() map[string]any {
				b := f.payload()
				b["revision"] = 1
				return b
			}()))
			check("entity_transition", f.doKey(raw, "POST", "/api/v1/quotes/"+entityID+"/transitions", map[string]any{"to": "sent", "revision": 2}))
			// The file route on a fresh draft-status quote.
			fresh := f.doKey(raw, "POST", "/api/v1/quotes", f.payload())
			freshID := quoteID
			if fresh.status == http.StatusCreated {
				freshID = str(t, fresh.body, "id")
			}
			req, _ := http.NewRequest("PUT", f.keySrv.URL+"/api/v1/quotes/"+freshID+"/file", strings.NewReader("PDF-BYTES"))
			req.Header.Set("Authorization", "Bearer "+raw)
			req.Header.Set("Content-Type", "application/pdf")
			req.Header.Set("If-Match", `"1"`)
			fileRes, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			fileRaw, _ := io.ReadAll(io.LimitReader(fileRes.Body, 4096))
			fileRes.Body.Close()
			check("file", parseResp(fileRes.StatusCode, fileRes.Header, fileRaw))
			// The orders kind: a propose key of quotes reaches nothing of
			// orders, and neither do the others.
			check("orders_list", f.doKey(raw, "GET", "/api/v1/drafts/orders", nil))
			check("orders_create", f.doKey(raw, "POST", "/api/v1/drafts/orders", map[string]any{"payload": f.orderPayload()}))
			check("orders_promote", f.doKey(raw, "POST", "/api/v1/drafts/orders/"+uuid.NewString()+"/promote", map[string]any{"revision": 1}))
		})
	}

	// The orders:propose key mirrors the quotes rows on the orders kind.
	t.Run("orders propose only", func(t *testing.T) {
		raw := f.mint("access orders propose", []string{"orders:propose"}, nil)
		r := f.doKey(raw, "POST", "/api/v1/drafts/orders", map[string]any{"payload": f.orderPayload()})
		if r.status != http.StatusCreated {
			t.Fatalf("orders create = %d: %s", r.status, r.raw)
		}
		id := str(t, r.body, "id")
		refuse(t, f.doKey(raw, "POST", "/api/v1/drafts/orders/"+id+"/promote", map[string]any{"revision": 1}),
			http.StatusForbidden, "orders:commit")
		refuse(t, f.doKey(raw, "GET", "/api/v1/drafts/quotes", nil), http.StatusForbidden, "quotes:propose")
		refuse(t, f.doKey(raw, "GET", "/api/v1/orders", nil), http.StatusForbidden, "orders:read")
	})

	// A person: the session reaches drafts and promotion alike (the confirm
	// gate binds marked agent sessions, not people).
	t.Run("a person", func(t *testing.T) {
		r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()})
		if r.status != http.StatusCreated {
			t.Fatalf("person create = %d: %s", r.status, r.raw)
		}
		id := str(t, r.body, "id")
		if p := f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1}); p.status != http.StatusCreated {
			t.Errorf("person promote = %d: %s", p.status, p.raw)
		}
	})
}

// TestProposeKeyCannotCommit is the cycle 5 exit line: the key creates and
// edits a draft (201, 200), is refused the promotion (403 naming
// quotes:commit) and the quote entity writes (403 naming quotes:write); a
// quotes:commit key then promotes the same revision (201, a Q- quote,
// quote.created then draft.promoted, the audit row naming the propose key
// as proposer and the commit key as committer).
func TestProposeKeyCannotCommit(t *testing.T) {
	f := newAccessFixture(t, testutil.RequireDB(t))
	propose := f.mint("propose only", []string{"quotes:propose"}, nil)
	commit := f.mint("commit", []string{"quotes:commit"}, nil)

	// The propose key creates and edits a draft.
	r := f.doKey(propose, "POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()})
	if r.status != http.StatusCreated {
		t.Fatalf("propose create = %d: %s", r.status, r.raw)
	}
	id := str(t, r.body, "id")
	r = f.doKey(propose, "PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload(), "revision": 1})
	if r.status != http.StatusOK {
		t.Fatalf("propose edit = %d: %s", r.status, r.raw)
	}

	// It cannot commit: the promotion, and every quote entity write.
	refuse(t, f.doKey(propose, "POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 2}),
		http.StatusForbidden, "quotes:commit")
	refuse(t, f.doKey(propose, "POST", "/api/v1/quotes", f.payload()), http.StatusForbidden, "quotes:write")
	refuse(t, f.doKey(propose, "PUT", "/api/v1/quotes/"+uuid.NewString(), f.payload()), http.StatusForbidden, "quotes:write")
	refuse(t, f.doKey(propose, "POST", "/api/v1/quotes/"+uuid.NewString()+"/transitions", map[string]any{"to": "sent", "revision": 1}),
		http.StatusForbidden, "quotes:write")

	// The commit key promotes the same revision.
	p := f.doKey(commit, "POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 2})
	if p.status != http.StatusCreated {
		t.Fatalf("commit promote = %d: %s", p.status, p.raw)
	}
	promoted := p.body["promoted"].(map[string]any)
	entity := promoted["entity_id"].(string)
	if number, _ := promoted["number"].(string); !strings.HasPrefix(number, "Q-") {
		t.Errorf("promoted number = %v, want a Q- quote", promoted["number"])
	}

	// quote.created then draft.promoted, in outbox order.
	types := eventsForEntity(t, f.db, "quote", entity)
	if len(types) != 1 || types[0] != "quote.created" {
		t.Errorf("quote events = %v", types)
	}
	draftTypes := eventsForEntity(t, f.db, "draft", id)
	if len(draftTypes) != 1 || draftTypes[0] != "draft.promoted" {
		t.Fatalf("draft events = %v", draftTypes)
	}

	// The audit row names the propose key as proposer and the commit key as
	// committer: one row holds who proposed and who confirmed.
	var proposerKind, proposerID, committerID string
	var changes map[string]any
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT actor_id, actor_kind, changes FROM audit_log
		  WHERE entity_type='draft' AND entity_id=$1 AND action='draft.promoted'`, id).
		Scan(&committerID, &proposerKind, &changes); err != nil {
		t.Fatal(err)
	}
	if proposerKind != "key" {
		t.Errorf("the committer's actor kind = %q, want the key", proposerKind)
	}
	proposers, _ := changes["proposers"].([]any)
	if len(proposers) != 1 {
		t.Fatalf("proposers = %v, want the one proposing key", changes["proposers"])
	}
	proposer, _ := proposers[0].(map[string]any)
	proposerID, _ = proposer["id"].(string)
	if proposerID == "" || committerID == "" || proposerID == committerID {
		t.Errorf("proposer %q and committer %q must be the two distinct keys", proposerID, committerID)
	}
}

// TestBoundKeyPinnedToItsBranch pins section 5.5: a branch bound key is
// pinned to its branch (its list sees only it, its writes resolve to it),
// is refused another branch in X-Branch-Id with its key.branch_refused
// audit row, and is refused a foreign payload branch_id on the draft create
// and on the module's own create alike; an unbound key names any branch
// while the single branch kill switch is off.
func TestBoundKeyPinnedToItsBranch(t *testing.T) {
	f := newAccessFixture(t, testutil.RequireDB(t))

	// A second branch, and a key bound to it.
	other := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, other, "ACC-"+other.String()[:8]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, other)
	})
	bound := f.mint("bound", []string{"quotes:propose", "quotes:write"}, &other)
	unbound := f.mint("unbound", []string{"quotes:propose"}, nil)

	// A draft in the default branch (a person's) and one the bound key
	// creates: its payload names no branch, so the write resolves to the
	// pinned branch.
	persons := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()})
	if persons.status != http.StatusCreated {
		t.Fatalf("person create = %d: %s", persons.status, persons.raw)
	}
	r := f.doKey(bound, "POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()})
	if r.status != http.StatusCreated {
		t.Fatalf("bound create = %d: %s", r.status, r.raw)
	}
	boundDraft := str(t, r.body, "id")
	if b, _ := r.body["branch_id"].(string); b != other.String() {
		t.Errorf("the bound key's draft landed in branch %v, want its pinned branch", r.body["branch_id"])
	}

	// The bound key's list sees only its branch: not the person's draft.
	list := f.doKey(bound, "GET", "/api/v1/drafts/quotes", nil)
	if list.status != http.StatusOK {
		t.Fatalf("bound list = %d: %s", list.status, list.raw)
	}
	items, _ := list.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("the bound key's list carries %d drafts, want only its branch's", len(items))
	}
	if item, _ := items[0].(map[string]any); item["id"] != boundDraft {
		t.Errorf("the bound key's list carries %v, want its own draft", items[0])
	}

	// A payload naming another branch: refused on the draft create and on
	// the module's own create alike.
	payloadOther := f.payload()
	payloadOther["branch_id"] = f.branchID.String()
	refuse(t, f.doKey(bound, "POST", "/api/v1/drafts/quotes", map[string]any{"payload": payloadOther}),
		http.StatusForbidden, "payload.branch_id")
	refuse(t, f.doKey(bound, "POST", "/api/v1/quotes", payloadOther),
		http.StatusForbidden, "branch_id")

	// X-Branch-Id naming another branch: refused, with the audit row.
	before := auditCount(t, f.db, "key.branch_refused")
	refuse(t, f.doKey(bound, "GET", "/api/v1/drafts/quotes", nil, "X-Branch-Id", f.branchID.String()),
		http.StatusForbidden, "")
	if after := auditCount(t, f.db, "key.branch_refused"); after <= before {
		t.Errorf("key.branch_refused rows: %d then %d, want the refusal audited", before, after)
	}
	// Naming its own branch in the header is fine.
	if r := f.doKey(bound, "GET", "/api/v1/drafts/quotes", nil, "X-Branch-Id", other.String()); r.status != http.StatusOK {
		t.Errorf("bound list naming its own branch = %d: %s", r.status, r.raw)
	}

	// An unbound key may name any branch while the kill switch is off (the
	// middleware treats it as an administrator).
	payloadAny := f.payload()
	payloadAny["branch_id"] = other.String()
	if r := f.doKey(unbound, "POST", "/api/v1/drafts/quotes", map[string]any{"payload": payloadAny}); r.status != http.StatusCreated {
		t.Errorf("unbound create naming another branch = %d (%s), want 201 while the kill switch is off", r.status, pickCode(r))
	}
}

// TestDirtyPathsWidenNothing pins the fail closed rule (section 5.2):
// doubled slashes and dot segments never widen a key's reach, and never
// reach a handler for anyone.
func TestDirtyPathsWidenNothing(t *testing.T) {
	f := newAccessFixture(t, testutil.RequireDB(t))
	propose := f.mint("dirty paths", []string{"quotes:propose"}, nil)

	dirty := []string{
		"/api/v1//drafts/quotes",
		"/api/v1/drafts//quotes",
		"/api/v1/drafts/quotes//",
		"/api/v1/drafts/../drafts/quotes",
		"/api/v1/drafts/quotes/../../quotes",
		"/api/v1/links/../drafts/quotes/" + uuid.NewString(),
		"/api/v1//links/quotes/" + uuid.NewString(),
	}
	for _, path := range dirty {
		// A key is refused by the auth core before the router cleans
		// anything: the dirty spelling names no scope target.
		r := f.doKey(propose, "GET", path, nil)
		if r.status/100 == 2 {
			t.Errorf("the keyed dirty path %q answered %d with a body", path, r.status)
		}
		// A person is redirected to the cleaned spelling and never served
		// the dirty one; the client that does not follow sees the redirect.
		req, _ := http.NewRequest("GET", f.srv.URL+path, nil)
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode/100 != 3 {
			t.Errorf("the person's dirty path %q answered %d, want the redirect to the cleaned spelling", path, res.StatusCode)
		}
	}
}

// TestAKeyCannotRouteAroundTheGateOnTheSeam pins section 5.6: the
// integration seam is outside the confirm gate, and a scoped key reaching
// it is answered by the seam's own authentication, never by a quote write.
func TestAKeyCannotRouteAroundTheGateOnTheSeam(t *testing.T) {
	f := newAccessFixture(t, testutil.RequireDB(t))
	propose := f.mint("seam probe", []string{"quotes:propose"}, nil)
	write := f.mint("seam probe write", []string{"quotes:write"}, nil)
	for _, raw := range []string{propose, write} {
		r := f.doKey(raw, "POST", "/api/integration/quotes", f.payload())
		if r.status != http.StatusUnauthorized {
			t.Errorf("the keyed seam request (scopes held) = %d (%s), want the seam's own 401", r.status, pickCode(r))
		}
	}
}

// auditCount counts the audit rows of one action written in this test run.
func auditCount(t *testing.T, db *database.DB, action string) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action = $1 AND created_at > now() - interval '5 minutes'`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
