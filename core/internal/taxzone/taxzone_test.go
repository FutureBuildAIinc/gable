// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package taxzone_test

// The zone tables of CP-10: the lookup that resolves a place of receipt to
// the zone that covers it (an exact code, a code inside a range, the five
// digit fallback through a plus four, the lowest combined rate where two
// zones cover one code, a province zone for a Canadian delivery, a window
// that does not cover the date), and the admin half (create, read, list,
// close, delete of an unused zone), with three creators contending at pool
// size 4.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/gablelbm/gable/internal/testutil"
)

func mustCreate(t *testing.T, repo *taxzone.Repository, d *taxzone.Draft) *taxzone.Zone {
	t.Helper()
	z, err := repo.Create(context.Background(), d)
	if err != nil {
		t.Fatalf("create zone: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), z.ID) })
	return z
}

func postalDraft(code, high string, parts ...taxzone.RatePart) *taxzone.Draft {
	return &taxzone.Draft{
		Country: "US", JurisdictionCode: "Z-" + code, JurisdictionType: "CTY", JurisdictionName: "The county",
		ZoneKind: taxzone.KindPostal, Sourcing: taxzone.SourcingDestination,
		EffectiveFrom: "2020-01-01", DataSource: "dealer", DataVersion: "1",
		Rates: parts,
		Boundaries: []taxzone.Boundary{{Country: "US", PostalLow: code, PostalHigh: high}},
	}
}

func part(name, rate string) taxzone.RatePart {
	return taxzone.RatePart{Name: name, Rate: rate, EffectiveFrom: "2020-01-01"}
}

func TestLookupResolvesTheZoneThatCoversThePlaceOfReceipt(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := taxzone.NewRepository(db)
	ctx := context.Background()
	on := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	// A range zone at 8.5 percent (6.5 state + 2 city) covering 98000-98199,
	// and an exact zone at 3 percent on 98109: the exact code is the lower
	// combined rate in the area, which is the published rule.
	mustCreate(t, repo, postalDraft("98000", "98199", part(taxzone.PartState, "0.065000"), part(taxzone.PartCity, "0.020000")))
	mustCreate(t, repo, postalDraft("98109", "98109", part(taxzone.PartState, "0.030000")))

	// A code inside the range takes the range's zone and its combined rate is
	// the sum of its parts.
	z, err := taxzone.Lookup(ctx, db, taxzone.LookupInput{Country: "US", Postal: "98150", On: on})
	if err != nil {
		t.Fatalf("lookup 98150: %v", err)
	}
	if z.CombinedRate != "0.085000" {
		t.Errorf("combined = %q, want 0.085000", z.CombinedRate)
	}
	if len(z.Parts) != 2 {
		t.Errorf("parts = %d, want 2", len(z.Parts))
	}
	if z.DataSource != "dealer 1" {
		t.Errorf("data source = %q, want dealer 1", z.DataSource)
	}

	// A code two zones cover takes the lowest combined rate in the area.
	z, err = taxzone.Lookup(ctx, db, taxzone.LookupInput{Country: "US", Postal: "98109", On: on})
	if err != nil {
		t.Fatalf("lookup 98109: %v", err)
	}
	if z.CombinedRate != "0.030000" {
		t.Errorf("combined = %q, want the lowest combined 0.030000", z.CombinedRate)
	}

	// A plus four code falls back to the five digit area, the published rule.
	z, err = taxzone.Lookup(ctx, db, taxzone.LookupInput{Country: "US", Postal: "98109-9998", On: on})
	if err != nil || z.CombinedRate != "0.030000" {
		t.Errorf("lookup 98109-9998 = %v %q, want 0.030000", err, z.CombinedRate)
	}

	// A code no zone covers is not an error the caller surfaces.
	if _, err := taxzone.Lookup(ctx, db, taxzone.LookupInput{Country: "US", Postal: "99999", On: on}); !errors.Is(err, taxzone.ErrZoneNotFound) {
		t.Errorf("lookup 99999 = %v, want ErrZoneNotFound", err)
	}
}

func TestLookupHonoursTheEffectiveWindow(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := taxzone.NewRepository(db)
	ctx := context.Background()
	on := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	future := postalDraft("97000", "97099", part(taxzone.PartState, "0.040000"))
	future.EffectiveFrom = "2050-01-01"
	mustCreate(t, repo, future)

	closed := postalDraft("97000", "97099", part(taxzone.PartState, "0.050000"))
	to := "2020-12-31"
	closed.EffectiveTo = &to
	mustCreate(t, repo, closed)

	if _, err := taxzone.Lookup(ctx, db, taxzone.LookupInput{Country: "US", Postal: "97050", On: on}); !errors.Is(err, taxzone.ErrZoneNotFound) {
		t.Errorf("a window that does not cover the date matched: %v", err)
	}
}

func TestLookupMatchesAProvinceZoneByRegion(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := taxzone.NewRepository(db)
	ctx := context.Background()
	on := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	// A Canadian province zone: the goods tax and the provincial sales tax
	// are two parts of one province.
	mustCreate(t, repo, &taxzone.Draft{
		Country: "CA", JurisdictionCode: "CA-BC", JurisdictionType: "PROV", JurisdictionName: "British Columbia",
		ProvinceCode: "BC", ZoneKind: taxzone.KindProvince, Sourcing: taxzone.SourcingDestination,
		EffectiveFrom: "2020-01-01", DataSource: "dealer", DataVersion: "1",
		Rates: []taxzone.RatePart{part(taxzone.PartGoodsTax, "0.050000"), part(taxzone.PartProvincialSales, "0.070000")},
	})

	z, err := taxzone.Lookup(ctx, db, taxzone.LookupInput{Country: "CA", Postal: "V0A 1A0", Region: "BC", On: on})
	if err != nil {
		t.Fatalf("lookup BC: %v", err)
	}
	if z.CombinedRate != "0.120000" {
		t.Errorf("combined = %q, want 0.120000", z.CombinedRate)
	}
	if _, err := taxzone.Lookup(ctx, db, taxzone.LookupInput{Country: "CA", Postal: "T2P 1J7", Region: "AB", On: on}); !errors.Is(err, taxzone.ErrZoneNotFound) {
		t.Errorf("a province no zone covers matched: %v", err)
	}
}

func TestZoneAdminCreateReadListCloseDelete(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := taxzone.NewRepository(db)
	ctx := context.Background()

	z := mustCreate(t, repo, postalDraft("60000", "60099", part(taxzone.PartState, "0.062500")))

	got, err := repo.Get(ctx, z.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if combined, ok := got.CombinedRate(); !ok || combined != "0.062500" {
		t.Errorf("combined = %q %v, want 0.062500", combined, ok)
	}

	list, err := repo.List(ctx, "US", "CTY")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, lz := range list {
		if lz.ID == z.ID {
			found = true
		}
	}
	if !found {
		t.Error("the created zone is not in the filtered list")
	}
	if _, err := repo.List(ctx, "CA", "CTY"); err != nil {
		t.Fatalf("list CA: %v", err)
	}

	// An unused zone deletes; the delete answers the close alternative.
	n, err := repo.UsedBy(ctx, z.ID)
	if err != nil || n != 0 {
		t.Fatalf("used by = %d %v, want 0", n, err)
	}
	if err := repo.Delete(ctx, z.ID); err != nil {
		t.Fatalf("delete an unused zone: %v", err)
	}

	// A used zone closes: its window ends on the named date and the sales
	// before it keep the rate they were charged.
	z2 := mustCreate(t, repo, postalDraft("61000", "61099", part(taxzone.PartState, "0.070000")))
	if err := repo.Close(ctx, z2.ID, "2026-06-30"); err != nil {
		t.Fatalf("close: %v", err)
	}
	closed, err := repo.Get(ctx, z2.ID)
	if err != nil || closed.EffectiveTo == nil || *closed.EffectiveTo != "2026-06-30" {
		t.Fatalf("closed zone = %v %v", closed.EffectiveTo, err)
	}
}

func TestZoneCreateContendsAtPoolSizeFour(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	repo := taxzone.NewRepository(db)

	// Three creators contend: each writes its zone, its parts and its
	// boundaries in one transaction, and every one lands whole.
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code := "62" + string(rune('0'+i)) + "00"
			_, errs[i] = repo.Create(context.Background(), postalDraft(code, code+"99", part(taxzone.PartState, "0.060000")))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("creator %d: %v", i, err)
		}
	}
}
