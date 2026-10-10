// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts_test

// Agent identity (ADR 0007 section 6): every draft act writes one audit
// row carrying the request's actor, and the actor quadruple the wire and
// the feed carry is what pkg/actor resolved: an agent acting with a
// person's session is the person's subject with the acting_as marker and
// the tool name; a keyed writer is the key's id.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestEachActsAuditRowCarriesItsActor(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))

	// The person creates.
	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	id := str(t, r.body, "id")
	if by, _ := r.body["created_by"].(map[string]any); by == nil || by["kind"] != "anonymous" {
		t.Errorf("the create answer's created_by = %v (the dev fixture carries no claims)", r.body["created_by"])
	}

	// An agent with the person's session edits: the row carries the marker
	// and the tool.
	r = f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload(), "revision": 1},
		"X-Acting-As", "agent", "X-Agent-Tool", "quote-builder")
	if r.status != http.StatusOK {
		t.Fatalf("agent put = %d: %s", r.status, r.raw)
	}
	if by, _ := r.body["updated_by"].(map[string]any); by["kind"] != "agent" || by["acting_as"] != "agent" || by["tool"] != "quote-builder" {
		t.Errorf("the put answer's updated_by = %v, want the agent with its marker and tool", r.body["updated_by"])
	}

	// The person discards, and reopens.
	if r = f.do("POST", "/api/v1/drafts/quotes/"+id+"/transitions", map[string]any{"to": "discarded", "revision": 2}); r.status != http.StatusOK {
		t.Fatalf("discard = %d: %s", r.status, r.raw)
	}
	if r = f.do("POST", "/api/v1/drafts/quotes/"+id+"/transitions", map[string]any{"to": "open", "revision": 3}); r.status != http.StatusOK {
		t.Fatalf("reopen = %d: %s", r.status, r.raw)
	}

	// Each act's audit row, in order, with its actor.
	rows := f.auditRows(id)
	want := []struct {
		action string
		kind   string
		tool   string
	}{
		{"draft.created", "anonymous", ""},
		{"draft.updated", "agent", "quote-builder"},
		{"draft.discarded", "anonymous", ""},
		{"draft.reopened", "anonymous", ""},
	}
	if len(rows) != len(want) {
		t.Fatalf("audit rows = %d, want %d: %v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i]["action"] != w.action {
			t.Errorf("row %d action = %v, want %s", i, rows[i]["action"], w.action)
			continue
		}
		if rows[i]["actor_kind"] != w.kind {
			t.Errorf("row %d (%s) actor_kind = %v, want %s", i, w.action, rows[i]["actor_kind"], w.kind)
		}
		if w.tool != "" && rows[i]["tool"] != w.tool {
			t.Errorf("row %d (%s) tool = %v, want %s", i, w.action, rows[i]["tool"], w.tool)
		}
		if w.tool == "" && rows[i]["tool"] != nil && rows[i]["tool"] != "" {
			t.Errorf("row %d (%s) tool = %v, want none", i, w.action, rows[i]["tool"])
		}
	}
	// The updated row's changes carry the module, the revision and the
	// payload hash.
	ch, _ := rows[1]["changes"].(map[string]any)
	if ch["module"] != "quotes" || ch["revision"] == nil || ch["payload_sha256"] == nil {
		t.Errorf("draft.updated changes = %v, want module, revision and payload_sha256", ch)
	}
}
