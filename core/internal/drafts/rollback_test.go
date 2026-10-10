// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts_test

// The rollback rule for every draft write (ADR 0007 section 11): a failing
// audit write rolls the whole act back, so a create leaves no draft and no
// change row, a PUT leaves the payload and the revision exactly as they
// were, and a transition leaves the status alone. (The promotion's
// rollback, with a failing outbox write, is promotion_test.go's.)

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
)

// failingAuditor faults every audit row, to prove the rollback.
type failingAuditor struct{}

func (failingAuditor) Log(context.Context, audit.Entry) error {
	return errAuditFault{}
}

type errAuditFault struct{}

func (errAuditFault) Error() string { return "the audit write failed (a test fault)" }

// auditFaultFixture builds the standard fixture with a failing audit sink,
// in place before the handler is built.
func auditFaultFixture(t *testing.T) *fixture {
	return newFixtureWithAudit(t, testutil.RequireDB(t), failingAuditor{})
}

// TestFailedAuditWriteRollsBackCreate: nothing lands, not the draft, not
// its change row.
func TestFailedAuditWriteRollsBackCreate(t *testing.T) {
	f := auditFaultFixture(t)
	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()})
	if r.status != http.StatusInternalServerError {
		t.Fatalf("create with a failing audit write = %d: %s", r.status, r.raw)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM drafts WHERE module='quotes'`); n != 0 {
		t.Errorf("drafts after the failed create = %d, want none committed", n)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM draft_events`); n != 0 {
		t.Errorf("draft_events after the failed create = %d, want none committed", n)
	}
}

// TestFailedAuditWriteRollsBackPut: the payload and the revision are
// exactly as they were, and no change row landed.
func TestFailedAuditWriteRollsBackPut(t *testing.T) {
	db := testutil.RequireDB(t)
	seed := newFixture(t, db)
	id := seed.create()

	f := newFixtureWithAudit(t, db, failingAuditor{})
	r := f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload(), "revision": 1})
	if r.status != http.StatusInternalServerError {
		t.Fatalf("put with a failing audit write = %d: %s", r.status, r.raw)
	}
	got := seed.do("GET", "/api/v1/drafts/quotes/"+id, nil)
	if got.status != http.StatusOK || num(t, got.body, "revision") != 1 || str(t, got.body, "status") != "open" {
		t.Fatalf("the draft changed by the failed put: %s", got.raw)
	}
	if n := countRows(t, db, `SELECT count(*) FROM draft_events WHERE draft_id = $1`, id); n != 1 {
		t.Errorf("draft_events = %d, want only the create's row", n)
	}
}

// TestFailedAuditWriteRollsBackTransition: the status is unchanged and no
// change row landed.
func TestFailedAuditWriteRollsBackTransition(t *testing.T) {
	db := testutil.RequireDB(t)
	seed := newFixture(t, db)
	id := seed.create()

	f := newFixtureWithAudit(t, db, failingAuditor{})
	r := f.do("POST", "/api/v1/drafts/quotes/"+id+"/transitions", map[string]any{"to": "discarded", "revision": 1})
	if r.status != http.StatusInternalServerError {
		t.Fatalf("transition with a failing audit write = %d: %s", r.status, r.raw)
	}
	got := seed.do("GET", "/api/v1/drafts/quotes/"+id, nil)
	if got.status != http.StatusOK || str(t, got.body, "status") != "open" || num(t, got.body, "revision") != 1 {
		t.Fatalf("the draft changed by the failed transition: %s", got.raw)
	}
	if n := countRows(t, db, `SELECT count(*) FROM draft_events WHERE draft_id = $1`, id); n != 1 {
		t.Errorf("draft_events = %d, want only the create's row", n)
	}
}

// The seeded fixtures the rollback tests read through: the faulted service
// shares the database with the plain one, so the unchanged draft is read
// back on the wire that works.
