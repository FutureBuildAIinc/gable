// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-14 A1 offline. A Canadian branch's register holds its zone's parts; the
// device signs each part's cents; the replay prices the same parts from the
// same snapshot, posts the sale and freezes the parts with their cents on its
// invoice. A device whose part cents differ from the snapshot's is refused
// tax_unverified, even when its total agrees, never split again silently.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func canadianBench(t *testing.T, f *fixture) *offlineBench {
	t.Helper()
	b := newOfflineBench(t, f)
	b.snap.Tax = pos.OfflineSnapshotTax{BranchRate: "0.120000", ZoneID: uuid.NewString(), DataSource: "seed",
		Country: "CA", Province: "BC", Parts: []pos.OfflineTaxPart{
			{Name: taxzone.PartGoodsTax, Rate: "0.050000", Label: "GST"},
			{Name: taxzone.PartProvincialSales, Rate: "0.070000", Label: "PST"}}}
	b.issue(t)
	return b
}

func TestOfflineCanadianSaleFreezesItsSignedParts(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := canadianBench(t, f)
	// 5 at 5.50 is 27.50: GST 138 and PST 193.
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "5")}})
	if sale.TaxCents != 331 || len(sale.TaxLines) != 2 {
		t.Fatalf("bench sale tax = %d %+v", sale.TaxCents, sale.TaxLines)
	}
	res := b.sync(t, raw)
	wantOutcome(t, res, sale, pos.OfflinePosted, "")
	var (
		taxCents int64
		src      string
		parts    []byte
	)
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT ROUND(i.tax_amount * 100)::bigint, i.tax_source, i.tax_rate_parts
		FROM invoices i JOIN pos_transactions t ON t.invoice_id = i.id WHERE t.register_id = $1`, b.register).Scan(&taxCents, &src, &parts); err != nil {
		t.Fatal(err)
	}
	lines := taxzone.Lines(parts)
	if taxCents != 331 || src != "OFFLINE_RATE" || len(lines) != 2 ||
		lines[0].Label != "GST" || lines[0].Cents != 138 || lines[1].Label != "PST" || lines[1].Cents != 193 {
		t.Errorf("invoice = %d %s %+v, want 331 OFFLINE_RATE with GST 138 and PST 193", taxCents, src, lines)
	}
}

func TestOfflineCanadianSaleWithTamperedPartsIsRefused(t *testing.T) {
	cases := []struct {
		name string
		edit func(*pos.SignedOfflineSale)
	}{
		// the total still agrees: a cent moved from PST to GST
		{"a cent moved between the parts", func(s *pos.SignedOfflineSale) { s.TaxLines[0].Cents++; s.TaxLines[1].Cents-- }},
		{"the parts dropped", func(s *pos.SignedOfflineSale) { s.TaxLines = nil }},
		{"a label changed", func(s *pos.SignedOfflineSale) { s.TaxLines[1].Label = "HST" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, testutil.RequireDB(t))
			offlinePolicyOn(t, f)
			b := canadianBench(t, f)
			raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "5")}}, c.edit)
			res := b.sync(t, raw)
			wantOutcome(t, res, sale, pos.OfflineRefused, "tax_unverified")
		})
	}
}

func TestOfflineUSSaleCarryingPartsIsRefused(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}}, func(s *pos.SignedOfflineSale) {
		s.TaxLines = []pos.SignedTaxLine{{Name: taxzone.PartGoodsTax, Rate: "0.088750", Label: "GST", Cents: s.TaxCents}}
	})
	res := b.sync(t, raw)
	o := wantOutcome(t, res, sale, pos.OfflineRefused, "tax_unverified")
	if b, _ := json.Marshal(o); len(b) == 0 {
		t.Fatal("no outcome")
	}
}

// TestOfflineSnapshotCarriesACanadianBranchsParts: the register's snapshot of
// a Canadian branch holds its zone's parts with their labels and the combined
// rate; a Canadian branch no zone covers is refused the snapshot.
func TestOfflineSnapshotCarriesACanadianBranchsParts(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withEngine(db))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	f.seedBCZone()
	f.placeBranch("CA", "BC", "V1X 7K2")
	t.Cleanup(func() {
		mustExec(t, db, `UPDATE locations SET country_code = NULL, state = NULL, zip = NULL WHERE id = $1`, f.branchID)
	})

	r := takeSnapshot(f, b.register, 0)
	if r.status != 200 {
		t.Fatalf("snapshot = %d %s", r.status, r.raw)
	}
	var snap pos.OfflineSnapshot
	if err := json.Unmarshal(r.raw, &snap); err != nil {
		t.Fatal(err)
	}
	tax := snap.Tax
	if tax.BranchRate != "0.120000" || tax.Country != "CA" || tax.Province != "BC" || len(tax.Parts) != 2 ||
		tax.Parts[0].Label == "" || tax.Parts[1].Label == "" {
		t.Fatalf("snapshot tax = %+v", tax)
	}

	f.placeBranch("CA", "AB", "T2P 1J9")
	if r := takeSnapshot(f, b.register, 0); r.status != 409 || !json.Valid(r.raw) ||
		!containsAll(string(r.raw), taxzone.BlockerPartsNotConfigured, "Alberta") {
		t.Fatalf("an unzoned Canadian branch's snapshot = %d %s", r.status, r.raw)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}
