// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// world is a database with two contractor accounts, a few dealer products and
// the doubles of the collaborators the service talks to.
type world struct {
	t     *testing.T
	db    *database.DB
	svc   *customerquote.Service
	store *customerquote.Store

	branch uuid.UUID
	a, b   customerquote.Actor
	projA  uuid.UUID
	projB  uuid.UUID
	catLum uuid.UUID
	catHw  uuid.UUID
	// products: stud (lumber), ply (lumber), nail (hardware)
	stud, ply, nail uuid.UUID

	costs  *fakeCosts
	mail   *recMail
	orders *fakeOrders
	logs   *syncBuf
	now    time.Time
	mu     sync.Mutex
}

type syncBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

// fakeCosts is the dealer's price for the two accounts: a fixed unit cost per
// product, and one dealer quote the test can name.
type fakeCosts struct {
	items  map[uuid.UUID]customerquote.CatalogItem
	source map[uuid.UUID]*customerquote.SourceQuote
	mu     sync.Mutex
	calls  int
}

func (f *fakeCosts) CatalogCost(_ context.Context, _, productID uuid.UUID, _ httpx.Quantity) (*customerquote.CatalogItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	it, ok := f.items[productID]
	if !ok {
		return nil, nil
	}
	return &it, nil
}

func (f *fakeCosts) DealerQuote(_ context.Context, _, quoteID uuid.UUID) (*customerquote.SourceQuote, error) {
	return f.source[quoteID], nil
}

// recMail records what the notifier was asked to send.
type recMail struct {
	mu     sync.Mutex
	links  []string
	codes  []string
	fail   bool
	linkOK bool
	// the channels a code can really go out on
	email, sms bool
}

func (m *recMail) Channels() customerquote.CodeChannels {
	m.mu.Lock()
	defer m.mu.Unlock()
	return customerquote.CodeChannels{Email: m.email, SMS: m.sms}
}

func (m *recMail) setChannels(email, sms bool) {
	m.mu.Lock()
	m.email, m.sms = email, sms
	m.mu.Unlock()
}

func (m *recMail) SendLink(_ context.Context, _ customerquote.Contact, _, _, link string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.links = append(m.links, link)
	return m.linkOK, nil
}

func (m *recMail) SendCode(_ context.Context, _, _, _, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return fmt.Errorf("no carrier")
	}
	m.codes = append(m.codes, code)
	return nil
}

func (m *recMail) lastCode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.codes) == 0 {
		return ""
	}
	return m.codes[len(m.codes)-1]
}

// fakeOrders stands in for the dealer's order path: it writes a draft order
// row and its lines, as the order service does, priced at the captured cost.
type fakeOrders struct {
	w     *world
	mu    sync.Mutex
	calls int
	last  customerquote.OrderPlacement
}

func (f *fakeOrders) PlaceOrder(ctx context.Context, p customerquote.OrderPlacement) (uuid.UUID, error) {
	f.mu.Lock()
	f.calls++
	f.last = p
	f.mu.Unlock()
	id := uuid.New()
	ex := f.w.db.GetExecutor(ctx)
	if _, err := ex.Exec(ctx, `INSERT INTO orders (id, customer_id, project_id, status, total_amount, branch_id, currency, delivery_type)
		VALUES ($1, $2, $3, 'DRAFT', 0, $4, 'USD', 'DELIVERY')`, id, p.CustomerID, p.ProjectID, f.w.branch); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func newWorld(t *testing.T) *world {
	t.Helper()
	return newWorldPool(t, 0)
}

func newWorldPool(t *testing.T, pool int32) *world {
	t.Helper()
	var db *database.DB
	if pool > 0 {
		db = testutil.RequireDBMaxConns(t, pool)
	} else {
		db = testutil.RequireDB(t)
	}
	ctx := context.Background()
	w := &world{t: t, db: db, logs: &syncBuf{}, now: time.Now().UTC().Truncate(time.Second)}
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	if err := db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&w.branch); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	mk := func(label string) (customerquote.Actor, uuid.UUID) {
		id := uuid.New()
		must(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`,
			id, label, "CQT-"+id.String()[:8], w.branch)
		uid := uuid.New()
		return customerquote.Actor{CustomerID: id, UserID: &uid, Name: label + " Buyer", Email: "buyer@" + id.String()[:6] + ".example", Role: "owner"}, id
	}
	var ca, cb uuid.UUID
	w.a, ca = mk("Quote Contractor A")
	w.b, cb = mk("Quote Contractor B")
	w.projA, w.projB = uuid.New(), uuid.New()
	must(`INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, 'Maple Street Deck', 'Active')`, w.projA, ca)
	must(`INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, 'Other Job', 'Active')`, w.projB, cb)
	w.catLum, w.catHw = uuid.New(), uuid.New()
	for _, c := range []struct {
		id   uuid.UUID
		name string
	}{{w.catLum, "cqt lumber"}, {w.catHw, "cqt hardware"}} {
		slug := "cqt_" + c.id.String()[:8]
		must(`INSERT INTO product_categories (id, name, slug, path) VALUES ($1, $2, $3::text, replace($3::text, '-', '_')::ltree)`, c.id, c.name, slug)
	}
	w.stud, w.ply, w.nail = uuid.New(), uuid.New(), uuid.New()
	for _, p := range []struct {
		id   uuid.UUID
		cat  uuid.UUID
		name string
		base string
	}{{w.stud, w.catLum, "2x4 stud", "5.50"}, {w.ply, w.catLum, "plywood sheet", "41.2500"}, {w.nail, w.catHw, "box of nails", "12.00"}} {
		must(`INSERT INTO products (id, sku, description, uom_primary, base_price, category_id) VALUES ($1, $2, $3, 'EA', $4::numeric, $5)`,
			p.id, "CQT-"+p.id.String()[:8], p.name, p.base, p.cat)
	}
	w.costs = &fakeCosts{items: map[uuid.UUID]customerquote.CatalogItem{}, source: map[uuid.UUID]*customerquote.SourceQuote{}}
	for _, p := range []struct {
		id   uuid.UUID
		cat  uuid.UUID
		name string
		unit int64
	}{{w.stud, w.catLum, "2x4 stud", 55000}, {w.ply, w.catLum, "plywood sheet", 412500}, {w.nail, w.catHw, "box of nails", 120000}} {
		cat := p.cat
		w.costs.items[p.id] = customerquote.CatalogItem{ProductID: p.id, SKU: "CQT-" + p.id.String()[:8], Description: p.name, UOM: "EA",
			CategoryID: &cat, UnitCostTenThousandths: p.unit}
	}
	w.mail = &recMail{linkOK: true, email: true, sms: true}
	w.orders = &fakeOrders{w: w}
	logger := slog.New(slog.NewTextHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w.store = customerquote.NewStore(db)
	w.svc = customerquote.NewService(db, w.store, w.costs, logger).
		WithOrders(w.orders).WithNotifier(w.mail).WithPublicURL("https://quotes.example").
		WithClock(func() time.Time { w.mu.Lock(); defer w.mu.Unlock(); return w.now }).
		WithLimits(1000, 1000, 1000)
	t.Cleanup(func() {
		testutil.DeleteCustomerQuotes(t, db, ca, cb)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = ANY($1))`, []uuid.UUID{ca, cb})
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = ANY($1)`, []uuid.UUID{ca, cb})
		_, _ = db.Pool.Exec(ctx, `DELETE FROM projects WHERE customer_id = ANY($1)`, []uuid.UUID{ca, cb})
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = ANY($1)`, []uuid.UUID{ca, cb})
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = ANY($1)`, []uuid.UUID{w.stud, w.ply, w.nail})
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_categories WHERE id = ANY($1)`, []uuid.UUID{w.catLum, w.catHw})
	})
	return w
}

