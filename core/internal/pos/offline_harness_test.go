// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The offline sync's test bench (ADR 0011 section 7): a register enrolled
// with an Ed25519 key, a snapshot of the fixture's own branch stored as O2's
// route will store it, and a stand in for the device's Rust core that builds
// each sale the way the core does (the bound price row, the extension, the
// tax, the tender sums, the sequence and the chain link) and signs it over
// the same canonical bytes. The sync runs through the service, as the route
// still answers 409 money_never_queues until slice C2 moves it.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/units"
	"github.com/google/uuid"
)

// offlineBench is one enrolled register of the fixture's branch.
type offlineBench struct {
	f          *fixture
	register   string
	epoch      int64
	priv       ed25519.PrivateKey
	deviceID   uuid.UUID
	session    uuid.UUID
	cashier    uuid.UUID
	snap       pos.OfflineSnapshot
	snapID     uuid.UUID
	seq        int64
	head       string
	confirmed  int64
	queueCount int64
	queueTotal int64
	walkIn     uuid.UUID
}

// offlinePolicyOn opens offline selling at the fixture's branch, and makes
// the cleanup take every offline row the branch's registers wrote. It runs
// before the fixture's own cleanup (t.Cleanup is last in, first out).
func offlinePolicyOn(t *testing.T, f *fixture) {
	t.Helper()
	mustExec(t, f.db, `INSERT INTO pos_offline_policy (branch_id, enabled, tenders, sale_cap_cents, check_cap_cents,
		account_cap_cents, queue_total_cap_cents, max_queued, updated_by)
		VALUES ($1, TRUE, ARRAY['CASH','CHECK','ACCOUNT'], 10000000, 10000000, 10000000, 100000000, 1000, 'test')
		ON CONFLICT (branch_id) DO UPDATE SET enabled = TRUE`, f.branchID)
	t.Cleanup(func() {
		ctx := context.Background()
		regs := `(SELECT id FROM pos_registers WHERE branch_id = $1)`
		for _, q := range []string{
			`DELETE FROM events_outbox WHERE entity_type = 'pos_offline_exception' AND entity_id IN (SELECT id FROM pos_offline_exceptions WHERE branch_id = $1)`,
			`DELETE FROM audit_log WHERE entity_type = 'pos_offline_exception' AND entity_id IN (SELECT id FROM pos_offline_exceptions WHERE branch_id = $1)`,
			`DELETE FROM pos_offline_exceptions WHERE branch_id = $1`,
			`UPDATE pos_transactions SET snapshot_id = NULL, replaces_sale_id = NULL, unknown_cart_id = NULL WHERE register_id IN ` + regs,
			`DELETE FROM pos_offline_snapshots WHERE branch_id = $1`,
			`DELETE FROM pos_register_devices WHERE branch_id = $1`,
			`DELETE FROM pos_offline_policy WHERE branch_id = $1`,
		} {
			if _, err := f.db.Pool.Exec(ctx, q, f.branchID); err != nil {
				t.Logf("offline cleanup: %v", err)
			}
		}
	})
}

