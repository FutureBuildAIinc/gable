// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package edi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// These are the tests the concrete-*EDIRepository gap used to block. EDIHandler
// now takes the EDIRepository interface declared in edi_repository.go, so the
// partner-creation defaults and the whole catalog ingest path — parse, map,
// persist, summarise — run end to end against a fake store.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// fakeEDIRepo is an in-memory EDIRepository that records every write.
type fakeEDIRepo struct {
	partners map[uuid.UUID]TradingPartner
	catalog  map[uuid.UUID][]CatalogEntry

	createErr error
	getErr    error
	listErr   error
	updateErr error
	deleteErr error
	saveErr   error
	countErr  error

	created     []TradingPartner
	updated     []TradingPartner
	deleted     []uuid.UUID
	savedFor    []uuid.UUID
	savedRows   [][]CatalogEntry
	listLimits  []int
	countCalled []uuid.UUID
}

var _ EDIRepository = (*fakeEDIRepo)(nil)

func newFakeEDIRepo() *fakeEDIRepo {
	return &fakeEDIRepo{
		partners: map[uuid.UUID]TradingPartner{},
		catalog:  map[uuid.UUID][]CatalogEntry{},
	}
}

func (f *fakeEDIRepo) CreatePartner(_ context.Context, p *TradingPartner) error {
	if f.createErr != nil {
		return f.createErr
	}
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	f.created = append(f.created, *p)
	f.partners[p.ID] = *p
	return nil
}

func (f *fakeEDIRepo) GetPartner(_ context.Context, id uuid.UUID) (*TradingPartner, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	p, ok := f.partners[id]
	if !ok {
		return nil, errors.New("trading partner not found")
	}
	return &p, nil
}

func (f *fakeEDIRepo) ListPartners(context.Context) ([]TradingPartner, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]TradingPartner, 0, len(f.partners))
	for _, p := range f.partners {
		out = append(out, p)
	}
	if len(out) == 0 {
		// The Postgres implementation returns a nil slice for an empty table;
		// mirror it so the handler's nil guard is the thing under test.
		return nil, nil
	}
	return out, nil
}

func (f *fakeEDIRepo) UpdatePartner(_ context.Context, p *TradingPartner) error {
	f.updated = append(f.updated, *p)
	if f.updateErr != nil {
		return f.updateErr
	}
	f.partners[p.ID] = *p
	return nil
}

func (f *fakeEDIRepo) DeletePartner(_ context.Context, id uuid.UUID) error {
	f.deleted = append(f.deleted, id)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.partners, id)
	return nil
}

func (f *fakeEDIRepo) SaveCatalogEntries(_ context.Context, partnerID uuid.UUID, entries []CatalogEntry) (int, error) {
	f.savedFor = append(f.savedFor, partnerID)
	f.savedRows = append(f.savedRows, entries)
	if f.saveErr != nil {
		return 0, f.saveErr
	}
	f.catalog[partnerID] = append(f.catalog[partnerID], entries...)
	return len(entries), nil
}

func (f *fakeEDIRepo) ListCatalogEntries(_ context.Context, partnerID uuid.UUID, limit int) ([]CatalogEntry, error) {
	f.listLimits = append(f.listLimits, limit)
	if f.listErr != nil {
		return nil, f.listErr
	}
	rows := f.catalog[partnerID]
	if len(rows) == 0 {
		return nil, nil
	}
	return rows, nil
}

func (f *fakeEDIRepo) GetCatalogEntryCount(_ context.Context, partnerID uuid.UUID) (int, error) {
	f.countCalled = append(f.countCalled, partnerID)
	if f.countErr != nil {
		return 0, f.countErr
	}
	return len(f.catalog[partnerID]), nil
}

func ediMux(repo EDIRepository) *http.ServeMux {
	mux := http.NewServeMux()
	NewEDIHandler(repo, newBG(), nil).RegisterRoutes(mux)
	return mux
}

