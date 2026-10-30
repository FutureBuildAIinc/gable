// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package taxzone_test

// CP-14 A1's Canadian rule: a Canadian branch's pickup takes the branch's own
// zone, a delivery its destination's, and none answering is refused in words
// that name the province; the small supplier switch drops the GST part; each
// part is priced on its own; the frozen parts read back as printed lines with
// the province's labels. A US branch reads nothing of this.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

func provinceDraft(province string, parts ...taxzone.RatePart) *taxzone.Draft {
	return &taxzone.Draft{
		Country: "CA", JurisdictionCode: "PROV-" + province, JurisdictionType: "PROV", JurisdictionName: province,
		ProvinceCode: province, ZoneKind: taxzone.KindProvince, Sourcing: taxzone.SourcingBoth,
		EffectiveFrom: "2020-01-01", DataSource: "dealer", DataVersion: "1", Rates: parts,
	}
}

func branchIn(t *testing.T, db *database.DB, country, state, zip string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO locations (id, type, code, name, state, zip, country_code, timezone, default_tax_rate)
		VALUES ($1, 'BRANCH', $2, 'CP-14 rule branch', $3, $4, $5, 'Etc/UTC', 0.120000)`,
		id, "CP14-"+id.String()[:8], state, zip, country); err != nil {
		t.Fatalf("branch: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, id) })
	return id
}

func setNotRegistered(t *testing.T, db *database.DB, on bool) {
	t.Helper()
	var was bool
	var number string
	ctx := context.Background()
	if err := db.Pool.QueryRow(ctx, `SELECT gst_hst_not_registered, gst_hst_number FROM dealer_profile WHERE id = 1`).Scan(&was, &number); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE dealer_profile SET gst_hst_not_registered = $1, gst_hst_number = '' WHERE id = 1`, on); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `UPDATE dealer_profile SET gst_hst_not_registered = $1, gst_hst_number = $2 WHERE id = 1`, was, number)
	})
}

