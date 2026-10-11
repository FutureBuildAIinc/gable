// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// Document numbers in record URLs (ADR 0007 section 7): every GET route
// whose path has {id} accepts the document number in that slot as well as
// the UUID, answering exactly the same body with no redirect. Writes take
// the UUID only.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestWire_ReadOrderByDocumentNumber(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	r := f.create()
	number, ok := r.body["number"].(string)
	if !ok || !strings.HasPrefix(number, "SO-") {
		t.Fatalf("create returned number %v, want an SO- number", r.body["number"])
	}
	id, ok := r.body["id"].(string)
	if !ok {
		t.Fatalf("create returned id %v", r.body["id"])
	}

	// The number spelling answers exactly what the UUID spelling does.
	byNumber := f.do("GET", "/api/v1/orders/"+number, nil)
	byID := f.do("GET", "/api/v1/orders/"+id, nil)
	if byNumber.status != http.StatusOK || byID.status != http.StatusOK {
		t.Fatalf("read by number = %d, by id = %d", byNumber.status, byID.status)
	}
	if string(byNumber.raw) != string(byID.raw) {
		t.Errorf("the number spelling (%s) and the UUID spelling answer different bodies:\n%s\n%s", number, byNumber.raw, byID.raw)
	}

	// A well formed number of another entity is a 400 naming id.
	bad := f.do("GET", "/api/v1/orders/Q-000123", nil)
	if bad.status != http.StatusBadRequest {
		t.Errorf("a quote number in the order id slot = %d, want 400", bad.status)
	} else if !strings.Contains(string(bad.raw), `"id"`) {
		t.Errorf("the 400 does not name id: %s", bad.raw)
	}

	// A number that names no visible row is a 404.
	missing := f.do("GET", "/api/v1/orders/SO-999999", nil)
	if missing.status != http.StatusNotFound {
		t.Errorf("an unseen number = %d, want 404", missing.status)
	}

	// Writes take the UUID only: a PUT spelled by number is a 400, never a
	// second fingerprint for one write.
	putByNumber := f.do("PUT", "/api/v1/orders/"+number, f.createBody())
	if putByNumber.status != http.StatusBadRequest {
		t.Errorf("PUT by number = %d, want 400 (writes take the UUID only)", putByNumber.status)
	}
}
