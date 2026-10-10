// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

// Document numbers in record URLs (ADR 0007 section 7): the invoice GET
// accepts the IN- number in the {id} slot as well as the UUID, answering
// exactly the same body with no redirect. Writes take the UUID only.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestWire_ReadInvoiceByDocumentNumber(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	invoiceID, _ := f.invoice("10")
	head := f.getInvoice(invoiceID)
	number, ok := head.body["number"].(string)
	if !ok || !strings.HasPrefix(number, "IN-") {
		t.Fatalf("the invoice carries number %v, want an IN- number", head.body["number"])
	}

	// The number spelling answers exactly what the UUID spelling does.
	byNumber := f.do("GET", "/api/v1/invoices/"+number, nil)
	if byNumber.status != http.StatusOK {
		t.Fatalf("read by number = %d: %s", byNumber.status, byNumber.raw)
	}
	if string(byNumber.raw) != string(head.raw) {
		t.Errorf("the number spelling (%s) and the UUID spelling answer different bodies:\n%s\n%s", number, byNumber.raw, head.raw)
	}

	// A well formed number of another entity is a 400 naming id.
	bad := f.do("GET", "/api/v1/invoices/SO-000123", nil)
	if bad.status != http.StatusBadRequest {
		t.Errorf("an order number in the invoice id slot = %d, want 400", bad.status)
	} else if !strings.Contains(string(bad.raw), `"id"`) {
		t.Errorf("the 400 does not name id: %s", bad.raw)
	}

	// A number that names no visible row is a 404.
	missing := f.do("GET", "/api/v1/invoices/IN-999999", nil)
	if missing.status != http.StatusNotFound {
		t.Errorf("an unseen number = %d, want 404", missing.status)
	}

	// Writes take the UUID only: a void spelled by number is a 400, never a
	// second fingerprint for one write.
	voidByNumber := f.do("POST", "/api/v1/invoices/"+number+"/transitions",
		map[string]any{"to": "void", "revision": 1, "reason": "spelled by number"})
	if voidByNumber.status != http.StatusBadRequest {
		t.Errorf("a write spelled by number = %d, want 400 (writes take the UUID only)", voidByNumber.status)
	}
}
