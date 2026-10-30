// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// tamper changes a signed item without signing it again.
func tamper(t *testing.T, raw json.RawMessage, key string, v any) json.RawMessage {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m[key] = v
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// RULE (ADR 0011 section 2 and 7 "A mismatch over an exception row", "A
// mismatch row is not the record of its id"): a sale refused for stock, then
// the same id under a different valid signature, writes a replay_mismatch
// row, leaves the first row untouched and advances nothing; sent again, it is
// refused as a mismatch again, never answered from the mismatch row.
func TestOfflineSync_AMismatchOverAnExceptionRowAdvancesNothing(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "500")}})
	wantOutcome(t, b.sync(t, r1), s1, pos.OfflineRefused, "insufficient_stock")
	_, head := b.deviceMark(t)

	// the same id and number, other figures, a valid signature
	b.seq, b.head = 0, "GENESIS"
	r1b, s1b := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}},
		func(s *pos.SignedOfflineSale) { s.ClientID = s1.ClientID })
	for i := 0; i < 2; i++ {
		wantOutcome(t, b.sync(t, r1b), s1b, pos.OfflineRefused, "replay_mismatch")
	}
	if got := b.exceptions(t); len(got) != 3 || got[0] != "1:insufficient_stock/open" ||
		got[1] != "1:replay_mismatch/open" || got[2] != "1:replay_mismatch/open" {
		t.Errorf("rows = %v, want the stock row untouched and two mismatch rows", got)
	}
	if seq, h := b.deviceMark(t); seq != 1 || h != head {
		t.Errorf("the mismatch moved the mark to %d (head %s)", seq, h)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, s1.ClientID); n != 0 {
		t.Errorf("the mismatch posted the sale")
	}
}

// RULE (ADR 0011 section 2 and 7 "Duplicates across both tables"): a new id
// under a used (register, epoch, sequence) answers sequence_reused and writes
// an exception row, never a posting, and moves nothing.
func TestOfflineSync_AReusedSequenceIsRefused(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	wantOutcome(t, b.sync(t, r1), s1, pos.OfflinePosted, "")
	// a restored store restarts its counter under the same epoch
	b.seq, b.head = 0, "GENESIS"
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "2")}})
	wantOutcome(t, b.sync(t, r2), s2, pos.OfflineRefused, "sequence_reused")
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, s2.ClientID); n != 0 {
		t.Errorf("the reused sequence posted")
	}
	if got := f.stock(); got != "99.0000/0.0000" {
		t.Errorf("stock = %s, want 99", got)
	}
}

// RULE (ADR 0011 3.4 and 7 "A tampered copy never locks out the true sale"):
// a batch whose second item is a tampered copy records it bad_signature,
// leaves the mark at the first and holds the third broken_chain; the next
// sync carries the true second: it posts, the bad row reads superseded, the
// held third is re-offered in the same sync, posts, and its row reads
// relinked, the mark at the third, with no manager act.
func TestOfflineSync_ATamperedCopyNeverLocksOutTheTrueSale(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "2")}})
	r3, s3 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "3")}})
	bad := tamper(t, r2, "total_cents", 1)
	res := b.sync(t, r1, bad, r3)
	wantOutcome(t, res, s1, pos.OfflinePosted, "")
	wantOutcome(t, res, s2, pos.OfflineRefused, "bad_signature")
	wantOutcome(t, res, s3, pos.OfflineHeld, "broken_chain")
	if seq, _ := b.deviceMark(t); seq != 1 {
		t.Fatalf("mark = %d, want 1", seq)
	}
	res = b.sync(t, r2)
	wantOutcome(t, res, s2, pos.OfflinePosted, "")
	wantOutcome(t, res, s3, pos.OfflinePosted, "")
	if got := b.exceptions(t); len(got) != 2 || got[0] != "2:bad_signature/superseded" || got[1] != "3:broken_chain/relinked" {
		t.Errorf("rows = %v, want the bad copy superseded and the held third relinked", got)
	}
	if seq, head := b.deviceMark(t); seq != 3 || head != b.head {
		t.Errorf("mark = %d, want 3 at the third's digest", seq)
	}
	if got := f.stock(); got != "94.0000/0.0000" {
		t.Errorf("stock = %s, want 94", got)
	}
	// the held third, sent again, is the duplicate it now is
	wantOutcome(t, b.sync(t, r3), s3, pos.OfflineDuplicate, "")
}

