// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Slice O2 (CP-13, ADR 0011 sections 5.4, 6.2 and 7): the snapshot route.
// The sheet is the engine's answer with rung 2 skipped (a promotion the live
// counter takes is not on the sheet; with no rule the walk-in row is the
// live price), an account's own rows are its contract and its level, its
// level's rows the fallback; a sale the bench signs under the issued
// snapshot posts through the sync; the route refuses queue_not_drained while
// the register holds what the server has not recorded; the room is the
// least of the credit room and the cap, less the register's open account
// exceptions; the ETag stands still until the content moves; three takes at
// once store one snapshot.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/app/orderwire"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// withEngine wires the real price engine, as serve wires it.
func withEngine(db *database.DB) func(*fixture) {
	return func(f *fixture) {
		f.service.WithPriceEngine(&orderwire.PriceEngineAdapter{Pricing: pricing.NewService(pricing.NewRepository(db))})
	}
}

// takeSnapshot is GET /pos/offline/snapshot as the register's cashier.
func takeSnapshot(f *fixture, register string, high int64, headers ...string) resp {
	h := append([]string{"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t)}, headers...)
	return f.do("GET", "/api/v1/pos/offline/snapshot?register_id="+register+"&high_seq="+strconv.FormatInt(high, 10), nil, h...)
}

// adopt makes the issued snapshot the bench's own, as the core keeps the copy.
func (b *offlineBench) adopt(t *testing.T, r resp) {
	t.Helper()
	var snap pos.OfflineSnapshot
	if err := json.Unmarshal([]byte(r.raw), &snap); err != nil {
		t.Fatalf("issued snapshot does not read as the sync's: %v", err)
	}
	b.snap, b.snapID = snap, uuid.MustParse(snap.SnapshotID)
}

func sheetRow(t *testing.T, snap map[string]any, product, key string) map[string]any {
	t.Helper()
	for _, r := range snap["price_sheet"].([]any) {
		m := r.(map[string]any)
		if m["product_id"] == product && m["price_key"] == key {
			return m
		}
	}
	return nil
}

// pricingSetup puts a promotion on the fixture's product, a contract and a
// level (Base less 10) on its customer, a second product with no contract,
// and the customer on the branch with a limit.
// lateProductCleanup removes a product the test made after the fixture's
// own cleanup has removed the sales that name it: an option registers its
// cleanup before the fixture registers its own, so it runs after.
func lateProductCleanup(id uuid.UUID) func(*fixture) {
	return func(f *fixture) {
		f.t.Cleanup(func() {
			for _, q := range []string{`DELETE FROM inventory WHERE product_id = $1`, `DELETE FROM product_units WHERE product_id = $1`,
				`DELETE FROM products WHERE id = $1`} {
				if _, err := f.db.Pool.Exec(context.Background(), q, id); err != nil {
					f.t.Logf("product cleanup: %v", err)
				}
			}
		})
	}
}

func pricingSetup(t *testing.T, f *fixture, p2 uuid.UUID) (level uuid.UUID) {
	t.Helper()
	level, profile := uuid.New(), uuid.New()
	t.Cleanup(func() {
		for _, q := range []struct {
			sql string
			arg any
		}{
			{`DELETE FROM pricing_rules WHERE product_id = $1`, f.productID},
			{`DELETE FROM customer_contracts WHERE customer_id = $1`, f.customerID},
			{`DELETE FROM customer_branches WHERE customer_id = $1`, f.customerID},
			{`UPDATE customers SET price_profile_id = NULL WHERE id = $1`, f.customerID},
			{`DELETE FROM price_profiles WHERE id = $1`, profile},
			{`DELETE FROM price_levels WHERE id = $1`, level},
		} {
			if _, err := f.db.Pool.Exec(context.Background(), q.sql, q.arg); err != nil {
				t.Logf("pricing cleanup: %v", err)
			}
		}
	})
	mustExec(t, f.db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost, upc, category)
		VALUES ($1, $2, 'cedar 1x6', 'PCS', 10.00, 6.00, '0123456789012', 'Boards')`, p2, "P2-"+p2.String()[:8])
	mustExec(t, f.db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 7, 0)`, p2, f.yardID)
	mustExec(t, f.db, `INSERT INTO price_levels (id, code, name, basis, adjust_percent, position) VALUES ($1, $2, 'Offline level', 'BASE', -10, 900)`,
		level, "OFL_"+strings.ToUpper(level.String()[:8]))
	mustExec(t, f.db, `INSERT INTO price_profiles (id, code, name, price_level_id) VALUES ($1, $2, 'Offline profile', $3)`,
		profile, "OFP_"+strings.ToUpper(profile.String()[:8]), level)
	mustExec(t, f.db, `UPDATE customers SET price_profile_id = $2, credit_limit = 1000.00 WHERE id = $1`, f.customerID, profile)
	mustExec(t, f.db, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, f.customerID, f.branchID)
	mustExec(t, f.db, `INSERT INTO customer_contracts (customer_id, product_id, contract_price, price_uom) VALUES ($1, $2, 5.00, 'PCS')`,
		f.customerID, f.productID)
	mustExec(t, f.db, `INSERT INTO pricing_rules (name, rule_type, product_id, fixed_price, price_uom) VALUES ('offline promo', 'PROMOTIONAL', $1, 4.00, 'PCS')`,
		f.productID)
	return level
}

func TestOfflineSnapshot_TheSheetIsTheEngineWithRungTwoSkipped(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	p2 := uuid.New()
	f := newFixture(t, db, withEngine(db), lateProductCleanup(p2))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	level := pricingSetup(t, f, p2)
	engine := &orderwire.PriceEngineAdapter{Pricing: pricing.NewService(pricing.NewRepository(db))}

	r := takeSnapshot(f, b.register, 0)
	if r.status != http.StatusOK {
		t.Fatalf("snapshot = %d %s", r.status, r.raw)
	}
	snap := r.body
	p1, cust, lvl := f.productID.String(), "customer:"+f.customerID.String(), "level:"+level.String()

	// the walk-in row skips the promotion the live counter takes
	live, err := engine.PriceFor(context.Background(), b.walkIn, f.productID, mustQ(t, "1"), "PCS", nil)
	if err != nil || live.Basis != "PROMOTIONAL" || live.Price != 40000 {
		t.Fatalf("live walk-in answer = %+v %v, want the promotion at 4.0000", live, err)
	}
	w := sheetRow(t, snap, p1, "walkin")
	if w == nil || w["price_basis"] == "PROMOTIONAL" || devNum(w, "unit_price_ten_thousandths") == 40000 {
		t.Fatalf("walk-in row = %v, want the promotion skipped", w)
	}
	// with no rule, the walk-in row is the live price
	mustExec(t, f.db, `UPDATE pricing_rules SET is_active = FALSE WHERE product_id = $1`, f.productID)
	live, err = engine.PriceFor(context.Background(), b.walkIn, f.productID, mustQ(t, "1"), "PCS", nil)
	if err != nil || devNum(w, "unit_price_ten_thousandths") != int64(live.Price) || w["price_basis"] != live.Basis ||
		w["price_source"] != strings.ToLower(live.Basis) || w["price_uom"] != live.PriceUOM {
		t.Errorf("walk-in row %v, live with no rule %+v", w, live)
	}
	// the account's own rows: its contract, else its level; its level's rows
	// for every product
	for _, c := range []struct {
		product, key, basis string
		price               int64
	}{
		{p1, cust, "CONTRACT", 50000}, {p1, lvl, "LEVEL", 49500},
		{p2.String(), cust, "LEVEL", 90000}, {p2.String(), lvl, "LEVEL", 90000}, {p2.String(), "walkin", "", 0},
	} {
		row := sheetRow(t, snap, c.product, c.key)
		if row == nil {
			t.Errorf("no %s row for %s", c.key, c.product)
			continue
		}
		if c.basis != "" && (row["price_basis"] != c.basis || devNum(row, "unit_price_ten_thousandths") != c.price ||
			row["uom_qty"] != "1" || row["price_uom_qty"] != "1") {
			t.Errorf("%s row for %s = %v, want %s at %d", c.key, c.product, row, c.basis, c.price)
		}
	}

	// the account, the level key, the products with the core's own fields
	var acct map[string]any
	for _, a := range snap["accounts"].([]any) {
		if a.(map[string]any)["customer_id"] == f.customerID.String() {
			acct = a.(map[string]any)
		}
	}
	if acct == nil || acct["price_key"] != cust || acct["level_key"] != lvl || devNum(acct, "credit_limit_cents") != 100000 ||
		acct["jobs"] == nil || acct["buyers"] == nil {
		t.Fatalf("cached account = %v", acct)
	}
	for _, p := range snap["products"].([]any) {
		m := p.(map[string]any)
		if m["id"] == p2.String() && (m["in_stock"] != "7" || m["barcode"] != "0123456789012" || m["category"] != "Boards" ||
			m["unit_set"] == nil) {
			t.Errorf("product = %v", m)
		}
	}
	if snap["till_session"].(map[string]any)["id"] != b.session.String() || devNum(snap, "device_epoch") != 1 {
		t.Errorf("snapshot session or epoch = %v %v", snap["till_session"], snap["device_epoch"])
	}

	// a sale the bench signs under the issued snapshot posts: the walk-in at
	// its row, the account at its contract and at its own level row
	b.adopt(t, r)
	raw1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "2")}})
	raw2, s2 := b.build(t, saleSpec{customer: &f.customerID, lines: []offlineLine{line(f.productID, "1"), line(p2, "1")}})
	res := b.sync(t, raw1, raw2)
	wantOutcome(t, res, s1, pos.OfflinePosted, "")
	wantOutcome(t, res, s2, pos.OfflinePosted, "")
	if s2.Lines[0].UnitPriceTenThousandths != 50000 || s2.Lines[1].UnitPriceTenThousandths != 90000 || s2.Lines[1].LevelFallback {
		t.Errorf("account lines = %+v", s2.Lines)
	}
	// the sync answer carries the confirmed mark back to the device
	var confirmed int64
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT confirmed_seq FROM pos_register_devices WHERE register_id = $1 AND epoch = 1`,
		b.register).Scan(&confirmed); err != nil || res.ConfirmedSeq != 2 || confirmed != 2 {
		t.Errorf("confirmed mark: answer %d, row %d (%v), want 2", res.ConfirmedSeq, confirmed, err)
	}
}

func TestOfflineSnapshot_TheLevelFallbackWhereTheAccountHasNoRow(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	p2 := uuid.New()
	f := newFixture(t, db, withEngine(db), lateProductCleanup(p2))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	level := pricingSetup(t, f, p2)
	r := takeSnapshot(f, b.register, 0)
	if r.status != http.StatusOK {
		t.Fatalf("snapshot = %d %s", r.status, r.raw)
	}
	b.adopt(t, r)
	// the sheet trimmed of the account's own row for p2 (the size limit's
	// case): the line prices at the level's row under the level's key
	kept := b.snap.PriceSheet[:0]
	for _, row := range b.snap.PriceSheet {
		if !(row.ProductID == p2.String() && row.PriceKey == "customer:"+f.customerID.String()) {
			kept = append(kept, row)
		}
	}
	b.snap.PriceSheet = kept
	raw, sale := b.build(t, saleSpec{customer: &f.customerID, lines: []offlineLine{line(p2, "1")}})
	if !sale.Lines[0].LevelFallback || sale.Lines[0].PriceKey != "level:"+level.String() {
		t.Fatalf("fallback line = %+v", sale.Lines[0])
	}
	// the stored snapshot carries the account's own row, so the sync refuses
	// a line priced at the level where the account's own row exists
	wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "price_unverified")
}

func TestOfflineSnapshot_Refusals(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	deviceCleanup(t, f, f.register)

	// no enrolment
	if r := takeSnapshot(f, f.register, 0); r.status != http.StatusConflict || errCode(r) != "device_not_enrolled" {
		t.Errorf("no enrolment = %d %s", r.status, r.raw)
	}
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	// policy off
	mustExec(t, f.db, `UPDATE pos_offline_policy SET enabled = FALSE WHERE branch_id = $1`, f.branchID)
	if r := takeSnapshot(f, b.register, 0); r.status != http.StatusConflict || errCode(r) != "money_never_queues" {
		t.Errorf("policy off = %d %s", r.status, r.raw)
	}
	mustExec(t, f.db, `UPDATE pos_offline_policy SET enabled = TRUE WHERE branch_id = $1`, f.branchID)
	// no open till
	mustExec(t, f.db, `UPDATE till_sessions SET status = 'CLOSED', closed_at = NOW() WHERE id = $1`, b.session)
	if r := takeSnapshot(f, b.register, 0); r.status != http.StatusConflict || errCode(r) != "till_not_open" {
		t.Errorf("no open till = %d %s", r.status, r.raw)
	}
	mustExec(t, f.db, `UPDATE till_sessions SET status = 'OPEN', closed_at = NULL WHERE id = $1`, b.session)
	// the query is strict
	for _, q := range []string{"register_id=" + b.register, "register_id=" + b.register + "&high_seq=-1",
		"register_id=" + b.register + "&high_seq=0&extra=1"} {
		if r := f.do("GET", "/api/v1/pos/offline/snapshot?"+q, nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusBadRequest {
			t.Errorf("query %q = %d, want 400", q, r.status)
		}
	}
	// a machine key is refused
	keyed := keyedServer(f, uuid.NewString())
	res, err := http.Get(keyed.URL + "/api/v1/pos/offline/snapshot?register_id=" + b.register + "&high_seq=0")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "never a machine key") {
		t.Errorf("machine key = %d %s", res.StatusCode, body)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_offline_snapshots WHERE register_id = $1 AND digest <> 'test'`, b.register); n != 0 {
		t.Errorf("a refused take stored %d snapshots", n)
	}
}

