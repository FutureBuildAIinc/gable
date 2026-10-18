// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The counter's tiles read: the catalogue ordered by what the caller's yard
// sells, its category chips, the picture setting and the caller's reach.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// tileWorld is two yards with a small catalogue of its own: a category per
// product group, sales at each yard, and one picture path on some products.
type tileWorld struct {
	*twoYards
	t        *testing.T
	tag      string
	catA     string // the category the yard A sells most of
	catB     string
	cats     []string
	products map[string]uuid.UUID // by sku
}

func (w *tileWorld) exec(sql string, args ...any) {
	w.t.Helper()
	db := testutil.RequireDB(w.t)
	if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("seed: %v", err)
	}
}

func (w *tileWorld) sku(n string) string { return "TL" + w.tag + n }

func (w *tileWorld) product(n, category, image string, cents int) uuid.UUID {
	w.t.Helper()
	id := uuid.New()
	var img any
	if image != "" {
		img = image
	}
	w.exec(`INSERT INTO products (id, sku, description, uom_primary, base_price, category, image_url)
		VALUES ($1, $2, $3, 'PCS', $4::numeric / 100, $5, $6)`, id, w.sku(n), "Tile "+w.tag+" "+n, cents, category, img)
	w.products[w.sku(n)] = id
	return id
}

// counterSale records one completed counter sale of one product at a yard.
func (w *tileWorld) counterSale(branch, product uuid.UUID, qty int) {
	w.t.Helper()
	tx := uuid.New()
	w.exec(`INSERT INTO pos_transactions (id, register_id, cashier_id, status, branch_id, currency, completed_at)
		VALUES ($1, 'TILEREG', $2, 'COMPLETED', $3, 'USD', NOW())`, tx, uuid.New(), branch)
	w.exec(`INSERT INTO pos_line_items (transaction_id, product_id, description, quantity, uom, price_uom, uom_qty,
			price_uom_qty, stock_uom, stock_quantity, unit_price, line_total, line_type, price_source)
		VALUES ($1, $2, 'sold', $3, 'PCS', 'PCS', 1, 1, 'PCS', $3, 1, $3, 'PRODUCT', 'PRICE_LIST')`, tx, product, qty)
}

// invoiceSale records one order invoice of one product at a yard.
func (w *tileWorld) invoiceSale(branch, customer, product uuid.UUID, qty int, status, origin string) {
	w.t.Helper()
	inv := uuid.New()
	voided := ""
	if status == "VOID" {
		voided = ", voided_at"
	}
	w.exec(`INSERT INTO invoices (id, customer_id, status, branch_id, total_amount, amount_open, origin`+voided+`)
		VALUES ($1, $2, $3, $4, 0, 0, $5`+map[bool]string{true: ", NOW()", false: ""}[voided != ""]+`)`, inv, customer, status, branch, origin)
	w.exec(`INSERT INTO invoice_lines (invoice_id, product_id, description, quantity, price_each, uom, price_uom,
			uom_qty, price_uom_qty, stock_uom, stock_quantity, unit_price, line_total, line_type)
		VALUES ($1, $2, 'billed', $3, 1, 'PCS', 'PCS', 1, 1, 'PCS', $3, 1, $3, 'PRODUCT')`, inv, product, qty)
}