// RULE (ADR 0011 3.4 and 7 "Late after a gap"): with the second sale lost,
// the third is held; a manager's accept moves the mark over the gap and
// leaves a sequence_gap item, and the chain restarts: the held third is
// re-offered at the next sync and its own link is adopted, the fourth
// verifies against it; the lost second then arrives verified and is
// late_after_gap in the queue, the gap item reads found, and the mark and
// head do not move.
func TestOfflineSync_LateAfterAnAcceptedGapAndTheChainRestart(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	r3, s3 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	res := b.sync(t, r1, r3)
	wantOutcome(t, res, s1, pos.OfflinePosted, "")
	wantOutcome(t, res, s3, pos.OfflineHeld, "broken_chain")

	// the manager's accept (slice O4's act): the mark over the gap, the
	// chain to restart, a sequence_gap item naming the missing number in its
	// gap range (device_seq 0: migration 146's unique sequence index does
	// not exclude sequence_gap rows, so the item cannot sit at the number it
	// names while the late sale may still arrive under it)
	mustExec(t, f.db, `UPDATE pos_register_devices SET high_seq = 2, chain_head = 'RESTART' WHERE id = $1`, b.deviceID)
	mustExec(t, f.db, `INSERT INTO pos_offline_exceptions (sale_id, register_id, branch_id, device_epoch, device_seq,
		gap_from_seq, gap_to_seq, business_date, reason_code, status)
		VALUES ($1, $2, $3, 1, 0, 2, 2, $4::date, 'sequence_gap', 'resolved')`, uuid.New(), b.register, f.branchID,
		b.snap.TillSession.BusinessDate)

	r4, s4 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	res = b.sync(t, r4)
	wantOutcome(t, res, s3, pos.OfflinePosted, "")
	wantOutcome(t, res, s4, pos.OfflinePosted, "")
	seq, head := b.deviceMark(t)
	if seq != 4 || head != b.head {
		t.Fatalf("mark = %d, want 4 at the fourth's digest", seq)
	}

	res = b.sync(t, r2)
	wantOutcome(t, res, s2, pos.OfflineRefused, "late_after_gap")
	if s, h := b.deviceMark(t); s != 4 || h != head {
		t.Errorf("the late sale moved the mark to %d", s)
	}
	if got := b.exceptions(t); len(got) != 4 || got[0] != "0:sequence_gap/found" || got[1] != "2:late_after_gap/open" ||
		got[2] != "3:broken_chain/relinked" || got[3] != "4:broken_chain/relinked" {
		t.Errorf("rows = %v, want the late row open, the gap found and the held third and fourth relinked", got)
	}
}

// RULE (ADR 0011 3.2 and 7 "Settled replaced cart", "A replaced cart of
// another register", "Re-ring under a result unknown cart"): an offline sale
// naming an open cart of its own register and session abandons it in the
// posting transaction, with no reversal and an audit row; a later sale that
// keeps the default link is refused replaced_sale_settled; one declared for a
// different customer posts with its declaration; a cart of another register
// is refused replaced_sale_mismatch and left open; a completed cart is
// refused replaced_sale_settled and nothing is voided.
func TestOfflineSync_TheReplacedCart(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	cart := f.startSale(nil)
	if r := f.addLine(cart, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	link := func(id string) func(*pos.SignedOfflineSale) {
		return func(s *pos.SignedOfflineSale) { s.ReplacesSaleID = &id }
	}
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}}, link(cart))
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}}, link(cart))
	r3, s3 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}}, func(s *pos.SignedOfflineSale) {
		s.DifferentCustomer = true
		s.UnknownCartID = &cart
	})
	res := b.sync(t, r1, r2, r3)
	wantOutcome(t, res, s1, pos.OfflinePosted, "")
	wantOutcome(t, res, s2, pos.OfflineRefused, "replaced_sale_settled")
	wantOutcome(t, res, s3, pos.OfflinePosted, "")
	if st := str(t, f.getSale(t, cart), "status"); st != "abandoned" {
		t.Errorf("the replaced cart reads %s, want abandoned", st)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.transaction.abandoned' AND entity_id = $1`, cart); n != 1 {
		t.Errorf("%d abandon audit rows, want 1", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1 AND different_customer AND unknown_cart_id = $2
		AND replaces_sale_id IS NULL`, s3.ClientID, cart); n != 1 {
		t.Errorf("the declared sale does not carry its declaration")
	}
	if got := f.stock(); got != "98.0000/0.0000" {
		t.Errorf("stock = %s, want 98 (two offline sales, the abandoned cart moved nothing)", got)
	}

	// a cart of another register: refused, left open
	other := "REG-" + uuid.NewString()[:8]
	mustExec(t, f.db, `INSERT INTO pos_registers (id, location_id, name, branch_id) VALUES ($1, $2, 'other', $3)`,
		other, f.yardID, f.branchID)
	f.ensureTillOn(other)
	foreign := f.startSaleOn(other)
	r4, s4 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}}, link(foreign))
	wantOutcome(t, b.sync(t, r4), s4, pos.OfflineRefused, "replaced_sale_mismatch")
	if st := str(t, f.getSale(t, foreign), "status"); st != "open" {
		t.Errorf("the other register's cart reads %s, want open", st)
	}

	// a cart whose completion committed: refused, nothing voided
	done, _ := f.saleOf("1", tender("cash", 599))
	r5, s5 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}}, link(done))
	wantOutcome(t, b.sync(t, r5), s5, pos.OfflineRefused, "replaced_sale_settled")
	if st := str(t, f.getSale(t, done), "status"); st != "completed" {
		t.Errorf("the completed cart reads %s, want completed", st)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id IN ($1, $2)`, s4.ClientID, s5.ClientID); n != 0 {
		t.Errorf("a refused replacement posted")
	}
}