// --- partner creation defaults ------------------------------------------

// CORRECTNESS: a partner created with only a name gets the defaults a fresh
// trading relationship needs — X12 004010, SFTP transport, an empty JSON
// transport config (the column is jsonb NOT NULL, so "" would fail the insert)
// and the 832/846/850 document set. These are the values every subsequent
// envelope is built from, so they have to be asserted at the persistence seam
// rather than inferred from a 201.
func TestCreatePartner_AppliesTheDefaults(t *testing.T) {
	repo := newFakeEDIRepo()

	rec := doEDI(t, ediMux(repo), http.MethodPost, "/api/v1/edi/partners", `{"name":"ACME Buying Group"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if len(repo.created) != 1 {
		t.Fatalf("CreatePartner reached persistence %d times, want 1", len(repo.created))
	}
	p := repo.created[0]

	if p.Name != "ACME Buying Group" {
		t.Errorf("name = %q", p.Name)
	}
	if p.EDIVersion != "004010" {
		t.Errorf("edi_version = %q, want the 004010 default", p.EDIVersion)
	}
	if p.TransportType != "SFTP" {
		t.Errorf("transport_type = %q, want the SFTP default", p.TransportType)
	}
	if p.TransportConfig != "{}" {
		t.Errorf("transport_config = %q, want the empty JSON object default", p.TransportConfig)
	}
	if got := strings.Join(p.SupportedDocuments, ","); got != "832,846,850" {
		t.Errorf("supported_documents = %v, want [832 846 850]", p.SupportedDocuments)
	}

	// The 201 body is what the admin UI renders straight after a create, so it
	// must carry the same defaults rather than the caller's sparse input.
	var echoed TradingPartner
	if err := json.Unmarshal(rec.Body.Bytes(), &echoed); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if echoed.EDIVersion != "004010" || echoed.TransportType != "SFTP" || echoed.TransportConfig != "{}" {
		t.Errorf("the response body lost the defaults: %+v", echoed)
	}
	if echoed.ID != p.ID {
		t.Errorf("response id %s != persisted id %s", echoed.ID, p.ID)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// CORRECTNESS: an explicitly configured partner keeps its own values. A default
// that overwrote a 005010 partner's version would silently downgrade every
// document sent to them.
func TestCreatePartner_DoesNotOverrideExplicitValues(t *testing.T) {
	repo := newFakeEDIRepo()

	body := `{
		"name":"Do It Best",
		"edi_version":"005010",
		"transport_type":"AS2",
		"transport_config":"{\"host\":\"as2.example\"}",
		"supported_documents":["850","855","810"],
		"isa_sender_id":"GABLELBM","isa_receiver_id":"DIB","is_active":true
	}`
	rec := doEDI(t, ediMux(repo), http.MethodPost, "/api/v1/edi/partners", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}

	p := repo.created[0]
	if p.EDIVersion != "005010" {
		t.Errorf("edi_version = %q, want the caller's 005010", p.EDIVersion)
	}
	if p.TransportType != "AS2" {
		t.Errorf("transport_type = %q, want the caller's AS2", p.TransportType)
	}
	if p.TransportConfig != `{"host":"as2.example"}` {
		t.Errorf("transport_config = %q, want the caller's config", p.TransportConfig)
	}
	if got := strings.Join(p.SupportedDocuments, ","); got != "850,855,810" {
		t.Errorf("supported_documents = %v, want the caller's set", p.SupportedDocuments)
	}
	if p.ISASenderID != "GABLELBM" || p.ISAReceiverID != "DIB" || !p.IsActive {
		t.Errorf("addressing/active flags were not carried through: %+v", p)
	}
}

// CORRECTNESS: an empty supported_documents array is treated as "unset" and
// gets the default set — a partner with no document types configured could
// never send or receive anything.
func TestCreatePartner_EmptyDocumentSetGetsTheDefault(t *testing.T) {
	repo := newFakeEDIRepo()

	rec := doEDI(t, ediMux(repo), http.MethodPost, "/api/v1/edi/partners",
		`{"name":"ACME","supported_documents":[]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if got := strings.Join(repo.created[0].SupportedDocuments, ","); got != "832,846,850" {
		t.Errorf("supported_documents = %v, want the default set", repo.created[0].SupportedDocuments)
	}
}

// CORRECTNESS: a partner with no name never reaches the insert.
func TestCreatePartner_NamelessNeverReachesPersistence(t *testing.T) {
	repo := newFakeEDIRepo()
	rec := doEDI(t, ediMux(repo), http.MethodPost, "/api/v1/edi/partners", `{"edi_version":"004010"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(repo.created) != 0 {
		t.Errorf("a nameless partner was written: %+v", repo.created)
	}
}

// CORRECTNESS: an insert failure is a 500 and the SQL error is not echoed.
func TestCreatePartner_PersistenceFailureIs500AndDoesNotLeak(t *testing.T) {
	repo := newFakeEDIRepo()
	repo.createErr = errors.New(`pq: duplicate key value violates unique constraint "edi_trading_partners_name_key"`)

	rec := doEDI(t, ediMux(repo), http.MethodPost, "/api/v1/edi/partners", `{"name":"ACME"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "edi_trading_partners") {
		t.Errorf("the database error reached the client: %s", rec.Body)
	}
}

// --- read paths ----------------------------------------------------------

// CORRECTNESS: an empty partner table serialises as [] rather than null. This
// is the guard salesteam and crm are missing.
func TestListPartners_EmptyIsAnEmptyArray(t *testing.T) {
	rec := doEDI(t, ediMux(newFakeEDIRepo()), http.MethodGet, "/api/v1/edi/partners", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("empty partner list serialised as %s, want []", body)
	}
}

// CORRECTNESS: the partner detail view carries the catalog row count, which is
// what tells an operator whether the last import landed.
func TestGetPartner_CarriesTheCatalogCount(t *testing.T) {
	repo := newFakeEDIRepo()
	id := uuid.New()
	repo.partners[id] = TradingPartner{ID: id, Name: "ACME", EDIVersion: "004010"}
	repo.catalog[id] = []CatalogEntry{{VendorSKU: "A"}, {VendorSKU: "B"}, {VendorSKU: "C"}}

	rec := doEDI(t, ediMux(repo), http.MethodGet, "/api/v1/edi/partners/"+id.String(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	var got struct {
		TradingPartner
		CatalogCount int `json:"catalog_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if got.ID != id || got.Name != "ACME" {
		t.Errorf("partner = %+v, want the stored row", got.TradingPartner)
	}
	if got.CatalogCount != 3 {
		t.Errorf("catalog_count = %d, want 3", got.CatalogCount)
	}
	if len(repo.countCalled) != 1 || repo.countCalled[0] != id {
		t.Errorf("the count was taken for %v, want the path partner %s", repo.countCalled, id)
	}
}

// CHARACTERIZATION: the catalog count is read with the error discarded
// (edi_handler.go:106, `count, _ :=`), so a failed count renders as 0 rather
// than failing the page. That is a deliberate degrade — the partner's
// credentials are the point of the view — but it means "0 entries" and "the
// count query broke" are indistinguishable in the admin UI.
func TestGetPartner_CountFailureRendersAsZero(t *testing.T) {
	repo := newFakeEDIRepo()
	id := uuid.New()
	repo.partners[id] = TradingPartner{ID: id, Name: "ACME"}
	repo.catalog[id] = []CatalogEntry{{VendorSKU: "A"}}
	repo.countErr = errors.New("count failed")

	rec := doEDI(t, ediMux(repo), http.MethodGet, "/api/v1/edi/partners/"+id.String(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the count failure must not fail the page", rec.Code)
	}
	var got struct {
		CatalogCount int `json:"catalog_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CatalogCount != 0 {
		t.Errorf("catalog_count = %d, want 0 for a failed count", got.CatalogCount)
	}
}

// CORRECTNESS: an unknown partner is a 404.
func TestGetPartner_UnknownIs404(t *testing.T) {
	rec := doEDI(t, ediMux(newFakeEDIRepo()), http.MethodGet, "/api/v1/edi/partners/"+uuid.NewString(), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// CORRECTNESS: the catalog listing is capped at 200 rows and an empty catalog
// serialises as [].
func TestListCatalog_IsCappedAndEmptyIsAnArray(t *testing.T) {
	repo := newFakeEDIRepo()
	id := uuid.New()

	rec := doEDI(t, ediMux(repo), http.MethodGet, "/api/v1/edi/partners/"+id.String()+"/catalog", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("empty catalog serialised as %s, want []", body)
	}
	if len(repo.listLimits) != 1 || repo.listLimits[0] != 200 {
		t.Errorf("query limits = %v, want a single 200-row cap", repo.listLimits)
	}
}

// --- catalog ingest ------------------------------------------------------

const sample832 = "N1*SU*ACME~" +
	"LIN*1*VP*AL-2X4*SK*2X4-8~PID*F****2x4-8 SPF Stud~CTP*RS*RES*4.75*1*EA~DTM*196*20260101~" +
	"LIN*2*VP*AL-2X6*SK*2X6-8~PID*F****2x6-8 SPF Stud~CTP*RS*RES*7.10*1*EA~"

// CORRECTNESS: an 832 upload is parsed, mapped and persisted against the
// partner in the path, and the summary reports what actually happened. The
// partner id is the tenancy key for the catalog table, so it has to be asserted
// at the write, not just echoed in the response.
func TestImportCatalog_X12PersistsAgainstThePathPartner(t *testing.T) {
	repo := newFakeEDIRepo()
	partner := uuid.New()

	rec := doEDI(t, ediMux(repo), http.MethodPost,
		"/api/v1/edi/partners/"+partner.String()+"/import-catalog", sample832)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	if len(repo.savedFor) != 1 {
		t.Fatalf("SaveCatalogEntries called %d times, want 1", len(repo.savedFor))
	}
	if repo.savedFor[0] != partner {
		t.Errorf("catalog written against %s, want the path partner %s", repo.savedFor[0], partner)
	}

	rows := repo.savedRows[0]
	if len(rows) != 2 {
		t.Fatalf("persisted %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].VendorSKU != "AL-2X4" || rows[1].VendorSKU != "AL-2X6" {
		t.Errorf("vendor SKUs = %q,%q, want the partner's AL-2X4,AL-2X6", rows[0].VendorSKU, rows[1].VendorSKU)
	}
	if rows[0].Description != "2x4-8 SPF Stud" {
		t.Errorf("description = %q", rows[0].Description)
	}
	if rows[0].UnitCost != 4.75 || rows[1].UnitCost != 7.10 {
		t.Errorf("unit costs = %v,%v, want 4.75,7.10", rows[0].UnitCost, rows[1].UnitCost)
	}
	if rows[0].UOM != "EA" {
		t.Errorf("uom = %q, want EA", rows[0].UOM)
	}
	if rows[0].MinOrderQty != 1 || rows[0].PackQty != 1 {
		t.Errorf("MinOrderQty=%v PackQty=%v, want the parser defaults of 1", rows[0].MinOrderQty, rows[0].PackQty)
	}

	var summary struct {
		PartnerID   string `json:"partner_id"`
		Format      string `json:"format"`
		ParsedCount int    `json:"parsed_count"`
		SavedCount  int    `json:"saved_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if summary.PartnerID != partner.String() {
		t.Errorf("summary partner_id = %s, want %s", summary.PartnerID, partner)
	}
	if summary.Format != "x12" {
		t.Errorf("format = %q, want the x12 default", summary.Format)
	}
	if summary.ParsedCount != 2 || summary.SavedCount != 2 {
		t.Errorf("parsed=%d saved=%d, want 2 and 2", summary.ParsedCount, summary.SavedCount)
	}
}

// CORRECTNESS: a CSV upload takes the same path and produces the same rows.
func TestImportCatalog_CSVPersistsTheSameShape(t *testing.T) {
	repo := newFakeEDIRepo()
	partner := uuid.New()

	csv := "vendor_sku,sku,description,unit_price,uom,min_order_qty\n" +
		"AL-2X4,2X4-8,2x4-8 SPF Stud,4.75,EA,10\n"
	rec := doEDI(t, ediMux(repo), http.MethodPost,
		"/api/v1/edi/partners/"+partner.String()+"/import-catalog?format=csv", csv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	rows := repo.savedRows[0]
	if len(rows) != 1 {
		t.Fatalf("persisted %d rows, want 1", len(rows))
	}
	if rows[0].VendorSKU != "AL-2X4" {
		t.Errorf("vendor_sku = %q, want AL-2X4 — the partner's identifier, not our 2X4-8", rows[0].VendorSKU)
	}
	if rows[0].UnitCost != 4.75 || rows[0].UOM != "EA" {
		t.Errorf("row = %+v, want 4.75/EA", rows[0])
	}
	if rows[0].MinOrderQty != 10 {
		t.Errorf("min_order_qty = %v, want the CSV's 10", rows[0].MinOrderQty)
	}

	var summary struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if summary.Format != "csv" {
		t.Errorf("format = %q, want csv", summary.Format)
	}
}

// CORRECTNESS: a persistence failure during import is a 500, and no partial
// success is reported.
func TestImportCatalog_SaveFailureIs500(t *testing.T) {
	repo := newFakeEDIRepo()
	repo.saveErr = errors.New(`pq: insert or update on table "edi_catalog_entries" violates foreign key constraint`)

	rec := doEDI(t, ediMux(repo), http.MethodPost,
		"/api/v1/edi/partners/"+uuid.NewString()+"/import-catalog", sample832)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "edi_catalog_entries") {
		t.Errorf("the database error reached the client: %s", rec.Body)
	}
}

// CORRECTNESS: an upload the parser cannot make sense of must be refused, the
// way the CSV branch already refuses one (edi_handler.go:179-183, pinned by
// TestEDIHandler_ImportCatalogRejectsAnUnparseableCSV: "an operator who uploads
// the wrong file needs to be told").
//
// The X12 branch did not hold that line. Parse832Catalog never returned an
// error — it splits on "~", ignores every segment it does not recognise, and
// reports success with zero entries. So the default upload path answered
// 200 {"parsed_count":0,"saved_count":0} for a CSV uploaded without
// ?format=csv, for a PDF, or for an empty body: the operator was told the
// import succeeded and the partner's catalog was silently left as it was.
//
// The handler now recognises the document (ValidateX12) before handing it to
// the lenient parser.
func TestImportCatalog_UnparseableX12IsRefused(t *testing.T) {
	for _, name := range []string{"a CSV uploaded without ?format=csv", "an empty body", "arbitrary text"} {
		body := map[string]string{
			"a CSV uploaded without ?format=csv": "vendor_sku,sku,unit_price\nAL-2X4,2X4-8,4.75\n",
			"an empty body":                      "",
			"arbitrary text":                     "this is not an EDI document at all",
		}[name]

		t.Run(name, func(t *testing.T) {
			repo := newFakeEDIRepo()
			rec := doEDI(t, ediMux(repo), http.MethodPost,
				"/api/v1/edi/partners/"+uuid.NewString()+"/import-catalog", body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422", rec.Code)
			}
		})
	}
}

// CORRECTNESS: the inverse of the characterization that used to pin the silent
// success above, and the reason the refusal has to be a recognition check
// rather than an entry count. The two halves are different answers:
//
//   - a file the parser cannot read is refused, and — unlike before — never
//     reaches SaveCatalogEntries, so the partner's catalog is untouched;
//   - a document that IS X12 and simply carries no catalog items is still a
//     success reporting zero. "Nothing to import" is a real answer.
//
// Collapsing the two — refusing on parsed_count == 0 — would reject a supplier
// sending an empty delta, which is a normal thing for a supplier to send.
func TestImportCatalog_UnreadableIsRefusedButEmptyX12Succeeds(t *testing.T) {
	t.Run("unreadable upload never reaches the write", func(t *testing.T) {
		repo := newFakeEDIRepo()

		rec := doEDI(t, ediMux(repo), http.MethodPost,
			"/api/v1/edi/partners/"+uuid.NewString()+"/import-catalog",
			"vendor_sku,sku,unit_price\nAL-2X4,2X4-8,4.75\n")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		if len(repo.savedFor) != 0 {
			t.Errorf("SaveCatalogEntries was called %d times for a refused upload, want 0", len(repo.savedFor))
		}
	})

	t.Run("valid X12 carrying no catalog items succeeds with zero", func(t *testing.T) {
		repo := newFakeEDIRepo()

		// A well-formed envelope with no LIN segments: recognisably X12, and
		// there is genuinely nothing in it.
		rec := doEDI(t, ediMux(repo), http.MethodPost,
			"/api/v1/edi/partners/"+uuid.NewString()+"/import-catalog",
			"ISA*00*00*ZZ*GABLELBM~GS*SC*GABLELBM*ACME*20260101~ST*832*0001~SE*3*0001~GE*1*1~IEA*1*000000001~")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
		}

		var summary struct {
			ParsedCount int `json:"parsed_count"`
			SavedCount  int `json:"saved_count"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if summary.ParsedCount != 0 || summary.SavedCount != 0 {
			t.Errorf("parsed=%d saved=%d, want 0 and 0", summary.ParsedCount, summary.SavedCount)
		}
	})
}

// CHARACTERIZATION: `format` is compared only against "csv", so every other
// value — including a typo like "X12", "xml" or "excel" — silently falls
// through to the X12 parser while the summary echoes the caller's spelling
// back. An 832 body survives that, as below. A CSV body sent as `?format=CSV`
// does not: it reaches the X12 branch and is now refused with 422 rather than
// reported as a successful import of nothing.
func TestImportCatalog_UnknownFormatFallsThroughToX12(t *testing.T) {
	for _, format := range []string{"xml", "X12", "CSV", "excel"} {
		t.Run(format, func(t *testing.T) {
			repo := newFakeEDIRepo()
			rec := doEDI(t, ediMux(repo), http.MethodPost,
				"/api/v1/edi/partners/"+uuid.NewString()+"/import-catalog?format="+format, sample832)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}

			var summary struct {
				Format      string `json:"format"`
				ParsedCount int    `json:"parsed_count"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if summary.Format != format {
				t.Errorf("format = %q, want the caller's %q echoed back", summary.Format, format)
			}
			if summary.ParsedCount != 2 {
				t.Errorf("parsed_count = %d, want 2 — the upload was parsed as X12 despite format=%q",
					summary.ParsedCount, format)
			}
		})
	}
}

// --- update / delete -----------------------------------------------------

// CORRECTNESS: the partner updated is the one named in the path, not one named
// in the body.
func TestUpdatePartner_PathIDBeatsTheBody(t *testing.T) {
	repo := newFakeEDIRepo()
	pathID, bodyID := uuid.New(), uuid.New()
	repo.partners[pathID] = TradingPartner{ID: pathID, Name: "ACME"}

	rec := doEDI(t, ediMux(repo), http.MethodPut, "/api/v1/edi/partners/"+pathID.String(),
		`{"id":"`+bodyID.String()+`","name":"ACME Renamed","edi_version":"005010"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(repo.updated) != 1 {
		t.Fatalf("UpdatePartner called %d times, want 1", len(repo.updated))
	}
	if repo.updated[0].ID != pathID {
		t.Errorf("updated %s, want the path id %s (the body's %s won)", repo.updated[0].ID, pathID, bodyID)
	}
	if repo.updated[0].Name != "ACME Renamed" || repo.updated[0].EDIVersion != "005010" {
		t.Errorf("update did not carry the submitted values: %+v", repo.updated[0])
	}
}

// CORRECTNESS: a PUT naming a partner that does not exist is a 404, not a 200
// echoing the submitted body back. The repository reports the condition with
// ErrPartnerNotFound (edi_repository.go) and the handler matches it with
// errors.Is — an operator editing a partner that was deleted in another tab has
// to be told the save did not happen.
func TestUpdatePartner_MissingPartnerIs404(t *testing.T) {
	repo := newFakeEDIRepo()
	repo.updateErr = ErrPartnerNotFound

	rec := doEDI(t, ediMux(repo), http.MethodPut, "/api/v1/edi/partners/"+uuid.NewString(),
		`{"name":"Ghost"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "NOT_FOUND" {
		t.Errorf("error code = %q, want NOT_FOUND", body.Error.Code)
	}
}

// CORRECTNESS: any OTHER repository failure on the update path is still a 500.
// Mapping every error to 404 would tell a client not to retry a transient
// database fault.
func TestUpdatePartner_UnclassifiedRepositoryFailureIs500(t *testing.T) {
	repo := newFakeEDIRepo()
	repo.updateErr = errors.New(`pq: relation "edi_trading_partners" does not exist`)

	rec := doEDI(t, ediMux(repo), http.MethodPut, "/api/v1/edi/partners/"+uuid.NewString(),
		`{"name":"ACME"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "edi_trading_partners") {
		t.Errorf("the database error reached the client: %s", rec.Body)
	}
}

// CHARACTERIZATION: unlike CreatePartner, the update path applies no defaults
// and no name check, so a PUT that omits a field writes its zero value. A UI
// that PATCHes by sending only the changed field would blank the partner's
// ISA/GS credentials and its EDI version.
func TestUpdatePartner_OmittedFieldsAreWrittenAsZeroValues(t *testing.T) {
	repo := newFakeEDIRepo()
	id := uuid.New()
	repo.partners[id] = TradingPartner{
		ID: id, Name: "ACME", EDIVersion: "005010", TransportType: "AS2",
		ISASenderID: "GABLELBM", SupportedDocuments: []string{"850"},
	}

	rec := doEDI(t, ediMux(repo), http.MethodPut, "/api/v1/edi/partners/"+id.String(), `{"name":"ACME"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := repo.updated[0]
	if got.EDIVersion != "" || got.TransportType != "" || got.ISASenderID != "" {
		t.Errorf("update = %+v, want the omitted fields written as zero values "+
			"(this test documents full-replace PUT semantics with no defaults)", got)
	}
}

// CORRECTNESS: a delete targets the path id and answers 204 with no body.
func TestDeletePartner_Is204AndTargetsThePathID(t *testing.T) {
	repo := newFakeEDIRepo()
	id := uuid.New()
	repo.partners[id] = TradingPartner{ID: id, Name: "ACME"}

	rec := doEDI(t, ediMux(repo), http.MethodDelete, "/api/v1/edi/partners/"+id.String(), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "" {
		t.Errorf("204 carried a body: %q", body)
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != id {
		t.Errorf("deleted %v, want the path id %s", repo.deleted, id)
	}
}

// CORRECTNESS: the seam is an interface and Postgres satisfies it.
func TestEDISeam_IsAnInterface(t *testing.T) {
	var _ EDIRepository = (*PostgresEDIRepository)(nil)
	if NewEDIHandler(newFakeEDIRepo(), newBG(), nil) == nil {
		t.Fatal("NewEDIHandler returned nil for a fake repository")
	}
}