func (w *tileWorld) setting(value string) {
	w.t.Helper()
	w.exec(`INSERT INTO system_settings (key, value) VALUES ('catalog.product_images', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, value)
}

func newTileWorld(t *testing.T) *tileWorld {
	t.Helper()
	testutil.LockProductImageSetting(t)
	db := testutil.RequireDB(t)
	w := &tileWorld{twoYards: seedTwoYards(t), t: t, tag: uuid.NewString()[:6], products: map[string]uuid.UUID{}}
	// nine categories ranked by the units yard A sold, so the chip list must
	// stop at eight; the ninth sold least of them.
	for i := 0; i < 9; i++ {
		w.cats = append(w.cats, fmt.Sprintf("Cat%s%d", w.tag, i))
	}
	w.catA, w.catB = w.cats[0], w.cats[8]
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE register_id = 'TILEREG' AND branch_id IN ($1, $2)`, w.a, w.b)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM invoice_lines WHERE invoice_id IN (SELECT id FROM invoices WHERE branch_id IN ($1, $2))`, w.a, w.b)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM invoices WHERE branch_id IN ($1, $2)`, w.a, w.b)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id IN (SELECT id FROM products WHERE sku LIKE $1)`, "TL"+w.tag+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE sku LIKE $1`, "TL"+w.tag+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key = 'catalog.product_images'`)
	})
	// one product per category; category i sold (9 - i) * 10 units at yard A
	for i, c := range w.cats {
		id := w.product(fmt.Sprintf("C%d", i), c, "", 1000+i)
		w.counterSale(w.a, id, (9-i)*10)
	}
	return w
}

func skus(items []pos.CounterTile) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.SKU)
	}
	return out
}

func indexOf(items []pos.CounterTile, sku string) int {
	for i, it := range items {
		if it.SKU == sku {
			return i
		}
	}
	return -1
}

// RULE: with no search the tiles are the caller's yard's top sellers by
// quantity (counter sales and order invoices), and another yard's sales do
// not count.
func TestTilesOrderByTheYardsTopSellers(t *testing.T) {
	w := newTileWorld(t)
	hot := w.product("HOT", w.catA, "", 500)
	warm := w.product("WARM", w.catA, "", 500)
	foreign := w.product("FOREIGN", w.catA, "", 500)
	w.counterSale(w.a, hot, 300)
	w.invoiceSale(w.a, w.custA, warm, 200, "UNPAID", "ORDER")
	w.counterSale(w.b, foreign, 100000)

	got, err := w.repo.CounterTiles(w.header(w.a), pos.TileQuery{Limit: 48})
	if err != nil {
		t.Fatal(err)
	}
	h, m, f := indexOf(got.Items, w.sku("HOT")), indexOf(got.Items, w.sku("WARM")), indexOf(got.Items, w.sku("FOREIGN"))
	if h != 0 || m != 1 {
		t.Fatalf("top sellers at yard A = %v, want HOT then WARM first", skus(got.Items))
	}
	if f != -1 && f < m {
		t.Errorf("another yard's sales counted: FOREIGN at %d, before WARM at %d", f, m)
	}
	// the other yard sees its own history
	gotB, err := w.repo.CounterTiles(w.header(w.b), pos.TileQuery{Limit: 48})
	if err != nil {
		t.Fatal(err)
	}
	if indexOf(gotB.Items, w.sku("FOREIGN")) != 0 {
		t.Errorf("yard B top sellers = %v, want FOREIGN first", skus(gotB.Items))
	}
	// the wire never carries a null array
	if got.Categories == nil || got.Items == nil {
		t.Errorf("categories or items nil")
	}
}

// RULE: a voided invoice and a counter sale's own invoice (origin POS) add
// nothing: the first was never sold, the second is the counter line again.
func TestTilesCountEachSaleOnce(t *testing.T) {
	w := newTileWorld(t)
	voided := w.product("VOIDED", w.catA, "", 500)
	twice := w.product("TWICE", w.catA, "", 500)
	once := w.product("ONCE", w.catA, "", 500)
	w.invoiceSale(w.a, w.custA, voided, 900, "VOID", "ORDER")
	w.counterSale(w.a, twice, 100)
	w.invoiceSale(w.a, w.custA, twice, 100, "PAID", "POS")
	w.invoiceSale(w.a, w.custA, once, 150, "PAID", "ORDER")

	got, err := w.repo.CounterTiles(w.header(w.a), pos.TileQuery{Limit: 48})
	if err != nil {
		t.Fatal(err)
	}
	// ONCE (150) outsells TWICE (100, not 200); VOIDED sold nothing
	if o, tw := indexOf(got.Items, w.sku("ONCE")), indexOf(got.Items, w.sku("TWICE")); o != 0 || tw != 1 {
		t.Errorf("order = %v, want ONCE then TWICE first", skus(got.Items))
	}
	if v := indexOf(got.Items, w.sku("VOIDED")); v != -1 && v < 2 {
		t.Errorf("a voided invoice counted: VOIDED at %d", v)
	}
}

// RULE: a category narrows to its products; a chip is an exact name.
func TestTilesCategoryChip(t *testing.T) {
	w := newTileWorld(t)
	w.product("EXTRA", w.catA, "", 500)
	got, err := w.repo.CounterTiles(w.header(w.a), pos.TileQuery{Category: w.catA, Limit: 48})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("category %s = %v, want its two products", w.catA, skus(got.Items))
	}
	for _, it := range got.Items {
		if it.Category != w.catA {
			t.Errorf("%s is in %q, want %q", it.SKU, it.Category, w.catA)
		}
	}
	if got.Items[0].SKU != w.sku("C0") {
		t.Errorf("sold product first: %v", skus(got.Items))
	}
	none, err := w.repo.CounterTiles(w.header(w.a), pos.TileQuery{Category: "No such category", Limit: 48})
	if err != nil {
		t.Fatal(err)
	}
	if len(none.Items) != 0 || none.Items == nil {
		t.Errorf("unknown category = %v, want an empty array", none.Items)
	}
}

// RULE: a search matches SKU or description, an exact SKU first, then top
// sellers.
func TestTilesSearchPutsAnExactSKUFirst(t *testing.T) {
	w := newTileWorld(t)
	best := w.product("BESTSELL", w.catA, "", 500)
	w.counterSale(w.a, best, 5000)
	w.product("MATCH", w.catA, "", 500)
	// every product of the world carries the tag in its description
	got, err := w.repo.CounterTiles(w.header(w.a), pos.TileQuery{Q: "tile " + w.tag, Limit: 48})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) < 2 || got.Items[0].SKU != w.sku("BESTSELL") {
		t.Fatalf("description search = %v, want the top seller first", skus(got.Items))
	}
	exact, err := w.repo.CounterTiles(w.header(w.a), pos.TileQuery{Q: w.sku("MATCH"), Limit: 48})
	if err != nil {
		t.Fatal(err)
	}
	if len(exact.Items) == 0 || exact.Items[0].SKU != w.sku("MATCH") {
		t.Fatalf("exact SKU search = %v, want MATCH first", skus(exact.Items))
	}
	// the exact SKU outranks a top seller whose SKU merely contains it
	w.product("MATCHX", w.catA, "", 500)
	w.counterSale(w.a, w.products[w.sku("MATCHX")], 9000)
	again, err := w.repo.CounterTiles(w.header(w.a), pos.TileQuery{Q: w.sku("MATCH"), Limit: 48})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Items) < 2 || again.Items[0].SKU != w.sku("MATCH") || again.Items[1].SKU != w.sku("MATCHX") {
		t.Errorf("exact SKU first = %v, want MATCH then MATCHX", skus(again.Items))
	}
}

// RULE: the limit bounds the items.
func TestTilesLimit(t *testing.T) {
	w := newTileWorld(t)
	got, err := w.repo.CounterTiles(w.header(w.a), pos.TileQuery{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 3 {
		t.Errorf("limit 3 gave %d items", len(got.Items))
	}
}

// RULE: the chips are at most eight categories, the yard's by units sold.
func TestTilesCategoriesAreTheTopEightByUnitsSold(t *testing.T) {
	w := newTileWorld(t)
	got, err := w.repo.CounterTiles(w.header(w.a), pos.TileQuery{Limit: 24})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Categories) != 8 {
		t.Fatalf("categories = %+v, want 8", got.Categories)
	}
	for i := 0; i < 8; i++ {
		if got.Categories[i].Name != w.cats[i] || got.Categories[i].Count != 1 {
			t.Errorf("category %d = %+v, want %s with one product", i, got.Categories[i], w.cats[i])
		}
	}
	for _, c := range got.Categories {
		if c.Name == w.catB {
			t.Errorf("the least sold category %s made the chips", c.Name)
		}
	}
}

// RULE: the picture setting off (or absent) gives no picture and images
// false; on gives each product's path and images true.
func TestTilesPictureSetting(t *testing.T) {
	w := newTileWorld(t)
	w.product("PIC", w.catA, "/catalog-illustrations/spf_framing_lumber.svg", 500)
	w.product("BARE", w.catA, "", 500)
	pic := func(label, wantImages string) pos.TileSet {
		t.Helper()
		got, err := w.repo.CounterTiles(w.header(w.a), pos.TileQuery{Category: w.catA, Limit: 48})
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if fmt.Sprint(got.Images) != wantImages {
			t.Errorf("%s: images = %v, want %s", label, got.Images, wantImages)
		}
		return got
	}
	byUrl := func(s pos.TileSet) map[string]*string {
		m := map[string]*string{}
		for _, it := range s.Items {
			m[it.SKU] = it.ImageURL
		}
		return m
	}
	absent := byUrl(pic("absent", "false"))
	if absent[w.sku("PIC")] != nil {
		t.Errorf("setting absent: picture %q must be null", *absent[w.sku("PIC")])
	}
	w.setting("off")
	if m := byUrl(pic("off", "false")); m[w.sku("PIC")] != nil {
		t.Errorf("setting off: picture must be null")
	}
	w.setting("on")
	on := byUrl(pic("on", "true"))
	if p := on[w.sku("PIC")]; p == nil || *p != "/catalog-illustrations/spf_framing_lumber.svg" {
		t.Errorf("setting on: PIC picture = %v", p)
	}
	if on[w.sku("BARE")] != nil {
		t.Errorf("a product with no picture stays null")
	}
}

// RULE: price and stock mean what the search answers: the list price in cents
// and the yard's on hand as a decimal string, none from another yard.
func TestTilesPriceAndStockAreTheSearchsFigures(t *testing.T) {
	w := newTileWorld(t)
	id := w.product("STOCKED", w.catA, "", 1234)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	locA, locB := uuid.New(), uuid.New()
	w.exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`, locA, "TA"+w.tag, w.a)
	w.exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`, locB, "TB"+w.tag, w.b)
	w.exec(`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 40, 5)`, id, locA)
	w.exec(`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 900, 0)`, id, locB)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE location_id IN ($1, $2)`, locA, locB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, locA, locB)
	})
	got, err := w.repo.CounterTiles(w.header(w.a), pos.TileQuery{Q: w.sku("STOCKED"), Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %v", skus(got.Items))
	}
	it := got.Items[0]
	if int64(it.UnitPriceCents) != 1234 || it.InStock.WireString() != "35" || it.UOM != "PCS" {
		t.Errorf("tile = %+v, want 1234 cents, 35 on hand, PCS", it)
	}
	search, err := w.repo.SearchProducts(w.header(w.a), w.sku("STOCKED"), 5)
	if err != nil || len(search) != 1 || search[0].InStock != it.InStock || search[0].UnitPriceCents != it.UnitPriceCents {
		t.Errorf("search %+v (%v) disagrees with tile %+v", search, err, it)
	}
}

