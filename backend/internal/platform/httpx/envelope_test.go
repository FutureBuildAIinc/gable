// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

type envelopeRow struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func decodeListBody(t *testing.T, w *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var env map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not a JSON object: %v (body: %s)", err, w.Body.String())
	}
	return env
}

// RULE (ADR 0001 §1): every list returns the one envelope: items,
// next_cursor, limit.
func TestWriteListShape(t *testing.T) {
	w := httptest.NewRecorder()
	next, err := MintCursor(testScope, "zzz", "aaa")
	if err != nil {
		t.Fatal(err)
	}
	WriteList(w, []envelopeRow{{ID: "1", Status: "draft"}, {ID: "2", Status: "sent"}}, next, 2)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	env := decodeListBody(t, w)
	if string(env["next_cursor"]) != `"`+next+`"` {
		t.Errorf("next_cursor = %s, want the minted cursor", env["next_cursor"])
	}
	if string(env["limit"]) != "2" {
		t.Errorf("limit = %s, want 2", env["limit"])
	}
	var items []envelopeRow
	if err := json.Unmarshal(env["items"], &items); err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 2 || items[0].ID != "1" || items[1].Status != "sent" {
		t.Errorf("items = %+v, want both rows", items)
	}
	if _, has := env["total"]; has {
		t.Errorf("total present without ?include=total: %s", env["total"])
	}
}

// RULE: items is never null. An empty or nil page serializes as [].
func TestWriteListItemsNeverNull(t *testing.T) {
	for _, items := range [][]envelopeRow{nil, {}} {
		w := httptest.NewRecorder()
		WriteList(w, items, "", DefaultPageLimit)
		env := decodeListBody(t, w)
		if string(env["items"]) != "[]" {
			t.Errorf("items = %s, want []", env["items"])
		}
	}
}

// RULE: next_cursor is null when the response has carried the final row.
func TestWriteListLastPageNullCursor(t *testing.T) {
	w := httptest.NewRecorder()
	WriteList(w, []envelopeRow{{ID: "1"}}, "", DefaultPageLimit)
	env := decodeListBody(t, w)
	if string(env["next_cursor"]) != "null" {
		t.Errorf("next_cursor = %s, want null", env["next_cursor"])
	}
}

// RULE: total appears only when the handler computed it (the ?include=total
// path), as an integer beside the other three fields.
func TestWriteListWithTotal(t *testing.T) {
	w := httptest.NewRecorder()
	WriteList(w, []envelopeRow{{ID: "1"}}, "", DefaultPageLimit, WithTotal(1234))
	env := decodeListBody(t, w)
	if string(env["total"]) != "1234" {
		t.Errorf("total = %s, want 1234", env["total"])
	}
	if string(env["limit"]) != "50" {
		t.Errorf("limit = %s, want the echoed limit", env["limit"])
	}
}
