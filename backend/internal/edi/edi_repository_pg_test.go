// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package edi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// The catalog upsert and the partner CRUD statements need a real database: the
// ON CONFLICT (partner_id, vendor_sku) DO UPDATE that makes a re-import
// idempotent, and the jsonb/text[] round trip on transport_config and
// supported_documents.
//
// Skips cleanly when Postgres is unreachable (testutil.RequireDB).

func seedPartner(t *testing.T, repo *PostgresEDIRepository) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	p := &TradingPartner{
		Name:               "PGTest Partner " + uuid.NewString()[:8],
		EDIVersion:         "004010",
		TransportType:      "SFTP",
		TransportConfig:    `{"host":"sftp.example"}`,
		SupportedDocuments: []string{"832", "846", "850"},
		IsActive:           true,
	}
	if err := repo.CreatePartner(ctx, p); err != nil {
		t.Fatalf("seed partner: %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.db.Pool.Exec(context.Background(), `DELETE FROM edi_trading_partners WHERE id = $1`, p.ID)
	})
	return p.ID
}

// CORRECTNESS: a created partner round-trips through Postgres with its jsonb
// transport config and its text[] document set intact. Those two columns are
// the ones with a type conversion in the statement ($11::jsonb and a Go
// []string), so they are where a silent loss would happen.
func TestPostgresEDIRepository_PartnerRoundTrips(t *testing.T) {
	repo := NewEDIRepository(testutil.RequireDB(t))
	ctx := context.Background()

	id := seedPartner(t, repo)
	got, err := repo.GetPartner(ctx, id)
	if err != nil {
		t.Fatalf("GetPartner: %v", err)
	}

	// jsonb is a parsed representation, so Postgres returns its own
	// canonical spacing; compare the decoded value, not the bytes.
	var cfg map[string]string
	if err := json.Unmarshal([]byte(got.TransportConfig), &cfg); err != nil {
		t.Fatalf("transport_config %q did not round-trip as JSON: %v", got.TransportConfig, err)
	}
	if cfg["host"] != "sftp.example" {
		t.Errorf("transport_config = %q, want host sftp.example", got.TransportConfig)
	}
	if len(got.SupportedDocuments) != 3 ||
		got.SupportedDocuments[0] != "832" || got.SupportedDocuments[2] != "850" {
		t.Errorf("supported_documents = %v, want [832 846 850]", got.SupportedDocuments)
	}
	if got.EDIVersion != "004010" || got.TransportType != "SFTP" || !got.IsActive {
		t.Errorf("partner = %+v, want the stored configuration", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps were not stamped: %+v", got)
	}
}

// CORRECTNESS: re-importing the same vendor SKU updates the row rather than
// duplicating it. A catalog refresh runs on a schedule, so a non-idempotent
// upsert would grow the table without bound and leave two prices for one SKU.
func TestPostgresEDIRepository_SaveCatalogEntriesIsIdempotentPerVendorSKU(t *testing.T) {
	repo := NewEDIRepository(testutil.RequireDB(t))
	ctx := context.Background()
	partner := seedPartner(t, repo)

	first := []CatalogEntry{
		{VendorSKU: "AL-2X4", Description: "2x4-8 SPF Stud", UnitCost: 4.75, UOM: "EA", MinOrderQty: 1, PackQty: 1},
		{VendorSKU: "AL-2X6", Description: "2x6-8 SPF Stud", UnitCost: 7.10, UOM: "EA", MinOrderQty: 1, PackQty: 1},
	}
	n, err := repo.SaveCatalogEntries(ctx, partner, first)
	if err != nil {
		t.Fatalf("SaveCatalogEntries: %v", err)
	}
	if n != 2 {
		t.Errorf("saved %d, want 2", n)
	}

	// Same SKU, new price.
	repriced := []CatalogEntry{
		{VendorSKU: "AL-2X4", Description: "2x4-8 SPF Stud", UnitCost: 5.25, UOM: "EA", MinOrderQty: 12, PackQty: 294},
	}
	if _, err := repo.SaveCatalogEntries(ctx, partner, repriced); err != nil {
		t.Fatalf("re-import: %v", err)
	}

	count, err := repo.GetCatalogEntryCount(ctx, partner)
	if err != nil {
		t.Fatalf("GetCatalogEntryCount: %v", err)
	}
	if count != 2 {
		t.Errorf("catalog has %d rows after re-importing one SKU, want 2 — the upsert duplicated instead of updating", count)
	}

	rows, err := repo.ListCatalogEntries(ctx, partner, 100)
	if err != nil {
		t.Fatalf("ListCatalogEntries: %v", err)
	}
	var found bool
	for _, r := range rows {
		if r.VendorSKU != "AL-2X4" {
			continue
		}
		found = true
		if r.UnitCost != 5.25 {
			t.Errorf("unit_cost = %v, want the re-imported 5.25", r.UnitCost)
		}
		if r.MinOrderQty != 12 || r.PackQty != 294 {
			t.Errorf("min_order_qty=%v pack_qty=%v, want the re-imported 12/294", r.MinOrderQty, r.PackQty)
		}
	}
	if !found {
		t.Errorf("AL-2X4 is missing from the catalog after the re-import: %+v", rows)
	}
}

// CORRECTNESS: the catalog listing is scoped to one partner. Two buying groups
// routinely carry the same vendor SKU string, so an unscoped read would show a
// competitor's cost on the partner's page.
func TestPostgresEDIRepository_ListCatalogEntriesIsScopedToThePartner(t *testing.T) {
	repo := NewEDIRepository(testutil.RequireDB(t))
	ctx := context.Background()
	mine, theirs := seedPartner(t, repo), seedPartner(t, repo)

	if _, err := repo.SaveCatalogEntries(ctx, mine,
		[]CatalogEntry{{VendorSKU: "SHARED-SKU", Description: "mine", UnitCost: 4.75, UOM: "EA"}}); err != nil {
		t.Fatalf("save mine: %v", err)
	}
	if _, err := repo.SaveCatalogEntries(ctx, theirs,
		[]CatalogEntry{{VendorSKU: "SHARED-SKU", Description: "theirs", UnitCost: 9.99, UOM: "EA"}}); err != nil {
		t.Fatalf("save theirs: %v", err)
	}

	rows, err := repo.ListCatalogEntries(ctx, mine, 100)
	if err != nil {
		t.Fatalf("ListCatalogEntries: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].Description != "mine" || rows[0].UnitCost != 4.75 {
		t.Errorf("row = %+v, want this partner's own entry", rows[0])
	}
	if rows[0].PartnerID != mine {
		t.Errorf("partner_id = %s, want %s", rows[0].PartnerID, mine)
	}
}

// CORRECTNESS: ListCatalogEntries defaults a non-positive limit to 100 rather
// than emitting `LIMIT 0` (which returns nothing) or `LIMIT -1` (a syntax
// error). The handler always passes 200, so this guard only matters for direct
// callers — but it is the guard the code claims to have.
func TestPostgresEDIRepository_ListCatalogEntriesDefaultsTheLimit(t *testing.T) {
	repo := NewEDIRepository(testutil.RequireDB(t))
	ctx := context.Background()
	partner := seedPartner(t, repo)

	if _, err := repo.SaveCatalogEntries(ctx, partner,
		[]CatalogEntry{{VendorSKU: "AL-2X4", Description: "x", UnitCost: 1, UOM: "EA"}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	for _, limit := range []int{0, -1} {
		rows, err := repo.ListCatalogEntries(ctx, partner, limit)
		if err != nil {
			t.Fatalf("ListCatalogEntries(limit=%d): %v", limit, err)
		}
		if len(rows) != 1 {
			t.Errorf("limit=%d returned %d rows, want the 1 stored row (the limit guard is gone)", limit, len(rows))
		}
	}
}

// CORRECTNESS: updating a partner that does not exist is a not-found, not a
// silent success. Every other module in this tree checks it — crm's Update
// (activity.go:150), project's UpdateProject (repository.go:94) and portal's
// SetOrderProject (orders.go:73) all return not-found on zero rows affected.
//
// UpdatePartner used to discard the pgconn.CommandTag entirely, so
// PUT /api/v1/edi/partners/{any-uuid} answered 200 and echoed the submitted
// body back. An operator who edits a partner that was deleted in another tab
// was told the save worked and walked away with credentials that were never
// stored.
func TestPostgresEDIRepository_UpdateMissingPartnerIsNotFound(t *testing.T) {
	repo := NewEDIRepository(testutil.RequireDB(t))

	ghost := &TradingPartner{
		ID: uuid.New(), Name: "Ghost", EDIVersion: "004010",
		TransportType: "SFTP", TransportConfig: "{}", SupportedDocuments: []string{"850"},
	}
	err := repo.UpdatePartner(context.Background(), ghost)
	if err == nil {
		t.Fatal("UpdatePartner on a nonexistent partner succeeded, want a not-found error")
	}
	// The sentinel, not just any error: the handler maps it to 404 with
	// errors.Is, and a wrapped driver failure must keep its 500.
	if !errors.Is(err, ErrPartnerNotFound) {
		t.Errorf("UpdatePartner error = %v, want ErrPartnerNotFound", err)
	}

	// And nothing was created as a side effect — this is a no-op UPDATE, not an
	// upsert, so the ghost must still not exist.
	if _, err := repo.GetPartner(context.Background(), ghost.ID); err == nil {
		t.Error("the no-op UPDATE created a partner row")
	}
}

// CORRECTNESS: the other half of the same check — a partner that IS there is
// still updated, so the RowsAffected guard cannot have been implemented as a
// blanket refusal.
func TestPostgresEDIRepository_UpdateExistingPartnerStillWrites(t *testing.T) {
	repo := NewEDIRepository(testutil.RequireDB(t))
	ctx := context.Background()
	id := seedPartner(t, repo)

	p, err := repo.GetPartner(ctx, id)
	if err != nil {
		t.Fatalf("GetPartner: %v", err)
	}
	p.Name = "ACME Renamed"
	p.EDIVersion = "005010"
	if err := repo.UpdatePartner(ctx, p); err != nil {
		t.Fatalf("UpdatePartner on an existing partner: %v", err)
	}

	got, err := repo.GetPartner(ctx, id)
	if err != nil {
		t.Fatalf("GetPartner after update: %v", err)
	}
	if got.Name != "ACME Renamed" || got.EDIVersion != "005010" {
		t.Errorf("stored partner = %+v, want the updated name and version", got)
	}
}