// newOfflineBench enrols a register (the fixture's own unless one is named),
// opens its till session an hour ago, and issues it a snapshot now.
func newOfflineBench(t *testing.T, f *fixture, register ...string) *offlineBench {
	t.Helper()
	b := &offlineBench{f: f, register: f.register, epoch: 1, head: "GENESIS", cashier: uuid.New()}
	if len(register) > 0 {
		b.register = register[0]
		mustExec(t, f.db, `INSERT INTO pos_registers (id, location_id, name, branch_id) VALUES ($1, $2, 'Offline test', $3)`,
			b.register, f.yardID, f.branchID)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b.priv = priv
	b.deviceID = uuid.New()
	mustExec(t, f.db, `INSERT INTO pos_register_devices (id, register_id, branch_id, epoch, public_key, enrolled_by, chain_head)
		VALUES ($1, $2, $3, 1, $4, 'test manager', 'GENESIS')`, b.deviceID, b.register, f.branchID, hex.EncodeToString(pub))
	if err := f.db.Pool.QueryRow(context.Background(), `INSERT INTO till_sessions (register_id, branch_id, cashier_id, status,
		opening_float, opened_at) VALUES ($1, $2, $3, 'OPEN', 0, NOW() - interval '1 hour') RETURNING id`,
		b.register, f.branchID, b.cashier).Scan(&b.session); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT id FROM customers WHERE account_number = 'WALK-IN'`).Scan(&b.walkIn); err != nil {
		t.Fatal(err)
	}
	b.snap = b.baseSnapshot(t)
	b.issue(t)
	return b
}

// baseSnapshot is the fixture's branch as a snapshot: the product at 5.50
// walk in (5.00 under the customer's own key, 5.20 at its level), the branch
// rate, the fixture's customer cached with room, generous caps.
func (b *offlineBench) baseSnapshot(t *testing.T) pos.OfflineSnapshot {
	t.Helper()
	f := b.f
	var businessDate string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT (opened_at AT TIME ZONE 'America/New_York')::date::text
		FROM till_sessions WHERE id = $1`, b.session).Scan(&businessDate); err != nil {
		t.Fatal(err)
	}
	level := "level:offline-test"
	return pos.OfflineSnapshot{
		RegisterID: b.register, BranchID: f.branchID.String(), DeviceEpoch: b.epoch,
		TillSession: pos.OfflineSnapshotSession{ID: b.session.String(), CashierID: b.cashier.String(), BusinessDate: businessDate},
		Policy: pos.OfflineSnapshotPolicy{Enabled: true, MaxSaleCents: 10000000, MaxCheckCents: 10000000,
			AccountCapCents: 10000000, MaxQueueCount: 1000, MaxQueueTotalCents: 100000000,
			AllowedTenders: []string{"cash", "check", "account"}, CashRoundingMode: "OFF"},
		Tax:            pos.OfflineSnapshotTax{BranchRate: "0.088750"},
		WalkInCustomer: pos.OfflineSnapshotWalkIn{ID: b.walkIn.String(), Currency: "USD"},
		Products: []pos.OfflineSnapshotProduct{{ID: f.productID.String(), SKU: f.sku, Description: "2x4x8 SPF", Taxable: true,
			StockUOM: "PCS", SaleUOM: "PCS", PriceUOM: "PCS",
			UnitSet: []pos.OfflineSnapshotUnitRow{{UOM: "PCS", UnitQty: "1", StockQty: "1", Sell: true, Price: true},
				{UOM: "BDL", UnitQty: "1", StockQty: "24", Sell: true, Price: false}}},
			{ID: f.kitID.String(), SKU: "KIT", Description: "bundle kit", Taxable: true, IsKit: true, StockUOM: "EA",
				SaleUOM: "EA", PriceUOM: "EA", UnitSet: []pos.OfflineSnapshotUnitRow{{UOM: "EA", UnitQty: "1", StockQty: "1", Sell: true, Price: true}}}},
		PriceSheet: []pos.OfflineSnapshotPrice{
			{ProductID: f.productID.String(), PriceKey: "walkin", PriceUOM: "PCS", UOMQty: "1", PriceUOMQty: "1", UnitPriceTenThousandths: 55000, PriceSource: "list"},
			{ProductID: f.productID.String(), PriceKey: "customer:" + f.customerID.String(), PriceUOM: "PCS", UOMQty: "1", PriceUOMQty: "1", UnitPriceTenThousandths: 50000, PriceSource: "contract"},
			{ProductID: f.productID.String(), PriceKey: level, PriceUOM: "PCS", UOMQty: "1", PriceUOMQty: "1", UnitPriceTenThousandths: 52000, PriceSource: "level"},
			{ProductID: f.kitID.String(), PriceKey: "walkin", PriceUOM: "EA", UOMQty: "1", PriceUOMQty: "1", UnitPriceTenThousandths: 110000, PriceSource: "list"},
		},
		Accounts: []pos.OfflineSnapshotAccount{{CustomerID: f.customerID.String(), AccountNumber: "OFF-1", Name: f.customerName,
			Currency: "USD", CreditLimitCents: 10000000, OfflineRoomCents: 10000000,
			PriceKey: "customer:" + f.customerID.String(), LevelKey: &level}},
	}
}

