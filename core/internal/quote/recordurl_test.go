// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// Document numbers in record URLs (ADR 0007 section 7): the quote GET
// accepts the Q- number in the {id} slot as well as the UUID, answering
// exactly the same body with no redirect. Writes take the UUID only.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestWire_ReadQuoteByDocumentNumber(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	r := f.create()
	number := str(t, r.body, "number")
	id := str(t, r.body, "id")
	if !strings.HasPrefix(number, "Q-") {
		t.Fatalf("create returned number %q, want a Q- number", number)
	}

	// The number spelling answers exactly what the UUID spelling does.
	byNumber := f.do("GET", "/api/v1/quotes/"+number, nil)
	byID := f.do("GET", "/api/v1/quotes/"+id, nil)
	if byNumber.status != http.StatusOK || byID.status != http.StatusOK {
		t.Fatalf("read by number = %d, by id = %d", byNumber.status, byID.status)
	}
	if string(byNumber.raw) != string(byID.raw) {
		t.Errorf("the number spelling (%s) and the UUID spelling answer different bodies:\n%s\n%s", number, byNumber.raw, byID.raw)
	}

	// A well formed number of another entity is a 400 naming id.
	bad := f.do("GET", "/api/v1/quotes/SO-000123", nil)
	if bad.status != http.StatusBadRequest {
		t.Errorf("an order number in the quote id slot = %d, want 400", bad.status)
	} else if !strings.Contains(string(bad.raw), `"id"`) {
		t.Errorf("the 400 does not name id: %s", bad.raw)
	}

	// A number that names no visible row is a 404.
	missing := f.do("GET", "/api/v1/quotes/Q-999999", nil)
	if missing.status != http.StatusNotFound {
		t.Errorf("an unseen number = %d, want 404", missing.status)
	}

	// Writes take the UUID only: a transition spelled by number is a 400,
	// never a second fingerprint for one write.
	byNumberWrite := f.do("POST", "/api/v1/quotes/"+number+"/transitions",
		map[string]any{"to": "sent", "revision": 1})
	if byNumberWrite.status != http.StatusBadRequest {
		t.Errorf("a write spelled by number = %d, want 400 (writes take the UUID only)", byNumberWrite.status)
	}
}
