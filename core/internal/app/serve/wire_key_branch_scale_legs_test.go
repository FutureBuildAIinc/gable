// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The scale link's WALLED legs (CP-19, ADR 0007 section 5.5): a key pinned
// to branch A drives the seven scale routes against the foreign twin and
// finds a missing row or a refused write, never branch B's device, ticket,
// sale or line; the pin's own register keeps its reach. The marks are the
// coverage the route allowlist names.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestKeyBranchPin_ScaleRoutesHoldThePin(t *testing.T) {
	f := newKeyPinFixture(t)
	f.record()
	defer driverDone(t, "scale")
	ctx := context.Background()

	// Branch B's own scale and an open weigh in ticket: the pin must never
	// name either, whatever the route.
	devB, ticketB := uuid.New(), uuid.New()
	mustExecK(t, f, `INSERT INTO scale_devices (id, branch_id, name, protocol, endpoint, certificate_no, location_note)
		VALUES ($1, $2, 'Foreign yard scale', 'SMA', 'sim://foreign', 'COC-B', 'the foreign bridge')`, devB, f.branchB)
	mustExecK(t, f, `INSERT INTO scale_tickets (id, branch_id, direction, status, source, captured_by, vehicle_ref)
		VALUES ($1, $2, 'WEIGH_IN', 'OPEN', 'SCALE', 'weigher-b', 'FOREIGN-TRUCK')`, ticketB, f.branchB)
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM scale_tickets WHERE id = $1`, ticketB)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM scale_devices WHERE id = $1`, devB)
	})
	ticketsAtB := func() int {
		t.Helper()
		var n int
		if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM scale_tickets WHERE branch_id = $1`, f.branchB).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := ticketsAtB()

	// The foreign register's scale status answers a missing row, never the
	// foreign device; the pin's own register answers its own.
	code, buf := f.key(t, f.bound, "GET", "/api/v1/pos/scale?register_id="+f.regB, "", nil)
	f.twinLeg("GET", "/api/v1/pos/scale")
	if code == http.StatusOK || strings.Contains(string(buf), devB.String()) {
		t.Errorf("bound key scale status of the foreign register = %d: %s", code, buf)
	}
	f.allow(t, "bound", "GET", "/api/v1/pos/scale?register_id="+f.regA, "", nil, http.StatusOK)

	// The ticket list through the foreign register names none of B's.
	_, buf = f.key(t, f.bound, "GET", "/api/v1/pos/scale/tickets?register_id="+f.regB, "", nil)
	f.twinLeg("GET", "/api/v1/pos/scale/tickets")
	if strings.Contains(string(buf), ticketB.String()) || strings.Contains(string(buf), "FOREIGN-TRUCK") {
		t.Errorf("bound key scale tickets of the foreign register named B's: %s", buf)
	}

	// By id and by void, B's ticket is a missing row and stays open.
	code, buf = f.key(t, f.bound, "GET", "/api/v1/pos/scale/tickets/"+ticketB.String(), "", nil)
	f.twinLeg("GET", "/api/v1/pos/scale/tickets/{id}")
	if code != http.StatusNotFound || strings.Contains(string(buf), ticketB.String()) {
		t.Errorf("bound key read of B's ticket = %d: %s", code, buf)
	}
	code, _ = f.key(t, f.bound, "POST", "/api/v1/pos/scale/tickets/"+ticketB.String()+"/void",
		`{"reason":"out of my branch","revision":1}`, nil)
	f.twinLeg("POST", "/api/v1/pos/scale/tickets/{id}/void")
	if code != http.StatusNotFound {
		t.Errorf("bound key void of B's ticket = %d, want 404", code)
	}
	var status string
	if err := f.db.Pool.QueryRow(ctx, `SELECT status FROM scale_tickets WHERE id = $1`, ticketB).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "OPEN" {
		t.Errorf("the refused void moved B's ticket: %s", status)
	}

	// A weigh in through the foreign register is refused and writes no
	// ticket; a weigh out and a manual weight against B's sale and line are
	// refused the same way.
	code, buf = f.key(t, f.bound, "POST", "/api/v1/pos/scale/weigh-in",
		fmt.Sprintf(`{"register_id":%q,"vehicle_ref":"FOREIGN-NEW"}`, f.regB), nil)
	f.twinLeg("POST", "/api/v1/pos/scale/weigh-in")
	if code == http.StatusCreated {
		t.Errorf("bound key weigh in on the foreign register created a ticket: %s", buf)
	}
	code, buf = f.key(t, f.bound, "POST", "/api/v1/pos/scale/weigh-out",
		fmt.Sprintf(`{"sale_id":%q,"line_id":%q,"vehicle_ref":"FOREIGN-TRUCK","revision":1}`, f.txnB, f.lineB), nil)
	f.twinLeg("POST", "/api/v1/pos/scale/weigh-out")
	if code == http.StatusOK {
		t.Errorf("bound key weigh out against B's sale landed: %s", buf)
	}
	code, buf = f.key(t, f.bound, "POST",
		fmt.Sprintf("/api/v1/pos/transactions/%s/items/%s/weight", f.txnB, f.lineB),
		`{"weight_lb":"1200","reason":"scale_offline","revision":1}`, nil)
	f.twinLeg("POST", "/api/v1/pos/transactions/{id}/items/{itemId}/weight")
	if code == http.StatusOK {
		t.Errorf("bound key manual weight on B's line landed: %s", buf)
	}
	if after := ticketsAtB(); after != before {
		t.Errorf("the refused scale writes left %d tickets at B, want %d", after, before)
	}
}

func mustExecK(t *testing.T, f *keyPinFixture, sql string, args ...any) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}
