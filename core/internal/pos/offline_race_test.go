// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// syncOnce runs one batch through the service, leaving the bench's own
// bookkeeping alone (safe from several goroutines).
func syncOnce(f *fixture, register string, items []json.RawMessage) (*pos.OfflineSyncResult, error) {
	return f.service.SyncOffline(context.Background(), &pos.OfflineBatch{BatchID: uuid.NewString(), RegisterID: register,
		DeviceEpoch: 1, Items: items}, "sync-user")
}

// RULE (ADR 0011 3.2 and 7 "Ledger and stock once under contention"): the
// same batch, one item of it refused for stock, posted by two concurrent
// requests beside a live completion for the same customer, at pool size 4
// with three contenders: each good sale posts once, exactly one exception
// row is written for the refused item, no item fails and nothing deadlocks.
func TestOfflineSync_TheSameBatchTwiceBesideALiveSale(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	cust := uuidPtr(f.customerID)
	r1, s1 := b.build(t, saleSpec{customer: cust, lines: []offlineLine{line(f.productID, "1")}})
	r2, s2 := b.build(t, saleSpec{customer: cust, lines: []offlineLine{line(f.productID, "500")}})
	r3, s3 := b.build(t, saleSpec{customer: cust, lines: []offlineLine{line(f.productID, "1")}})
	items := []json.RawMessage{r1, r2, r3}
	live := f.startSale(cust)
	if r := f.addLine(live, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}

	liveTotal := num(t, f.getSale(t, live), "total_cents")
	var wg sync.WaitGroup
	results := make([]*pos.OfflineSyncResult, 2)
	errs := make([]error, 2)
	var liveStatus int
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = syncOnce(f, b.register, items)
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		liveStatus = f.completeSale(live, tender("cash", liveTotal)).status
	}()
	wg.Wait()

	if liveStatus != http.StatusOK {
		t.Errorf("the live completion = %d, want 200", liveStatus)
	}
	posted := map[string]int{}
	for i, res := range results {
		if errs[i] != nil {
			t.Fatalf("sync %d: %v", i, errs[i])
		}
		for _, o := range res.Items {
			if o.Outcome == "error" {
				t.Errorf("sync %d: item %d failed: %s", i, o.DeviceSeq, derefStr(o.ReasonMessage))
			}
			if o.Outcome == pos.OfflinePosted {
				posted[o.ClientID]++
			}
		}
	}
	if posted[s1.ClientID] != 1 || posted[s3.ClientID] != 1 || posted[s2.ClientID] != 0 {
		t.Errorf("posted answers = %v, want the first and third once each", posted)
	}
	if n := b.postedCount(t); n != 2 {
		t.Errorf("%d offline sales posted, want 2", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_offline_exceptions WHERE sale_id = $1`, s2.ClientID); n != 1 {
		t.Errorf("%d exception rows for the refused item, want 1", n)
	}
	if got := f.stock(); got != "97.0000/0.0000" {
		t.Errorf("stock = %s, want 97 (two offline, one live)", got)
	}
	if n := countOf(t, f.db, `SELECT count(DISTINCT invoice_id) FROM pos_transactions WHERE register_id = $1 AND status = 'COMPLETED'`,
		b.register); n != 3 {
		t.Errorf("%d invoices, want 3", n)
	}
	if seq, _ := b.deviceMark(t); seq != 3 {
		t.Errorf("mark = %d, want 3", seq)
	}
}

// RULE (ADR 0011 2 and 7 "Numbering under two registers offline"): two
// registers queue sales with overlapping sequence numbers and sync them
// interleaved and concurrently, beside a third contender replaying one of
// the batches: every sale gets one POS number and one invoice number, all
// distinct, and no unique index fires.
func TestOfflineSync_NumberingUnderTwoRegistersOffline(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	offlinePolicyOn(t, f)
	b1 := newOfflineBench(t, f)
	b2 := newOfflineBench(t, f, "REG-"+uuid.NewString()[:8])
	var all []*pos.SignedOfflineSale
	batch := map[*offlineBench][]json.RawMessage{}
	for i := 0; i < 3; i++ {
		for _, b := range []*offlineBench{b1, b2} {
			raw, s := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
			batch[b] = append(batch[b], raw)
			all = append(all, s)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for _, b := range []*offlineBench{b1, b2} {
		wg.Add(1)
		go func(b *offlineBench) {
			defer wg.Done()
			// one item a request, interleaved with the other register's
			for _, raw := range batch[b] {
				if res, err := syncOnce(f, b.register, []json.RawMessage{raw}); err != nil {
					errs <- err
				} else {
					for _, o := range res.Items {
						if o.Outcome == "error" {
							errs <- errorString(derefStr(o.ReasonMessage))
						}
					}
				}
			}
		}(b)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if res, err := syncOnce(f, b2.register, batch[b2]); err != nil {
			errs <- err
		} else {
			for _, o := range res.Items {
				if o.Outcome == "error" {
					errs <- errorString(derefStr(o.ReasonMessage))
				}
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a contender failed: %v", err)
	}
	ids := make([]string, 0, len(all))
	for _, s := range all {
		ids = append(ids, s.ClientID)
	}
	var sales, numbers, invoices, invoiceNumbers int64
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*), count(DISTINCT t.number), count(DISTINCT t.invoice_id),
		count(DISTINCT i.number) FROM pos_transactions t JOIN invoices i ON i.id = t.invoice_id
		WHERE t.id = ANY($1::uuid[]) AND t.status = 'COMPLETED'`, ids).Scan(&sales, &numbers, &invoices, &invoiceNumbers); err != nil {
		t.Fatal(err)
	}
	if sales != 6 || numbers != 6 || invoices != 6 || invoiceNumbers != 6 {
		t.Errorf("sales %d, POS numbers %d, invoices %d, invoice numbers %d, want 6 of each", sales, numbers, invoices, invoiceNumbers)
	}
	if got := f.stock(); got != "94.0000/0.0000" {
		t.Errorf("stock = %s, want 94", got)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
