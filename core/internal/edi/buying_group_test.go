// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package edi

import (
	"errors"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"
)

// EDI is an untrusted-input surface: a buying group's 832 catalog and 846
// inventory advice arrive as third-party text and drive purchasing decisions.
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

func newBG() *BuyingGroupService {
	return NewBuyingGroupService(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// --- EDI 832 price/sales catalog ----------------------------------------

// CORRECTNESS: the supplier name from the N1*SU segment must be stamped on
// every item that follows it, so a catalog price can be attributed to a vendor.
func TestParse832Catalog_VendorNameFromN1SU(t *testing.T) {
	bg := newBG()
	doc := "ISA*00*~GS*SC*~ST*832*0001~N1*SU*ACME LUMBER~LIN*1*VP*AL-2X4*SK*2X4-8~CTP*RS*RES*4.75*1*EA~SE*5*0001~"

	entries, err := bg.Parse832Catalog(doc)
	if err != nil {
		t.Fatalf("Parse832Catalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if entries[0].VendorName != "ACME LUMBER" {
		t.Errorf("VendorName = %q, want %q", entries[0].VendorName, "ACME LUMBER")
	}
}

// CORRECTNESS: a non-supplier N1 (N1*BY = buyer, N1*ST = ship-to) must not be
// mistaken for the supplier, or the whole catalog gets filed under the wrong
// trading partner.
func TestParse832Catalog_IgnoresNonSupplierN1(t *testing.T) {
	bg := newBG()
	doc := "N1*BY*OUR COMPANY~N1*ST*JOBSITE 12~LIN*1*VP*X1~CTP*RS*RES*1.00*1*EA~"

	entries, err := bg.Parse832Catalog(doc)
	if err != nil {
		t.Fatalf("Parse832Catalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if entries[0].VendorName != "" {
		t.Errorf("VendorName = %q, want empty: only N1*SU identifies the supplier", entries[0].VendorName)
	}
}

// CORRECTNESS: CTP carries the price. It must be parsed to the exact decimal
// and the unit of measure must come with it, because a price without a UOM is
// unusable for comparison (per-piece vs per-thousand-board-feet).
func TestParse832Catalog_PriceAndUOM(t *testing.T) {
	tests := []struct {
		name    string
		segment string
		want    float64
		wantUOM string
	}{
		{"dollars and cents", "CTP*RS*RES*12.50*1*EA", 12.50, "EA"},
		{"sub-cent precision is preserved", "CTP*RS*RES*0.4375*1*LF", 0.4375, "LF"},
		{"per MBF pricing", "CTP*RS*RES*685.00*1*MBF", 685.00, "MBF"},
		{"zero price", "CTP*RS*RES*0*1*EA", 0, "EA"},
		{"unparseable price leaves the entry at zero", "CTP*RS*RES*N/A*1*EA", 0, "EA"},
		{"missing UOM element", "CTP*RS*RES*9.99", 9.99, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bg := newBG()
			entries, err := bg.Parse832Catalog("N1*SU*V~LIN*1*VP*SKU1~" + tc.segment + "~")
			if err != nil {
				t.Fatalf("Parse832Catalog: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("parsed %d entries, want 1", len(entries))
			}
			if math.Abs(entries[0].UnitPrice-tc.want) > 1e-9 {
				t.Errorf("UnitPrice = %v, want %v", entries[0].UnitPrice, tc.want)
			}
			if entries[0].UOM != tc.wantUOM {
				t.Errorf("UOM = %q, want %q", entries[0].UOM, tc.wantUOM)
			}
		})
	}
}

// CORRECTNESS: DTM*196 is the effective date and DTM*197 the expiry. Mixing
// them up would either apply future prices today or expire a live price list.
func TestParse832Catalog_EffectiveAndExpiryDates(t *testing.T) {
	bg := newBG()
	doc := "N1*SU*V~LIN*1*VP*SKU1~CTP*RS*RES*1.00*1*EA~DTM*196*20260101~DTM*197*20261231~"

	entries, err := bg.Parse832Catalog(doc)
	if err != nil {
		t.Fatalf("Parse832Catalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if got := entries[0].EffectiveDate.Format("2006-01-02"); got != "2026-01-01" {
		t.Errorf("EffectiveDate = %s, want 2026-01-01", got)
	}
	if got := entries[0].ExpiryDate.Format("2006-01-02"); got != "2026-12-31" {
		t.Errorf("ExpiryDate = %s, want 2026-12-31", got)
	}
}

// CORRECTNESS: an unrecognised or malformed date qualifier must leave the
// defaults alone rather than zeroing the effective date to year 1.
func TestParse832Catalog_BadDateLeavesDefaultEffectiveDate(t *testing.T) {
	bg := newBG()
	before := time.Now().Add(-time.Second)

	entries, err := bg.Parse832Catalog("N1*SU*V~LIN*1*VP*SKU1~DTM*196*not-a-date~DTM*999*20260101~")
	if err != nil {
		t.Fatalf("Parse832Catalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if entries[0].EffectiveDate.Before(before) {
		t.Errorf("EffectiveDate = %v, want the 'now' default when the segment is unparseable", entries[0].EffectiveDate)
	}
	if !entries[0].ExpiryDate.IsZero() {
		t.Errorf("ExpiryDate = %v, want zero when no DTM*197 was present", entries[0].ExpiryDate)
	}
}

// CORRECTNESS: every LIN starts a new item, including the last one in the
// document, and defaults must be sane (a min order quantity of 0 would let a
// buyer order nothing).
func TestParse832Catalog_MultipleItemsAndDefaults(t *testing.T) {
	bg := newBG()
	doc := "N1*SU*V~" +
		"LIN*1*VP*A1~CTP*RS*RES*1.00*1*EA~" +
		"LIN*2*VP*A2~CTP*RS*RES*2.00*1*EA~" +
		"LIN*3*VP*A3~CTP*RS*RES*3.00*1*EA~"

	entries, err := bg.Parse832Catalog(doc)
	if err != nil {
		t.Fatalf("Parse832Catalog: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("parsed %d entries, want 3 (the final LIN must be flushed)", len(entries))
	}
	for i, e := range entries {
		if e.MinOrderQty != 1 {
			t.Errorf("entry %d MinOrderQty = %v, want 1", i, e.MinOrderQty)
		}
		if e.PackSize != 1 {
			t.Errorf("entry %d PackSize = %d, want 1", i, e.PackSize)
		}
	}
	if entries[2].UnitPrice != 3.00 {
		t.Errorf("last entry price = %v, want 3.00", entries[2].UnitPrice)
	}
}

// CORRECTNESS: garbage in must not panic or invent items.
func TestParse832Catalog_DegenerateInput(t *testing.T) {
	tests := []struct{ name, doc string }{
		{"empty", ""},
		{"only separators", "~~~~"},
		{"whitespace segments", "  ~ \t ~"},
		{"unknown segments only", "ISA*00~GS*SC~SE*1*0001~"},
		{"truncated LIN", "LIN"},
		{"CTP before any LIN", "CTP*RS*RES*1.00*1*EA~"},
		{"PID before any LIN", "PID*F****Nothing~"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bg := newBG()
			entries, err := bg.Parse832Catalog(tc.doc)
			if err != nil {
				t.Fatalf("Parse832Catalog(%q): %v", tc.doc, err)
			}
			if tc.name == "truncated LIN" {
				// A bare LIN still opens an item; it just has no identifiers.
				if len(entries) != 1 {
					t.Fatalf("parsed %d entries, want 1", len(entries))
				}
				return
			}
			if len(entries) != 0 {
				t.Fatalf("parsed %d entries from %q, want 0", len(entries), tc.doc)
			}
		})
	}
}

// CORRECTNESS: ValidateX12 answers the question the lenient parser above
// cannot — "is this an X12 document at all?" — and it answers it structurally,
// not by counting what came out. The two cases that must stay apart are the
// last entry in each table: an envelope with no catalog items is X12 (and
// Parse832Catalog rightly returns zero entries and no error for it, above),
// while a CSV posted to the X12 endpoint is not a document this parser can
// read and has to be refused rather than reported as importing nothing.
func TestValidateX12_RecognisesSegmentedDocumentsOnly(t *testing.T) {
	x12 := map[string]string{
		"a catalog": "N1*SU*ACME~LIN*1*VP*AL-2X4~CTP*RS*RES*4.75*1*EA~",
		"envelope segments only": "ISA*00*00*ZZ*GABLELBM~GS*SC*GABLELBM*ACME*20260101~" +
			"ST*832*0001~SE*3*0001~GE*1*1~IEA*1*000000001~",
		"a bare segment with no elements": "SE~",
		"newline-terminated segments":     "ISA*00*00\nGS*SC*GABLELBM\n",
		"leading blank segments":          "~~N1*SU*ACME~",
	}
	for name, doc := range x12 {
		t.Run(name, func(t *testing.T) {
			if err := ValidateX12(doc); err != nil {
				t.Errorf("ValidateX12(%q) = %v, want nil", doc, err)
			}
		})
	}

	notX12 := map[string]string{
		"empty":              "",
		"separators only":    "~~~~",
		"whitespace":         "  ~ \t ~",
		"prose":              "this is not an EDI document at all",
		"a CSV":              "vendor_sku,sku,unit_price\nAL-2X4,2X4-8,4.75\n",
		"a lower-case token": "lin*1*VP*AL-2X4~",
		"an over-long id":    "SEGMENT*1~",
	}
	for name, doc := range notX12 {
		t.Run(name, func(t *testing.T) {
			if err := ValidateX12(doc); !errors.Is(err, ErrNotX12) {
				t.Errorf("ValidateX12(%q) = %v, want ErrNotX12", doc, err)
			}
		})
	}
}

// CORRECTNESS: the LIN segment is LIN01=line-number followed by
// (qualifier, value) pairs — exactly as the code's own comment documents:
// "LIN*1*VP*VENDOR-SKU*SK*OUR-SKU". The pair walk must therefore start at
// LIN02 (index 2); starting at index 1 reads ("1","VP"), ("VENDOR-SKU","SK")
// and never sees a real qualifier, leaving both identifiers empty.
//
// The consequence compounds: ImportCatalog skips any entry whose SKU and
// VendorSKU are both empty, so a mis-parsed 832 catalog imports zero items.
func TestParse832Catalog_LINIdentifiersAreDropped(t *testing.T) {
	bg := newBG()
	entries, err := bg.Parse832Catalog("N1*SU*ACME~LIN*1*VP*AL-2X4*SK*2X4-8~CTP*RS*RES*4.75*1*EA~")
	if err != nil {
		t.Fatalf("Parse832Catalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if entries[0].VendorSKU != "AL-2X4" {
		t.Errorf("VendorSKU = %q, want %q", entries[0].VendorSKU, "AL-2X4")
	}
	if entries[0].SKU != "2X4-8" {
		t.Errorf("SKU = %q, want %q", entries[0].SKU, "2X4-8")
	}
}

// CORRECTNESS: X12 PID puts the free-text description in PID05, which is
// elements[5] — the code's own example segment, PID*F****Description, splits
// into six elements with the description last. Reading elements[4] picks up
// the empty PID04 placeholder, so descriptions come out blank.
func TestParse832Catalog_PIDDescriptionIsDropped(t *testing.T) {
	bg := newBG()
	entries, err := bg.Parse832Catalog("N1*SU*V~LIN*1*VP*A1~PID*F****2X4-8 SPF STUD~CTP*RS*RES*4.75*1*EA~")
	if err != nil {
		t.Fatalf("Parse832Catalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if entries[0].Description != "2X4-8 SPF STUD" {
		t.Errorf("Description = %q, want %q", entries[0].Description, "2X4-8 SPF STUD")
	}
}

// --- EDI 846 inventory inquiry ------------------------------------------

// CORRECTNESS: QTY*33 is the quantity available. Getting it or its UOM wrong
// drives a purchasing decision against stock that is not there.
func TestParse846Inquiry_QuantityLeadTimeAndVendor(t *testing.T) {
	bg := newBG()
	doc := "N1*SU*ACME LUMBER~LIN*1*VP*AL-2X4~QTY*33*500*EA~LDT*AF*7*DA~"

	results, err := bg.Parse846Inquiry(doc)
	if err != nil {
		t.Fatalf("Parse846Inquiry: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("parsed %d results, want 1", len(results))
	}
	r := results[0]
	if r.VendorName != "ACME LUMBER" {
		t.Errorf("VendorName = %q, want %q", r.VendorName, "ACME LUMBER")
	}
	if r.QtyAvailable != 500 {
		t.Errorf("QtyAvailable = %v, want 500", r.QtyAvailable)
	}
	if r.UOM != "EA" {
		t.Errorf("UOM = %q, want EA", r.UOM)
	}
	if r.LeadTimeDays != 7 {
		t.Errorf("LeadTimeDays = %d, want 7", r.LeadTimeDays)
	}
	if r.AsOfDate != time.Now().Format("2006-01-02") {
		t.Errorf("AsOfDate = %q, want today", r.AsOfDate)
	}
}

// CORRECTNESS: fractional and zero availability must survive. Rounding 0.5 MBF
// to 0 or to 1 both misstate what can be bought; treating 0 as "no data" would
// hide a stockout.
func TestParse846Inquiry_QuantityEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		seg  string
		want float64
	}{
		{"whole", "QTY*33*500*EA", 500},
		{"fractional", "QTY*33*12.75*MBF", 12.75},
		{"zero means out of stock", "QTY*33*0*EA", 0},
		{"unparseable leaves zero", "QTY*33*many*EA", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bg := newBG()
			results, err := bg.Parse846Inquiry("N1*SU*V~LIN*1*VP*A~" + tc.seg + "~")
			if err != nil {
				t.Fatalf("Parse846Inquiry: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("parsed %d results, want 1", len(results))
			}
			if results[0].QtyAvailable != tc.want {
				t.Errorf("QtyAvailable = %v, want %v", results[0].QtyAvailable, tc.want)
			}
		})
	}
}

// CORRECTNESS: every LIN opens a new result and the last one is flushed.
func TestParse846Inquiry_MultipleLinesAndFlush(t *testing.T) {
	bg := newBG()
	doc := "N1*SU*V~LIN*1*VP*A~QTY*33*10*EA~LIN*2*VP*B~QTY*33*20*EA~LIN*3*VP*C~QTY*33*30*EA~"

	results, err := bg.Parse846Inquiry(doc)
	if err != nil {
		t.Fatalf("Parse846Inquiry: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("parsed %d results, want 3", len(results))
	}
	if results[2].QtyAvailable != 30 {
		t.Errorf("last result qty = %v, want 30", results[2].QtyAvailable)
	}
}

// CORRECTNESS: the 846's PID has the same shape as the 832's — the free-text
// description is PID05, which is elements[5]. Reading elements[4] picks up the
// empty PID04 placeholder, so an inventory advice arrives with no description
// and a buyer cannot tell what the availability figure refers to.
func TestParse846Inquiry_PIDDescription(t *testing.T) {
	bg := newBG()
	results, err := bg.Parse846Inquiry("N1*SU*V~LIN*1*VP*AL-2X4~PID*F****2X4-8 SPF STUD~QTY*33*500*EA~")
	if err != nil {
		t.Fatalf("Parse846Inquiry: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("parsed %d results, want 1", len(results))
	}
	if results[0].Description != "2X4-8 SPF STUD" {
		t.Errorf("Description = %q, want %q", results[0].Description, "2X4-8 SPF STUD")
	}
}

func TestParse846Inquiry_DegenerateInput(t *testing.T) {
	for _, doc := range []string{"", "~~~", "QTY*33*5*EA~", "LDT*AF*7*DA~"} {
		bg := newBG()
		results, err := bg.Parse846Inquiry(doc)
		if err != nil {
			t.Fatalf("Parse846Inquiry(%q): %v", doc, err)
		}
		if len(results) != 0 {
			t.Errorf("parsed %d results from %q, want 0", len(results), doc)
		}
	}
}

// CORRECTNESS: the 846 LIN has the same shape as the 832's. A qualifier scan
// starting at index 1 and stepping by two only ever inspects odd indices,
// while "VP" sits at index 2 in LIN*1*VP*SKU, so VendorSKU stays empty.
func TestParse846Inquiry_VendorSKUIsDropped(t *testing.T) {
	bg := newBG()
	results, err := bg.Parse846Inquiry("N1*SU*V~LIN*1*VP*AL-2X4~QTY*33*500*EA~")
	if err != nil {
		t.Fatalf("Parse846Inquiry: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("parsed %d results, want 1", len(results))
	}
	if results[0].VendorSKU != "AL-2X4" {
		t.Errorf("VendorSKU = %q, want %q", results[0].VendorSKU, "AL-2X4")
	}
}

// --- CSV catalog ---------------------------------------------------------

// CORRECTNESS: the CSV importer maps by header name, so column order must not
// matter and unknown columns must be ignored rather than shifting the mapping.
func TestParseCSVCatalog_HeaderDrivenMapping(t *testing.T) {
	bg := newBG()
	data := "description,unit_price,vendor_sku,notes,sku,uom,min_order_qty\n" +
		"2x4-8 SPF Stud,4.75,AL-2X4,ignore me,2X4-8,EA,10\n"

	entries, err := bg.ParseCSVCatalog(data, "ACME")
	if err != nil {
		t.Fatalf("ParseCSVCatalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.VendorName != "ACME" {
		t.Errorf("VendorName = %q, want ACME", e.VendorName)
	}
	if e.VendorSKU != "AL-2X4" {
		t.Errorf("VendorSKU = %q, want AL-2X4", e.VendorSKU)
	}
	if e.SKU != "2X4-8" {
		t.Errorf("SKU = %q, want 2X4-8", e.SKU)
	}
	if e.Description != "2x4-8 SPF Stud" {
		t.Errorf("Description = %q", e.Description)
	}
	if e.UnitPrice != 4.75 {
		t.Errorf("UnitPrice = %v, want 4.75", e.UnitPrice)
	}
	if e.UOM != "EA" {
		t.Errorf("UOM = %q, want EA", e.UOM)
	}
	if e.MinOrderQty != 10 {
		t.Errorf("MinOrderQty = %v, want 10", e.MinOrderQty)
	}
}

// CORRECTNESS: headers must be matched case- and whitespace-insensitively,
// because supplier spreadsheets are hand-made.
func TestParseCSVCatalog_HeaderNormalisation(t *testing.T) {
	bg := newBG()
	data := " SKU , Unit_Price ,UOM\nABC,9.99,PC\n"

	entries, err := bg.ParseCSVCatalog(data, "V")
	if err != nil {
		t.Fatalf("ParseCSVCatalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if entries[0].SKU != "ABC" || entries[0].UnitPrice != 9.99 || entries[0].UOM != "PC" {
		t.Errorf("got %+v, want SKU=ABC price=9.99 uom=PC", entries[0])
	}
}

// CORRECTNESS: a missing price column must leave the price at zero rather than
// inventing one, and a header-only file must produce nothing.
func TestParseCSVCatalog_MissingColumnsAndEmptyBody(t *testing.T) {
	bg := newBG()
	entries, err := bg.ParseCSVCatalog("sku\nABC\n", "V")
	if err != nil {
		t.Fatalf("ParseCSVCatalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if entries[0].UnitPrice != 0 {
		t.Errorf("UnitPrice = %v, want 0 when there is no price column", entries[0].UnitPrice)
	}
	if entries[0].MinOrderQty != 1 {
		t.Errorf("MinOrderQty = %v, want the default of 1", entries[0].MinOrderQty)
	}

	empty, err := bg.ParseCSVCatalog("sku,unit_price\n", "V")
	if err != nil {
		t.Fatalf("ParseCSVCatalog(header only): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("parsed %d entries from a header-only file, want 0", len(empty))
	}
}

// CORRECTNESS: a completely empty upload is an error, not an empty success —
// an operator who uploads the wrong file must be told.
func TestParseCSVCatalog_EmptyInputIsAnError(t *testing.T) {
	bg := newBG()
	if _, err := bg.ParseCSVCatalog("", "V"); err == nil {
		t.Fatal("want an error when the CSV has no header row")
	}
}

// CHARACTERIZATION: a row with the wrong number of fields is dropped silently.
// The count of skipped rows is not reported anywhere, so a partially-mangled
// price file imports as a partial catalog with no warning.
func TestParseCSVCatalog_RaggedRowsAreSilentlyDropped(t *testing.T) {
	bg := newBG()
	data := "sku,unit_price,uom\n" +
		"A,1.00,EA\n" +
		"B,2.00\n" + // too few fields
		"C,3.00,EA\n"

	entries, err := bg.ParseCSVCatalog(data, "V")
	if err != nil {
		t.Fatalf("ParseCSVCatalog: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("parsed %d entries, want 2 (the ragged row is dropped)", len(entries))
	}
	if entries[0].SKU != "A" || entries[1].SKU != "C" {
		t.Errorf("got SKUs %q,%q, want A,C", entries[0].SKU, entries[1].SKU)
	}
}

// --- catalog import ------------------------------------------------------

// CORRECTNESS: an entry that identifies no product cannot be priced, so it
// must be skipped and counted as skipped.
func TestImportCatalog_SkipsUnidentifiableEntries(t *testing.T) {
	bg := newBG()
	res := bg.ImportCatalog([]SupplierCatalogEntry{
		{VendorName: "ACME", VendorSKU: "A1", UnitPrice: 1},
		{VendorName: "ACME"}, // no SKU at all
		{VendorName: "ACME", SKU: "OURS-1", UnitPrice: 2},
	})

	if res.ItemsSkipped != 1 {
		t.Errorf("ItemsSkipped = %d, want 1", res.ItemsSkipped)
	}
	if res.ItemsImported != 2 {
		t.Errorf("ItemsImported = %d, want 2", res.ItemsImported)
	}
	if res.VendorName != "ACME" {
		t.Errorf("VendorName = %q, want ACME", res.VendorName)
	}
	if res.SyncedAt == "" {
		t.Error("SyncedAt must be stamped")
	}
	if _, err := time.Parse(time.RFC3339, res.SyncedAt); err != nil {
		t.Errorf("SyncedAt = %q is not RFC3339: %v", res.SyncedAt, err)
	}
}

// CORRECTNESS: re-importing the same vendor SKU is an update, not a duplicate.
// A duplicate would make ComparePrices return two prices for one item.
func TestImportCatalog_ReimportUpdatesInPlace(t *testing.T) {
	bg := newBG()

	first := bg.ImportCatalog([]SupplierCatalogEntry{{VendorName: "ACME", VendorSKU: "A1", UnitPrice: 4.75}})
	if first.ItemsImported != 1 || first.ItemsUpdated != 0 {
		t.Fatalf("first import: imported=%d updated=%d, want 1/0", first.ItemsImported, first.ItemsUpdated)
	}

	second := bg.ImportCatalog([]SupplierCatalogEntry{{VendorName: "ACME", VendorSKU: "A1", UnitPrice: 5.25}})
	if second.ItemsUpdated != 1 || second.ItemsImported != 0 {
		t.Fatalf("second import: imported=%d updated=%d, want 0/1", second.ItemsImported, second.ItemsUpdated)
	}
	if got := bg.GetCatalog(); len(got) != 1 {
		t.Fatalf("catalog holds %d entries, want 1", len(got))
	} else if got[0].UnitPrice != 5.25 {
		t.Errorf("UnitPrice = %v, want the updated 5.25", got[0].UnitPrice)
	}
}

// CORRECTNESS: the same vendor SKU offered by two different vendors is two
// different offers and must both be kept, or price comparison silently loses a
// competing quote.
func TestImportCatalog_SameSKUFromDifferentVendorsCoexist(t *testing.T) {
	bg := newBG()
	res := bg.ImportCatalog([]SupplierCatalogEntry{
		{VendorName: "ACME", VendorSKU: "2X4-8", UnitPrice: 4.75},
		{VendorName: "BETA", VendorSKU: "2X4-8", UnitPrice: 4.50},
	})
	if res.ItemsImported != 2 {
		t.Fatalf("ItemsImported = %d, want 2", res.ItemsImported)
	}
	if len(bg.GetCatalog()) != 2 {
		t.Fatalf("catalog holds %d entries, want 2", len(bg.GetCatalog()))
	}
}

// CORRECTNESS: deduplication must not key on (VendorSKU, VendorName) alone. An
// entry that carries our own SKU but no vendor SKU — which ImportCatalog
// explicitly admits, since it only skips when BOTH are empty — would otherwise
// collide with every other vendor-SKU-less entry from the same vendor, and the
// second product would silently overwrite the first. The comparison falls back
// to SKU when VendorSKU is empty.
func TestImportCatalog_EmptyVendorSKUMustNotCollapseDistinctProducts(t *testing.T) {
	bg := newBG()
	res := bg.ImportCatalog([]SupplierCatalogEntry{
		{VendorName: "ACME", SKU: "2X4-8", UnitPrice: 4.75},
		{VendorName: "ACME", SKU: "2X6-8", UnitPrice: 7.25},
	})
	if res.ItemsImported != 2 {
		t.Fatalf("ItemsImported = %d (updated=%d), want 2 distinct products", res.ItemsImported, res.ItemsUpdated)
	}
	if len(bg.GetCatalog()) != 2 {
		t.Fatalf("catalog holds %d entries, want 2", len(bg.GetCatalog()))
	}
}

// --- price comparison ----------------------------------------------------

// CORRECTNESS: savings is current cost minus catalog price, and the percentage
// is that saving as a share of what we pay today. Sign and denominator both
// matter — a negative saving must be reported as "not a better price".
func TestComparePrices(t *testing.T) {
	bg := newBG()
	bg.ImportCatalog([]SupplierCatalogEntry{
		{VendorName: "ACME", VendorSKU: "AL-2X4", SKU: "2X4-8", UnitPrice: 4.00,
			EffectiveDate: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)},
	})

	tests := []struct {
		name           string
		lookupSKU      string
		currentCost    float64
		wantCount      int
		wantSavings    float64
		wantSavingsPct float64
		wantBetter     bool
	}{
		{"catalog beats our cost", "2X4-8", 5.00, 1, 1.00, 20, true},
		{"catalog is worse than our cost", "2X4-8", 3.00, 1, -1.00, -100.0 / 3.0, false},
		{"identical price is not an improvement", "2X4-8", 4.00, 1, 0, 0, false},
		{"lookup by the vendor's own SKU also matches", "AL-2X4", 5.00, 1, 1.00, 20, true},
		{"unknown SKU returns nothing", "NOT-STOCKED", 5.00, 0, 0, 0, false},
		{"zero current cost avoids a division by zero", "2X4-8", 0, 1, -4.00, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bg.ComparePrices(tc.lookupSKU, tc.currentCost, "2x4-8 Stud")
			if len(got) != tc.wantCount {
				t.Fatalf("got %d comparisons, want %d", len(got), tc.wantCount)
			}
			if tc.wantCount == 0 {
				return
			}
			c := got[0]
			if math.Abs(c.Savings-tc.wantSavings) > 1e-9 {
				t.Errorf("Savings = %v, want %v", c.Savings, tc.wantSavings)
			}
			if math.Abs(c.SavingsPct-tc.wantSavingsPct) > 1e-9 {
				t.Errorf("SavingsPct = %v, want %v", c.SavingsPct, tc.wantSavingsPct)
			}
			if c.IsBetterPrice != tc.wantBetter {
				t.Errorf("IsBetterPrice = %v, want %v", c.IsBetterPrice, tc.wantBetter)
			}
			if c.SKU != tc.lookupSKU {
				t.Errorf("SKU = %q, want the SKU that was asked for (%q)", c.SKU, tc.lookupSKU)
			}
			if c.VendorName != "ACME" {
				t.Errorf("VendorName = %q, want ACME", c.VendorName)
			}
			if c.CatalogDate != "2026-01-15" {
				t.Errorf("CatalogDate = %q, want 2026-01-15", c.CatalogDate)
			}
		})
	}
}

// CORRECTNESS: when several vendors carry the same item, every offer must be
// returned so the buyer can choose; silently returning only the first would
// hide the cheapest.
func TestComparePrices_ReturnsEveryVendorOffer(t *testing.T) {
	bg := newBG()
	bg.ImportCatalog([]SupplierCatalogEntry{
		{VendorName: "ACME", VendorSKU: "V1", SKU: "2X4-8", UnitPrice: 4.75},
		{VendorName: "BETA", VendorSKU: "V2", SKU: "2X4-8", UnitPrice: 4.10},
		{VendorName: "GAMMA", VendorSKU: "V3", SKU: "2X4-8", UnitPrice: 5.50},
	})

	got := bg.ComparePrices("2X4-8", 5.00, "2x4-8 Stud")
	if len(got) != 3 {
		t.Fatalf("got %d comparisons, want 3", len(got))
	}
	better := 0
	for _, c := range got {
		if c.IsBetterPrice {
			better++
		}
	}
	if better != 2 {
		t.Errorf("%d offers flagged better than $5.00, want 2", better)
	}
}

// CORRECTNESS: an empty catalog must produce no comparisons rather than a
// zero-price "saving".
func TestComparePrices_EmptyCatalog(t *testing.T) {
	bg := newBG()
	if got := bg.ComparePrices("ANY", 10, "x"); len(got) != 0 {
		t.Fatalf("got %d comparisons from an empty catalog, want 0", len(got))
	}
	if got := bg.GetCatalog(); len(got) != 0 {
		t.Fatalf("GetCatalog on a fresh service returned %d entries, want 0", len(got))
	}
}

// SyncSupplierPricing is an acknowledged stub; the only contract it has today
// is that it does not fail and does not fabricate catalog data.
func TestSyncSupplierPricing_IsAHarmlessStub(t *testing.T) {
	bg := newBG()
	if err := bg.SyncSupplierPricing(); err != nil {
		t.Fatalf("SyncSupplierPricing: %v", err)
	}
	if len(bg.GetCatalog()) != 0 {
		t.Error("the stub must not invent catalog entries")
	}
}
