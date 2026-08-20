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
// transport config) and ingests supplier catalogs. EDIHandler holds a concrete
// *EDIRepository, so only the paths that return before persistence are
// reachable here — see TestEDIRepository_IsNotUnitTestable at the end.
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

// KNOWN BUG. ImportCatalog files the wrong SKU as the vendor SKU.
// SupplierCatalogEntry carries both SKU (ours) and VendorSKU (theirs), and the
// CatalogEntry it is mapped into stores the partner's SKU so a vendor invoice
// or 855 acknowledgement can be matched back. The mapping copies item.SKU —
// our internal code — into CatalogEntry.VendorSKU on BOTH the CSV and the X12
// path, so the partner's own identifier is discarded.
//
// backend/internal/edi/edi_handler.go:186 (CSV) and edi_handler.go:202 (X12) —
//
//	entries = append(entries, CatalogEntry{
//	    VendorSKU: item.SKU,   // should be item.VendorSKU
//
// Compounding it, the X12 branch also drops MinOrderQty and PackQty, which the
// CSV branch does carry.
//
// This test body is intentionally empty: the mapping lives inside a handler
// that calls a concrete *EDIRepository, so it cannot be driven without
// Postgres. The parser half of the evidence is asserted in
// TestParseCSVCatalog_HeaderDrivenMapping, which shows SKU and VendorSKU are
// distinct values by the time the handler sees them.
func TestImportCatalog_FilesTheInternalSKUAsTheVendorSKU(t *testing.T) {
	t.Skip("KNOWN BUG: edi/edi_handler.go:186 and :202 copy item.SKU into CatalogEntry.VendorSKU, discarding the trading partner's own identifier")
}

// TestEDIRepository_IsNotUnitTestable documents a testability gap rather than
// behaviour.
//
// EDIHandler takes a concrete *EDIRepository holding a *database.DB, so the
// partner CRUD defaults applied in CreatePartner (EDIVersion 004010, transport
// SFTP, the default 832/846/850 document set) and the whole catalog persistence
// path require Postgres. Those defaults are the ones a fresh partner is created
// with, so they are worth covering once the seam exists.
func TestEDIRepository_IsNotUnitTestable(t *testing.T) {
	t.Skip("TESTABILITY GAP: edi/edi_handler.go:16-20 holds a concrete *EDIRepository, so CreatePartner's defaults and every catalog write need Postgres")
}
