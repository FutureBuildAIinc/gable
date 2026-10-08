// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package edi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The EDI admin API manages trading-partner credentials (ISA/GS identifiers,
// transport config) and ingests supplier catalogs. EDIHandler takes the
// EDIRepository interface declared in edi_repository.go, so this file covers
// routing, request-shape rejection and the pure mapping helpers, while
// edi_handler_repo_test.go drives the full CRUD and ingest paths against a fake
// store and edi_repository_pg_test.go covers the SQL.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

func newEDIMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	NewEDIHandler(nil, newBG(), nil).RegisterRoutes(mux)
	return mux
}

func doEDI(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// CORRECTNESS: a malformed partner id is a client error on every partner-scoped
// route, caught before the repository is touched.
func TestEDIHandler_MalformedPartnerIDIs400(t *testing.T) {
	mux := newEDIMux(t)

	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/edi/partners/not-a-uuid", ""},
		{http.MethodPut, "/api/v1/edi/partners/not-a-uuid", `{"name":"X"}`},
		{http.MethodDelete, "/api/v1/edi/partners/not-a-uuid", ""},
		{http.MethodPost, "/api/v1/edi/partners/not-a-uuid/import-catalog", "sku\nA\n"},
		{http.MethodGet, "/api/v1/edi/partners/not-a-uuid/catalog", ""},
	}

	for _, tc := range cases {
		rec := doEDI(t, mux, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400", tc.method, tc.path, rec.Code)
		}
	}
}

// CORRECTNESS: a trading partner without a name cannot be identified in the
// admin UI or matched to a vendor, so it must be refused before insert.
func TestEDIHandler_CreatePartnerRequiresAName(t *testing.T) {
	mux := newEDIMux(t)

	for _, body := range []string{`{}`, `{"name":""}`, `{"isa_sender_id":"GABLELBM"}`} {
		rec := doEDI(t, mux, http.MethodPost, "/api/v1/edi/partners", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s => %d, want 400", body, rec.Code)
		}
	}
}

func TestEDIHandler_CreatePartnerMalformedBodyIs400(t *testing.T) {
	rec := doEDI(t, newEDIMux(t), http.MethodPost, "/api/v1/edi/partners", "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// CORRECTNESS: an unusable CSV upload must be reported as an unprocessable
// entity, not silently imported as zero rows — an operator who uploads the
// wrong file needs to be told.
func TestEDIHandler_ImportCatalogRejectsAnUnparseableCSV(t *testing.T) {
	mux := newEDIMux(t)
	partner := uuid.NewString()

	rec := doEDI(t, mux, http.MethodPost,
		"/api/v1/edi/partners/"+partner+"/import-catalog?format=csv", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for a CSV with no header row (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "UNPROCESSABLE_ENTITY" {
		t.Errorf("error code = %q, want UNPROCESSABLE_ENTITY", body.Error.Code)
	}
}

// CORRECTNESS: the role guard supplied at registration must wrap every route.
// These endpoints expose trading-partner credentials and accept file uploads.
func TestEDIHandler_RoleGuardWrapsEveryEndpoint(t *testing.T) {
	var hits int
	guard := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits++
			w.WriteHeader(http.StatusForbidden)
		})
	}
	mux := http.NewServeMux()
	NewEDIHandler(nil, newBG(), nil).RegisterRoutes(mux, guard)

	id := uuid.NewString()
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/edi/partners"},
		{http.MethodPost, "/api/v1/edi/partners"},
		{http.MethodGet, "/api/v1/edi/partners/" + id},
		{http.MethodPut, "/api/v1/edi/partners/" + id},
		{http.MethodDelete, "/api/v1/edi/partners/" + id},
		{http.MethodPost, "/api/v1/edi/partners/" + id + "/import-catalog"},
		{http.MethodGet, "/api/v1/edi/partners/" + id + "/catalog"},
	}
	for _, r := range routes {
		rec := doEDI(t, mux, r.method, r.path, "{}")
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403: the role guard did not wrap this route", r.method, r.path, rec.Code)
		}
	}
	if hits != len(routes) {
		t.Errorf("guard ran %d times, want %d", hits, len(routes))
	}
}

// --- trading-partner JSON contract --------------------------------------