func TestCanadaRule(t *testing.T) {
	db := testutil.RequireDB(t)
	clearZones(t, db)
	repo := taxzone.NewRepository(db)
	ctx := context.Background()
	on := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	mustCreate(t, repo, provinceDraft("BC", part(taxzone.PartGoodsTax, "0.050000"), part(taxzone.PartProvincialSales, "0.070000")))
	setNotRegistered(t, db, false)

	t.Run("a US branch reads nothing", func(t *testing.T) {
		got, err := taxzone.Canada(ctx, db, taxzone.CanadaInput{BranchID: branchIn(t, db, "US", "WA", "98109"), On: on})
		if err != nil || got.Canadian || got.Zone != nil {
			t.Fatalf("US branch: %+v, %v", got, err)
		}
	})

	t.Run("a BC pickup takes the branch's own zone", func(t *testing.T) {
		got, err := taxzone.Canada(ctx, db, taxzone.CanadaInput{BranchID: branchIn(t, db, "CA", "BC", "V1X 7K2"), On: on})
		if err != nil || !got.Canadian || got.Destination || got.Zone == nil {
			t.Fatalf("BC pickup: %+v, %v", got, err)
		}
		if got.Zone.CombinedRate != "0.120000" || got.Zone.Country != "CA" || got.Zone.Province != "BC" || len(got.Zone.Parts) != 2 {
			t.Errorf("zone = %+v", got.Zone)
		}
	})

	t.Run("a Canadian branch with no zone is refused, naming its province", func(t *testing.T) {
		_, err := taxzone.Canada(ctx, db, taxzone.CanadaInput{BranchID: branchIn(t, db, "CA", "AB", "T2P 1J9"), On: on})
		var e *taxzone.PartsNotConfiguredError
		if !errors.As(err, &e) || e.Destination || e.Province != "AB" {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(e.ClerkWords(), "Alberta") {
			t.Errorf("words = %q", e.ClerkWords())
		}
		h, ok := taxzone.AsPartsNotConfigured(err)
		if !ok || h.Status != 409 || h.Details[0].Message == "" {
			t.Errorf("wire = %+v", h)
		}
	})

	t.Run("a delivery no zone answers is refused with the two ways forward", func(t *testing.T) {
		_, err := taxzone.Canada(ctx, db, taxzone.CanadaInput{BranchID: branchIn(t, db, "CA", "BC", "V1X 7K2"), On: on,
			ShipTo: &taxzone.Place{Country: "CA", Postal: "T2P 1J9", Region: "AB"}})
		var e *taxzone.PartsNotConfiguredError
		if !errors.As(err, &e) || !e.Destination {
			t.Fatalf("err = %v", err)
		}
		want := "No tax zone covers Alberta: ask an admin to add one, or take the sale as a pickup."
		if e.ClerkWords() != want {
			t.Errorf("words = %q, want %q", e.ClerkWords(), want)
		}
	})

	t.Run("a delivery takes the destination's zone, never the branch's", func(t *testing.T) {
		dest := &taxzone.Snapshot{ZoneID: uuid.New(), CombinedRate: "0.130000", Country: "CA", Province: "ON",
			Parts: []taxzone.Part{{Name: taxzone.PartGoodsTax, Rate: "0.130000"}}}
		got, err := taxzone.Canada(ctx, db, taxzone.CanadaInput{BranchID: branchIn(t, db, "CA", "BC", "V1X 7K2"), On: on,
			ShipTo: &taxzone.Place{Country: "CA", Region: "ON"}, Destination: dest})
		if err != nil || !got.Destination || got.Zone.CombinedRate != "0.130000" {
			t.Fatalf("delivery: %+v, %v", got, err)
		}
	})

	t.Run("not registered: the GST part is dropped, PST stays", func(t *testing.T) {
		setNotRegistered(t, db, true)
		got, err := taxzone.Canada(ctx, db, taxzone.CanadaInput{BranchID: branchIn(t, db, "CA", "BC", "V1X 7K2"), On: on})
		if err != nil || len(got.Zone.Parts) != 1 || got.Zone.Parts[0].Name != taxzone.PartProvincialSales || got.Zone.CombinedRate != "0.070000" {
			t.Fatalf("not registered: %+v, %v", got.Zone, err)
		}
	})
}

func TestPriceAndLines(t *testing.T) {
	snap := &taxzone.Snapshot{ZoneID: uuid.New(), Country: "CA", Province: "BC", CombinedRate: "0.120000",
		Parts: []taxzone.Part{{Name: taxzone.PartProvincialSales, Rate: "0.070000"}, {Name: taxzone.PartGoodsTax, Rate: "0.050000"}}}
	sum, err := snap.Price(1250)
	if err != nil || sum != 151 {
		t.Fatalf("Price(1250) = %d, %v; want 151", sum, err)
	}
	raw, _ := json.Marshal(snap)
	lines := taxzone.Lines(raw)
	if len(lines) != 2 || lines[0].Label != "GST" || lines[0].RatePercent != "5" || lines[0].Cents != 63 ||
		lines[1].Label != "PST" || lines[1].RatePercent != "7" || lines[1].Cents != 88 {
		t.Fatalf("lines = %+v", lines)
	}
	// A US zone's frozen parts carry no cents and print no lines.
	us, _ := json.Marshal(&taxzone.Snapshot{ZoneID: uuid.New(), CombinedRate: "0.085000",
		Parts: []taxzone.Part{{Name: taxzone.PartState, Rate: "0.085000"}}})
	if got := taxzone.Lines(us); got != nil {
		t.Errorf("US lines = %+v", got)
	}
	if got := taxzone.Lines(nil); got != nil {
		t.Errorf("no parts = %+v", got)
	}
	for _, c := range []struct{ prov, part, want string }{
		{"ON", taxzone.PartGoodsTax, "HST"}, {"NS", taxzone.PartGoodsTax, "HST"}, {"AB", taxzone.PartGoodsTax, "GST"},
		{"SK", taxzone.PartProvincialSales, "PST"}, {"MB", taxzone.PartProvincialSales, "RST"},
		{"QC", taxzone.PartGoodsTax, taxzone.PartGoodsTax}, {"QC", taxzone.PartProvincialSales, taxzone.PartProvincialSales},
	} {
		if got := taxzone.Label("CA", c.prov, c.part); got != c.want {
			t.Errorf("Label(%s, %s) = %q, want %q", c.prov, c.part, got, c.want)
		}
	}
}

// TestReadSetupNamesTheGaps pins the Tech Admin tax setup panel's standing
// warnings (items 364 and 365): a Canadian dealer with no GST/HST number and
// not marked not registered is told so, and a province of active Canadian
// ship-tos with no zone is named with its count, until its zone exists.
func TestReadSetupNamesTheGaps(t *testing.T) {
	db := testutil.RequireDB(t)
	clearZones(t, db)
	repo := taxzone.NewRepository(db)
	ctx := context.Background()
	on := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	setNotRegistered(t, db, false)
	branch := branchIn(t, db, "CA", "BC", "V1X 7K2")

	customer := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Setup Co', $2, $3)`,
		customer, "SETUP-"+customer.String()[:8], branch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM customer_ship_tos WHERE customer_id = $1`, customer)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM customers WHERE id = $1`, customer)
	})
	for i, code := range []string{"A", "B"} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, is_default, is_active)
			VALUES ($1, $2, $3, 'Site', '1 Way', 'Whitehorse', 'YT', 'Y1A 1A1', 'CA', $4, TRUE)`, uuid.New(), customer, code, i == 0); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.ReadSetup(ctx, on)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Canadian || !got.GSTHSTNumberMissing {
		t.Errorf("setup = %+v, want Canadian with the number missing", got)
	}
	yt := func(s *taxzone.Setup) *taxzone.UnzonedProvince {
		for i := range s.UnzonedProvinces {
			if s.UnzonedProvinces[i].Province == "YT" {
				return &s.UnzonedProvinces[i]
			}
		}
		return nil
	}
	if u := yt(got); u == nil || u.Name != "Yukon" || u.ShipTos != 2 {
		t.Fatalf("unzoned = %+v, want Yukon with two ship-tos", got.UnzonedProvinces)
	}

	mustCreate(t, repo, provinceDraft("YT", part(taxzone.PartGoodsTax, "0.050000")))
	if _, err := db.Pool.Exec(ctx, `UPDATE dealer_profile SET gst_hst_number = '123456789RT0001' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	got, err = repo.ReadSetup(ctx, on)
	if err != nil {
		t.Fatal(err)
	}
	if yt(got) != nil || got.GSTHSTNumberMissing {
		t.Errorf("after the zone and the number: %+v", got)
	}
}