func TestOfflineSnapshot_AnotherBranchsRegisterIsRefused(t *testing.T) {
	w := newWallFixture(t)
	offlinePolicyOn(t, w.fixture)
	b := newOfflineBench(t, w.fixture)
	r := w.do("GET", "/api/v1/pos/offline/snapshot?register_id="+b.register+"&high_seq=0", nil, w.scoped...)
	if r.status != http.StatusForbidden {
		t.Errorf("branch B's cashier = %d %s, want 403", r.status, r.raw)
	}
	r = w.do("GET", "/api/v1/pos/offline/snapshot?register_id="+b.register+"&high_seq=0", nil, w.noHdr...)
	if r.status != http.StatusForbidden {
		t.Errorf("branch B's cashier naming no branch = %d %s, want 403", r.status, r.raw)
	}
}

func TestOfflineSnapshot_TheDrainRule(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	if r := takeSnapshot(f, b.register, 0); r.status != http.StatusOK {
		t.Fatalf("drained register = %d %s", r.status, r.raw)
	}
	// a sale queued and not sent: refused until it is recorded
	raw1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	if r := takeSnapshot(f, b.register, 1); r.status != http.StatusConflict || errCode(r) != "queue_not_drained" {
		t.Fatalf("one waiting = %d %s, want 409 queue_not_drained", r.status, r.raw)
	}
	wantOutcome(t, b.sync(t, raw1), s1, pos.OfflinePosted, "")
	if r := takeSnapshot(f, b.register, 1); r.status != http.StatusOK {
		t.Fatalf("after the sync = %d %s", r.status, r.raw)
	}
	// a held item (its predecessor missing) is recorded: answered
	raw2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	raw3, s3 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	wantOutcome(t, b.sync(t, raw3), s3, pos.OfflineHeld, "broken_chain")
	if r := takeSnapshot(f, b.register, 3); r.status != http.StatusOK {
		t.Errorf("held item recorded = %d %s, want 200", r.status, r.raw)
	}
	// an open bad copy of the epoch waits until the true sale supersedes it
	tampered := *s2
	tampered.Lines = append([]pos.SignedSaleLine{}, s2.Lines...)
	tampered.Lines[0].UnitPriceTenThousandths = 1
	badRaw, _ := json.Marshal(&tampered)
	b.sync(t, badRaw)
	if r := takeSnapshot(f, b.register, 3); r.status != http.StatusConflict || errCode(r) != "queue_not_drained" ||
		!strings.Contains(string(r.raw), "bad copy") {
		t.Fatalf("open bad copy = %d %s, want 409 queue_not_drained naming the bad copy", r.status, r.raw)
	}
	b.sync(t, raw2)
	if r := takeSnapshot(f, b.register, 3); r.status != http.StatusOK {
		t.Errorf("after the true sale = %d %s, want 200", r.status, r.raw)
	}
}