// RULE: the yard is the caller's reach: a bound key keeps its pin whatever
// the header says, a user with no grants sees no yard's history, and an
// administrator with no header sees every yard's.
func TestTilesHoldTheCallersReach(t *testing.T) {
	w := newTileWorld(t)
	onlyB := w.product("ONLYB", w.catA, "", 500)
	w.counterSale(w.b, onlyB, 100000)

	first := func(label string, ctx context.Context) string {
		t.Helper()
		got, err := w.repo.CounterTiles(ctx, pos.TileQuery{Limit: 48})
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if len(got.Items) == 0 {
			return ""
		}
		return got.Items[0].SKU
	}
	if s := first("header B", w.header(w.b)); s != w.sku("ONLYB") {
		t.Errorf("header B first = %s", s)
	}
	if s := first("bound key pinned A", w.bound(w.a)); s == w.sku("ONLYB") {
		t.Errorf("a key pinned to yard A saw yard B's sales")
	}
	if s := first("bound key pinned B", w.bound(w.b)); s != w.sku("ONLYB") {
		t.Errorf("a key pinned to yard B first = %s", s)
	}
	// the user is granted yard A only: with no header they read A
	granted := branchctx.With(context.Background(), &branchctx.Context{UserSub: w.sub})
	if s := first("grants of A", granted); s == w.sku("ONLYB") {
		t.Errorf("a user granted only yard A saw yard B's sales")
	}
	// no reach: a user with no grant at all counts no history, so a heavy
	// seller at yard A is not first for them (it is for the yard's own caller)
	late := w.product("ZLATE", w.catA, "", 500)
	w.counterSale(w.a, late, 700000)
	if s := first("header A", w.header(w.a)); s != w.sku("ZLATE") {
		t.Errorf("header A first = %s, want ZLATE", s)
	}
	stranger := branchctx.With(context.Background(), &branchctx.Context{UserSub: "nobody-" + uuid.NewString()})
	if s := first("no reach", stranger); s == w.sku("ZLATE") || s == w.sku("ONLYB") {
		t.Errorf("a caller with no reach saw top sellers: %s", s)
	}
}

// RULE: any parameter outside the contract is a 400 (no register, no offset),
// a q under two characters, a limit outside 1 to 48 or not a number.
func TestTilesRouteRefusesBadParameters(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	get := func(query string) resp {
		return f.do("GET", "/api/v1/pos/tiles"+query, nil, cashierHeaders, "cashier", "X-Test-Sub", mustUUID(t))
	}
	for _, q := range []string{"?offset=1", "?register_id=x", "?q=a", "?limit=0", "?limit=49", "?limit=abc", "?limit=-1"} {
		if r := get(q); r.status != http.StatusBadRequest {
			t.Errorf("GET tiles%s = %d, want 400: %s", q, r.status, r.raw)
		}
	}
	ok := get("?q=" + url.QueryEscape(f.sku) + "&limit=5")
	if ok.status != http.StatusOK {
		t.Fatalf("GET tiles q = %d: %s", ok.status, ok.raw)
	}
	for _, key := range []string{"categories", "items", "images"} {
		if _, has := ok.body[key]; !has {
			t.Errorf("answer has no %q: %s", key, ok.raw)
		}
	}
	if items, _ := ok.body["items"].([]any); len(items) != 1 {
		t.Errorf("items = %v, want the fixture's product", ok.body["items"])
	}
}