func (w *world) advance(d time.Duration) {
	w.mu.Lock()
	w.now = w.now.Add(d)
	w.mu.Unlock()
}

func qty(t *testing.T, s string) httpx.Quantity {
	t.Helper()
	q, err := httpx.ParseQuantity(s)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func sp(s string) *string { return &s }

// basicInput is a quote of two lumber lines and one hardware line, a labour
// line, a contractor only labour line, flat overhead and a quote default
// markup of 25 percent.
func (w *world) basicInput() *customerquote.QuoteInput {
	return &customerquote.QuoteInput{
		Title:          "Maple Street deck",
		ProjectID:      sp(w.projA.String()),
		JobSiteAddress: "12 Maple Street",
		EndCustomer:    customerquote.Contact{Name: "Pat Homeowner", Email: "pat@homeowner.example", Phone: "555 010 2020"},
		SiteContact:    customerquote.Contact{Name: "Sam Foreman", Phone: "555 010 3030"},
		Markup:         &customerquote.Markup{Kind: customerquote.KindMarkup, BPS: 2500},
		Tax:            &customerquote.Tax{Mode: customerquote.TaxSiteRate, RateBPS: 800},
		PaymentTerms:   sp("Net 15"),
		DepositBPS:     func() *int64 { v := int64(2500); return &v }(),
		Lines: []customerquote.LineInput{
			{ProductID: w.stud.String(), Quantity: qty(w.t, "100")},
			{ProductID: w.ply.String(), Quantity: qty(w.t, "10")},
			{ProductID: w.nail.String(), Quantity: qty(w.t, "4")},
		},
		Labour: []customerquote.LabourInput{
			{Description: "Framing crew", RateType: customerquote.RateHourly, RateCents: 6500, Hours: qty(w.t, "16")},
			{Description: "Own travel", RateType: customerquote.RateFlat, RateCents: 12000, ContractorOnly: true},
		},
		Overhead: []customerquote.OverheadInput{
			{Label: "Permit", AmountType: customerquote.AmountFlat, Value: 15000},
			{Label: "Own insurance share", AmountType: customerquote.AmountPercent, Value: 300, ContractorOnly: true},
		},
	}
}

func (w *world) createSent(in *customerquote.QuoteInput) (*customerquote.Quote, string) {
	w.t.Helper()
	ctx := context.Background()
	q, err := w.svc.Create(ctx, w.a, in)
	if err != nil {
		w.t.Fatalf("create: %v", err)
	}
	res, err := w.svc.Send(ctx, w.a, q.ID, "")
	if err != nil {
		w.t.Fatalf("send: %v", err)
	}
	return res.Quote, strings.TrimPrefix(res.Path, "/q/")
}

var anon = customerquote.Caller{Address: "203.0.113.9", UserAgent: "cq-test"}

type httpxTimestamp = httpx.Timestamp

func httpxTS(t time.Time) httpx.Timestamp { return httpx.TimestampOf(t) }