func TestOfflineSnapshot_ETagAndTheRoom(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	p2 := uuid.New()
	f := newFixture(t, db, withEngine(db), lateProductCleanup(p2))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	pricingSetup(t, f, p2)
	// the cap 500.00 is under the credit room 1000.00
	mustExec(t, f.db, `UPDATE pos_offline_policy SET account_cap_cents = 50000 WHERE branch_id = $1`, f.branchID)
	first := takeSnapshot(f, b.register, 0)
	if first.status != http.StatusOK {
		t.Fatalf("snapshot = %d %s", first.status, first.raw)
	}
	etag := first.header.Get("ETag")
	again := takeSnapshot(f, b.register, 0)
	if again.header.Get("ETag") != etag || again.body["snapshot_id"] != first.body["snapshot_id"] {
		t.Errorf("unchanged content moved: %s %v, then %s %v", etag, first.body["snapshot_id"], again.header.Get("ETag"), again.body["snapshot_id"])
	}
	if r := takeSnapshot(f, b.register, 0, "If-None-Match", etag); r.status != http.StatusNotModified {
		t.Errorf("If-None-Match = %d, want 304", r.status)
	}
	room := func(r resp) int64 {
		for _, a := range r.body["accounts"].([]any) {
			if m := a.(map[string]any); m["customer_id"] == f.customerID.String() {
				return devNum(m, "offline_room_cents")
			}
		}
		return -1
	}
	if got := room(first); got != 50000 {
		t.Errorf("room = %d, want the cap 50000", got)
	}
	// an open account exception of this register on the account comes off
	// the room at issue, once; another register's does not
	mustExec(t, f.db, `INSERT INTO pos_offline_exceptions (sale_id, register_id, branch_id, device_epoch, device_seq, business_date,
		customer_id, reason_code, account_cents, total_cents, status) VALUES ($1, $2, $3, 1, 90, CURRENT_DATE, $4, 'cap_exceeded', 12000, 12000, 'open')`,
		uuid.New(), b.register, f.branchID, f.customerID)
	moved := takeSnapshot(f, b.register, 0)
	if moved.header.Get("ETag") == etag || room(moved) != 38000 {
		t.Errorf("after an open exception: etag %s (was %s), room %d, want a new etag and 38000", moved.header.Get("ETag"), etag, room(moved))
	}
	// a limit under the cap governs; a customer with no limit has the cap
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 300.00 WHERE id = $1`, f.customerID)
	if got := room(takeSnapshot(f, b.register, 0)); got != 18000 {
		t.Errorf("room under a 300.00 limit = %d, want 18000", got)
	}
	mustExec(t, f.db, `UPDATE customers SET credit_limit = NULL WHERE id = $1`, f.customerID)
	if got := room(takeSnapshot(f, b.register, 0)); got != 38000 {
		t.Errorf("room with no limit = %d, want 38000", got)
	}
	// an account the counter would not charge is not cached
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 0 WHERE id = $1`, f.customerID)
	if got := room(takeSnapshot(f, b.register, 0)); got != -1 {
		t.Errorf("an account with no credit was cached (room %d)", got)
	}
}