// issue stores the bench's snapshot as a new snapshot row, issued now.
func (b *offlineBench) issue(t *testing.T) {
	t.Helper()
	b.snapID = uuid.New()
	b.snap.SnapshotID = b.snapID.String()
	b.snap.IssuedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(b.snap)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, b.f.db, `INSERT INTO pos_offline_snapshots (id, register_id, branch_id, device_epoch, business_date, content, digest)
		VALUES ($1, $2, $3, $4, $5::date, $6::jsonb, 'test')`, b.snapID, b.register, b.f.branchID, b.epoch,
		b.snap.TillSession.BusinessDate, string(raw))
}

// offlineLine is one line the clerk rings: a product, a quantity, a unit.
type offlineLine struct {
	product uuid.UUID
	qty     string
	uom     string
}

// saleSpec is what the clerk rang; the bench derives every figure.
type saleSpec struct {
	customer *uuid.UUID
	lines    []offlineLine
	// tenders by method; a zero cash amount means "exactly the total"
	tenders []pos.SignedSaleTender
	po      *string
}

// build makes the next sale on the register the way the Rust core does and
// signs it. edit, when given, changes the sale before it is signed (a device
// that lies), so the signature still verifies.
func (b *offlineBench) build(t *testing.T, spec saleSpec, edit ...func(*pos.SignedOfflineSale)) (json.RawMessage, *pos.SignedOfflineSale) {
	t.Helper()
	snap := b.snap
	var acct *pos.OfflineSnapshotAccount
	if spec.customer != nil {
		for i := range snap.Accounts {
			if snap.Accounts[i].CustomerID == spec.customer.String() {
				acct = &snap.Accounts[i]
			}
		}
	}
	rate := snap.Tax.BranchRate
	exempt := acct != nil && acct.TaxExempt
	if exempt {
		rate = "0.000000"
	}
	scaled, _, err := salesdoc.ParseTaxRate(rate)
	if err != nil {
		t.Fatal(err)
	}
	b.seq++
	sale := &pos.SignedOfflineSale{ClientID: uuid.NewString(), RegisterID: b.register, DeviceEpoch: b.epoch,
		DeviceSeq: b.seq, PrevDigest: b.head, OfflineRef: fmt.Sprintf("%s-%d-%06d", b.register, b.epoch, b.seq),
		SnapshotID: b.snapID.String(), TillSessionID: b.session.String(), CashierID: b.cashier.String(),
		BusinessDate: snap.TillSession.BusinessDate, SoldAt: time.Now().UTC().Format(time.RFC3339Nano), Currency: "USD",
		TaxRate: rate, TaxExempt: exempt, ConfirmedMark: b.confirmed, CustomerPO: spec.po,
		Lines: []pos.SignedSaleLine{}, Tenders: []pos.SignedSaleTender{}}
	if spec.customer != nil {
		c := spec.customer.String()
		sale.CustomerID = &c
	}
	var taxable int64
	for i, l := range spec.lines {
		var prod *pos.OfflineSnapshotProduct
		for k := range snap.Products {
			if snap.Products[k].ID == l.product.String() {
				prod = &snap.Products[k]
			}
		}
		if prod == nil {
			t.Fatalf("line %d names a product the bench snapshot lacks", i)
		}
		uom := l.uom
		if uom == "" {
			uom = prod.SaleUOM
		}
		key, fallback := "walkin", false
		if acct != nil {
			key = acct.PriceKey
		}
		row := findRow(snap, prod.ID, key)
		if row == nil && acct != nil && acct.LevelKey != nil {
			row, fallback = findRow(snap, prod.ID, *acct.LevelKey), true
		}
		if row == nil {
			t.Fatalf("no sheet row for line %d", i)
		}
		var uRow pos.OfflineSnapshotUnitRow
		for _, r := range prod.UnitSet {
			if r.UOM == uom {
				uRow = r
			}
		}
		q := mustQ(t, l.qty)
		pair := units.One()
		if uom != row.PriceUOM {
			var pRow pos.OfflineSnapshotUnitRow
			for _, r := range prod.UnitSet {
				if r.UOM == row.PriceUOM {
					pRow = r
				}
			}
			var err error
			pair, err = units.ResolveLinePair(units.Pair{A: mustQ(t, uRow.UnitQty), B: mustQ(t, uRow.StockQty)},
				units.Pair{A: mustQ(t, pRow.UnitQty), B: mustQ(t, pRow.StockQty)})
			if err != nil {
				t.Fatal(err)
			}
		}
		uq, pq := pair.A, pair.B
		price := httpx.Price(row.UnitPriceTenThousandths)
		probe := salesdoc.Line{LineType: salesdoc.LineProduct, Quantity: &q, UOM: &uom, PriceUOM: &row.PriceUOM,
			UOMQty: &uq, PriceUOMQty: &pq, UnitPrice: &price}
		if err := salesdoc.ExtendLine(&probe); err != nil {
			t.Fatal(err)
		}
		stock := q * mustQ(t, uRow.StockQty) / mustQ(t, uRow.UnitQty)
		line := pos.SignedSaleLine{ID: uuid.NewString(), Position: i, ProductID: prod.ID, SKU: prod.SKU, Description: prod.Description,
			Quantity: q.WireString(), UOM: uom, PriceUOM: row.PriceUOM, UOMQty: uq.WireString(), PriceUOMQty: pq.WireString(),
			StockUOM: prod.StockUOM, StockQuantity: stock.WireString(), PriceKey: row.PriceKey, PriceSource: row.PriceSource,
			PriceBasis: row.PriceBasis, LevelFallback: fallback, UnitPriceTenThousandths: row.UnitPriceTenThousandths,
			LineTotalCents: int64(*probe.LineTotal), Taxable: prod.Taxable}
		sale.Lines = append(sale.Lines, line)
		sale.SubtotalCents += line.LineTotalCents
		if line.Taxable {
			taxable += line.LineTotalCents
		}
	}
	if !exempt {
		sale.TaxCents = int64(salesdoc.TaxAt(httpx.Cents(taxable), scaled))
	}
	sale.TotalCents = sale.SubtotalCents + sale.TaxCents
	tenders := spec.tenders
	if len(tenders) == 0 {
		tenders = []pos.SignedSaleTender{{Method: "cash"}}
	}
	for _, tn := range tenders {
		if tn.AmountCents == 0 {
			tn.AmountCents = sale.TotalCents
		}
		sale.Tenders = append(sale.Tenders, tn)
	}
	var paid int64
	for _, tn := range sale.Tenders {
		paid += tn.AmountCents
	}
	if paid > sale.TotalCents {
		sale.ChangeCents = paid - sale.TotalCents
	}
	b.queueCount++
	b.queueTotal += sale.TotalCents
	sale.QueueCount, sale.QueueTotalCents = b.queueCount, b.queueTotal
	for _, e := range edit {
		e(sale)
	}
	raw := b.sign(t, sale)
	return raw, sale
}