// CORRECTNESS: the ISA/GS identifiers are the addressing envelope of every
// outbound document. Their JSON names are the admin UI's contract and a rename
// would silently blank a configured partner's credentials on the next save.
func TestTradingPartnerJSON_FieldNames(t *testing.T) {
	b, err := json.Marshal(TradingPartner{
		ID: uuid.New(), Name: "ACME Buying Group",
		ISASenderID: "GABLELBM", ISASenderQualifier: "ZZ",
		ISAReceiverID: "ACMEBG", ISAReceiverQualifier: "01",
		GSSenderID: "GABLELBM", GSReceiverID: "ACMEBG",
		EDIVersion: "004010", TransportType: "SFTP", TransportConfig: `{"host":"sftp.example"}`,
		SupportedDocuments: []string{"832", "846", "850"}, IsActive: true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, field := range []string{
		"isa_sender_id", "isa_sender_qualifier", "isa_receiver_id", "isa_receiver_qualifier",
		"gs_sender_id", "gs_receiver_id", "edi_version", "transport_type", "transport_config",
		"supported_documents", "is_active",
	} {
		if _, ok := raw[field]; !ok {
			t.Errorf("the payload is missing %q: %s", field, b)
		}
	}
	if got := string(raw["supported_documents"]); got != `["832","846","850"]` {
		t.Errorf("supported_documents = %s, want a JSON array of document type codes", got)
	}
}

// CORRECTNESS: ImportCatalog must file the PARTNER's SKU as the vendor SKU.
// SupplierCatalogEntry carries both SKU (ours) and VendorSKU (theirs), and the
// CatalogEntry it is mapped into stores the partner's SKU so a vendor invoice
// or 855 acknowledgement can be matched back. Copying item.SKU — our internal
// code — into CatalogEntry.VendorSKU discards the partner's own identifier.
// MinOrderQty and PackQty must survive the mapping on both the CSV and the X12
// source; the X12 branch used to drop them.
//
// This exercises the mapping in isolation, through toCatalogEntries — the pure
// function the handler delegates to. The same rule is asserted end to end,
// through the HTTP handler and into persistence, by
// TestImportCatalog_X12PersistsAgainstThePathPartner in
// edi_handler_repo_test.go.
func TestToCatalogEntries_FilesThePartnerSKUAsTheVendorSKU(t *testing.T) {
	got := toCatalogEntries([]SupplierCatalogEntry{{
		VendorName:  "ACME",
		SKU:         "2X4-8",  // ours
		VendorSKU:   "AL-2X4", // theirs
		Description: "2x4-8 SPF Stud",
		UnitPrice:   4.75,
		UOM:         "EA",
		MinOrderQty: 10,
		PackSize:    294,
	}})

	if len(got) != 1 {
		t.Fatalf("mapped %d entries, want 1", len(got))
	}
	e := got[0]
	if e.VendorSKU != "AL-2X4" {
		t.Errorf("VendorSKU = %q, want the trading partner's own identifier AL-2X4", e.VendorSKU)
	}
	if e.Description != "2x4-8 SPF Stud" {
		t.Errorf("Description = %q", e.Description)
	}
	if e.UnitCost != 4.75 {
		t.Errorf("UnitCost = %v, want 4.75", e.UnitCost)
	}
	if e.UOM != "EA" {
		t.Errorf("UOM = %q, want EA", e.UOM)
	}
	if e.MinOrderQty != 10 {
		t.Errorf("MinOrderQty = %v, want 10", e.MinOrderQty)
	}
	if e.PackQty != 294 {
		t.Errorf("PackQty = %v, want 294", e.PackQty)
	}
}

// CORRECTNESS: the same mapping is used for both upload formats, so an 832 and
// a CSV describing the same item must persist the same row. The X12 branch
// previously had its own copy that dropped MinOrderQty and PackQty.
func TestImportCatalog_X12AndCSVMapIdentically(t *testing.T) {
	bg := newBG()

	x12Items, err := bg.Parse832Catalog("N1*SU*ACME~LIN*1*VP*AL-2X4*SK*2X4-8~PID*F****2x4-8 SPF Stud~CTP*RS*RES*4.75*1*EA~")
	if err != nil {
		t.Fatalf("Parse832Catalog: %v", err)
	}
	csvItems, err := bg.ParseCSVCatalog(
		"vendor_sku,sku,description,unit_price,uom\nAL-2X4,2X4-8,2x4-8 SPF Stud,4.75,EA\n", "ACME")
	if err != nil {
		t.Fatalf("ParseCSVCatalog: %v", err)
	}

	fromX12, fromCSV := toCatalogEntries(x12Items), toCatalogEntries(csvItems)
	if len(fromX12) != 1 || len(fromCSV) != 1 {
		t.Fatalf("mapped %d X12 and %d CSV entries, want 1 each", len(fromX12), len(fromCSV))
	}
	if fromX12[0] != fromCSV[0] {
		t.Errorf("the two upload formats produced different rows:\n x12: %+v\n csv: %+v", fromX12[0], fromCSV[0])
	}
	if fromX12[0].VendorSKU != "AL-2X4" {
		t.Errorf("VendorSKU = %q, want AL-2X4", fromX12[0].VendorSKU)
	}
	if fromX12[0].MinOrderQty != 1 || fromX12[0].PackQty != 1 {
		t.Errorf("MinOrderQty=%v PackQty=%v, want the parser defaults of 1", fromX12[0].MinOrderQty, fromX12[0].PackQty)
	}
}