// Three takes of one register's snapshot while a sync of the register holds
// lock 0a and records an account exception (pool size 4, three contenders
// and the holder): the takes wait for it, all three answer one stored
// snapshot, and its room counts the exception the sync recorded.
func TestOfflineSnapshot_ThreeTakesWaitForTheSyncInFlight(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	p2 := uuid.New()
	f := newFixture(t, db, withEngine(db), lateProductCleanup(p2))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	pricingSetup(t, f, p2)
	mustExec(t, f.db, `UPDATE pos_offline_policy SET account_cap_cents = 50000 WHERE branch_id = $1`, f.branchID)
	ctx := context.Background()
	for round := 0; round < 3; round++ {
		before := countOf(t, f.db, `SELECT count(*) FROM pos_offline_snapshots WHERE register_id = $1`, b.register)
		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT id FROM pos_register_devices WHERE register_id = $1 AND epoch = 1 FOR UPDATE`, b.register); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO pos_offline_exceptions (sale_id, register_id, branch_id, device_epoch, device_seq,
			business_date, customer_id, reason_code, account_cents, total_cents, status)
			VALUES ($1, $2, $3, 1, $4, CURRENT_DATE, $5, 'cap_exceeded', 1000, 1000, 'open')`,
			uuid.New(), b.register, f.branchID, 100+round, f.customerID); err != nil {
			t.Fatal(err)
		}
		rooms := make([]int64, 3)
		ids := make([]any, 3)
		statuses := make([]int, 3)
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodGet, f.srv.URL+"/api/v1/pos/offline/snapshot?register_id="+b.register+"&high_seq=0", nil)
				req.RequestURI = ""
				req.Header.Set("X-Test-Role", "cashier")
				req.Header.Set("X-Test-Sub", uuid.NewString())
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					return
				}
				defer res.Body.Close()
				var body map[string]any
				dec := json.NewDecoder(res.Body)
				dec.UseNumber()
				_ = dec.Decode(&body)
				statuses[i], ids[i] = res.StatusCode, body["snapshot_id"]
				rooms[i] = -1
				if accts, ok := body["accounts"].([]any); ok {
					for _, a := range accts {
						if m := a.(map[string]any); m["customer_id"] == f.customerID.String() {
							rooms[i] = devNum(m, "offline_room_cents")
						}
					}
				}
			}(i)
		}
		// the sync in flight records its item, then commits
		time.Sleep(300 * time.Millisecond)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		want := int64(50000 - 1000*(round+1))
		after := countOf(t, f.db, `SELECT count(*) FROM pos_offline_snapshots WHERE register_id = $1`, b.register)
		for i := 0; i < 3; i++ {
			if statuses[i] != 200 || rooms[i] != want || ids[i] != ids[0] {
				t.Fatalf("round %d take %d: status %d room %d id %v (first %v); want 200, room %d, one snapshot",
					round, i, statuses[i], rooms[i], ids[i], ids[0], want)
			}
		}
		if after != before+1 {
			t.Fatalf("round %d stored %d snapshots, want 1", round, after-before)
		}
	}
}