// sign signs a sale over its canonical bytes and moves the chain head over
// it, as the core does.
func (b *offlineBench) sign(t *testing.T, sale *pos.SignedOfflineSale) json.RawMessage {
	t.Helper()
	sale.DeviceSig = ""
	canonical := canonicalOf(t, sale)
	sum := sha256.Sum256(canonical)
	b.head = hex.EncodeToString(sum[:])
	sale.DeviceSig = base64.StdEncoding.EncodeToString(ed25519.Sign(b.priv, canonical))
	raw, err := json.Marshal(sale)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// canonicalOf is the device's canonical form (sorted keys, no whitespace,
// no device_sig), built independently of the server's: a generic map
// marshalled by encoding/json sorts its keys, and the bench's strings carry
// no character the two escapers differ on.
func canonicalOf(t *testing.T, sale *pos.SignedOfflineSale) []byte {
	t.Helper()
	raw, err := json.Marshal(sale)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	delete(m, "device_sig")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func findRow(s pos.OfflineSnapshot, product, key string) *pos.OfflineSnapshotPrice {
	for i := range s.PriceSheet {
		if s.PriceSheet[i].ProductID == product && s.PriceSheet[i].PriceKey == key {
			return &s.PriceSheet[i]
		}
	}
	return nil
}

func mustQ(t *testing.T, s string) httpx.Quantity {
	t.Helper()
	q, err := httpx.ParseQuantity(s)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// sync sends the items as one batch, answering the result; the bench takes
// the confirmed mark back, as the device does.
func (b *offlineBench) sync(t *testing.T, items ...json.RawMessage) *pos.OfflineSyncResult {
	t.Helper()
	res, err := b.trySync(items...)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return res
}

func (b *offlineBench) trySync(items ...json.RawMessage) (*pos.OfflineSyncResult, error) {
	res, err := b.f.service.SyncOffline(context.Background(), &pos.OfflineBatch{BatchID: uuid.NewString(),
		RegisterID: b.register, DeviceEpoch: b.epoch, ReportedSeq: b.seq, Items: items}, "sync-user")
	if err == nil && res.DeviceEpoch == b.epoch && res.ConfirmedSeq > b.confirmed {
		b.confirmed = res.ConfirmedSeq
		// the answered items leave the device's queue
		b.queueCount, b.queueTotal = 0, 0
	}
	return res, err
}

// outcomeOf finds one item's answer.
func outcomeOf(t *testing.T, res *pos.OfflineSyncResult, sale *pos.SignedOfflineSale) pos.OfflineItemOutcome {
	t.Helper()
	var found []pos.OfflineItemOutcome
	for _, o := range res.Items {
		if o.ClientID == sale.ClientID {
			found = append(found, o)
		}
	}
	if len(found) == 0 {
		t.Fatalf("no answer for sale %s in %+v", sale.ClientID, res.Items)
	}
	return found[len(found)-1]
}

// wantOutcome asserts an item's outcome and, for a refusal, its reason.
func wantOutcome(t *testing.T, res *pos.OfflineSyncResult, sale *pos.SignedOfflineSale, outcome, reason string) pos.OfflineItemOutcome {
	t.Helper()
	o := outcomeOf(t, res, sale)
	got := ""
	if o.ReasonCode != nil {
		got = *o.ReasonCode
	}
	if o.Outcome != outcome || got != reason {
		msg := ""
		if o.ReasonMessage != nil {
			msg = *o.ReasonMessage
		}
		t.Fatalf("sale %d: outcome %s %q (%s), want %s %q", sale.DeviceSeq, o.Outcome, got, msg, outcome, reason)
	}
	return o
}

// deviceMark reads the register's mark and head.
func (b *offlineBench) deviceMark(t *testing.T) (int64, string) {
	t.Helper()
	var seq int64
	var head string
	if err := b.f.db.Pool.QueryRow(context.Background(), `SELECT high_seq, chain_head FROM pos_register_devices
		WHERE register_id = $1 AND epoch = $2`, b.register, b.epoch).Scan(&seq, &head); err != nil {
		t.Fatal(err)
	}
	return seq, head
}

// exceptions lists the register's exception rows as reason/status pairs in
// sequence order.
func (b *offlineBench) exceptions(t *testing.T) []string {
	t.Helper()
	rows, err := b.f.db.Pool.Query(context.Background(), `SELECT device_seq, reason_code, status FROM pos_offline_exceptions
		WHERE register_id = $1 ORDER BY device_seq, created_at`, b.register)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var seq int64
		var reason, status string
		if err := rows.Scan(&seq, &reason, &status); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d:%s/%s", seq, reason, status))
	}
	sort.Strings(out)
	return out
}

// postedCount counts the register's posted offline sales.
func (b *offlineBench) postedCount(t *testing.T) int64 {
	t.Helper()
	return countOf(t, b.f.db, `SELECT count(*) FROM pos_transactions WHERE register_id = $1 AND offline_ref IS NOT NULL
		AND status = 'COMPLETED'`, b.register)
}

func line(product uuid.UUID, qty string) offlineLine { return offlineLine{product: product, qty: qty} }

func uuidPtr(id uuid.UUID) *uuid.UUID { return &id }