var updateSnapshotFixture = flag.Bool("update-snapshot-fixture", false,
	"rewrite testdata/offline-snapshot.json from the snapshot the route issues")

// jsonShape is a document's shape: every key at every level with its kind,
// array elements merged; a null fits any kind, an empty array any element.
func jsonShape(v any, path string, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		out[path] = "object"
		for k, e := range x {
			jsonShape(e, path+"."+k, out)
		}
	case []any:
		out[path] = "array"
		for _, e := range x {
			jsonShape(e, path+"[]", out)
		}
	case nil:
		if _, ok := out[path]; !ok {
			out[path] = "null"
		}
	case string:
		out[path] = "string"
	case float64:
		out[path] = "number"
	case bool:
		out[path] = "bool"
	}
}

// RULE (ADR 0011 5.4, the shared snapshot fixture, lead entry 692): the file
// the Rust core's test parses is a snapshot this route issued, and its shape
// is still the route's: the same keys at every level with the same kinds. A
// field the route adds, drops or retypes fails here until the fixture is
// issued again (go test -run TheSharedFixture -update-snapshot-fixture), and
// then the Rust test proves the core reads it with nothing lost.
func TestOfflineSnapshot_TheSharedFixtureIsWhatTheRouteIssues(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	p2 := uuid.New()
	f := newFixture(t, db, withEngine(db), lateProductCleanup(p2))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	pricingSetup(t, f, p2)
	// a job and a capped buyer, so every list of the cached account has a row
	mustExec(t, f.db, `INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, 'Front Deck', 'Active')`, uuid.New(), f.customerID)
	f.addContact("Yard", "Buyer", true, ptr64(50000), true)
	r := takeSnapshot(f, b.register, 0)
	if r.status != http.StatusOK {
		t.Fatalf("snapshot = %d %s", r.status, r.raw)
	}
	const path = "testdata/offline-snapshot.json"
	if *updateSnapshotFixture {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, r.raw, "", "  "); err != nil {
			t.Fatal(err)
		}
		pretty.WriteByte('\n')
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the shared snapshot fixture: %v (issue it with -update-snapshot-fixture)", err)
	}
	var fixture, issued any
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(r.raw, &issued); err != nil {
		t.Fatal(err)
	}
	want, got := map[string]string{}, map[string]string{}
	jsonShape(issued, "$", want)
	jsonShape(fixture, "$", got)
	for k, kind := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("the fixture lacks %s (%s) the route issues", k, kind)
		} else if g != kind && g != "null" && kind != "null" {
			t.Errorf("%s is %s in the fixture, %s as issued", k, g, kind)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("the fixture holds %s the route no longer issues", k)
		}
	}
	// the sync reads the fixture as the snapshot it verifies sales against
	var snap pos.OfflineSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil || snap.SnapshotID == "" || len(snap.PriceSheet) == 0 ||
		len(snap.Products) == 0 || len(snap.Accounts) == 0 {
		t.Fatalf("the fixture as the sync reads it: %+v %v", snap, err)
	}
}
