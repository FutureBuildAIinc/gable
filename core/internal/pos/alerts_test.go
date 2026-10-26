// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The customer's alerts at the counter (CP-11): the standing read states the
// alerts that speak at the counter, and a warning that asks to be
// acknowledged stops the sale until the clerk says so. The acknowledgement
// is recorded on the sale, naming the alert and the cashier.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// leaveAlert leaves an alert on the customer's account: the office's note or
// warning, the way the customer module leaves it.
func (f *fixture) leaveAlert(kind, text string, atCounter, atOrders, acknowledge bool) string {
	f.t.Helper()
	id := uuid.NewString()
	mustExec(f.t, f.db, `
		INSERT INTO customer_alerts (id, customer_id, kind, text, show_at_counter, show_at_orders,
			acknowledge_required, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'office')`,
		id, f.customerID, kind, text, atCounter, atOrders, acknowledge)
	return id
}

// RULE (CP-11): the standing read carries the customer's alerts that speak at
// the counter: the open ones marked for it, and nothing else.
func TestCP11_StandingCarriesCounterAlerts(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	noteID := f.leaveAlert("note", "Call before the delivery", true, false, false)
	f.leaveAlert("warning", "Needs a PO on every order", true, true, true)
	f.leaveAlert("note", "Only for the order desk", false, true, false)
	ended := f.leaveAlert("warning", "Called off", true, false, false)
	mustExec(f.t, f.db, `UPDATE customer_alerts SET ended_by = 'office', ended_at = NOW() WHERE id = $1`, ended)

	r := f.standing(f.customerID, "")
	if r.status != http.StatusOK {
		t.Fatalf("standing = %d: %s", r.status, r.raw)
	}
	alerts, ok := r.body["alerts"].([]any)
	if !ok {
		t.Fatalf("no alerts in the standing: %s", r.raw)
	}
	if len(alerts) != 2 {
		t.Fatalf("the standing carries %d alerts, want the note and the warning: %s", len(alerts), r.raw)
	}
	first, _ := alerts[0].(map[string]any)
	second, _ := alerts[1].(map[string]any)
	if first["id"] != noteID || first["kind"] != "note" || first["text"] != "Call before the delivery" {
		t.Errorf("the first alert = %v, want the note", first)
	}
	if first["acknowledge_required"] != false {
		t.Errorf("a note asks for no acknowledgement: %v", first)
	}
	if second["kind"] != "warning" || second["acknowledge_required"] != true {
		t.Errorf("the second alert = %v, want the warning that asks to be acknowledged", second)
	}

	// A customer with no alert carries an empty list, not a missing field.
	other := uuid.New()
	mustExec(f.t, f.db, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Quiet Co', $2, `+defaultBranch+`)`, other, "WPOS-"+uuid.NewString()[:8])
	quiet := f.standing(other, "")
	if quiet.status != http.StatusOK {
		t.Fatalf("standing of a customer with no alert = %d: %s", quiet.status, quiet.raw)
	}
	if list, ok := quiet.body["alerts"].([]any); !ok || len(list) != 0 {
		t.Errorf("a customer with no alert = %s, want an empty list", quiet.raw)
	}
}

// RULE (CP-11): a warning that asks to be acknowledged stops the sale until
// the clerk says so, and the acknowledgement is recorded on the sale, naming
// the alert and the cashier.
func TestCP11_AcknowledgementRecordedOnSale(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	alertID := f.leaveAlert("warning", "Needs a PO on every order", true, false, true)

	sale, total := f.accountCart("1", nil)
	stockBefore := f.stock()
	r := f.completeSale(sale, tender("account", total))
	code, blockers, _ := errorOf(t, r)
	if r.status != http.StatusConflict || code != "conflict" || len(blockers) != 1 || blockers[0] != "alert_unacknowledged" {
		t.Fatalf("a sale without the acknowledgement = %d: %s, want 409 alert_unacknowledged", r.status, r.raw)
	}
	if f.stock() != stockBefore || f.counterInvoices(t) != 0 {
		t.Errorf("a refused sale moved stock or wrote an invoice")
	}
	if st := f.saleStatus(t, sale); st != "OPEN" {
		t.Errorf("the refused sale is %s, want it open", st)
	}

	// The clerk says so, and the sale is taken.
	cur := f.getSale(f.t, sale)
	ok := f.do("POST", "/api/v1/pos/transactions/"+sale+"/complete",
		map[string]any{"tenders": []any{tender("account", total)}, "revision": num(f.t, cur, "revision"),
			"acknowledged_alert_ids": []string{alertID}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
	if ok.status != http.StatusOK {
		t.Fatalf("the acknowledged sale = %d: %s", ok.status, ok.raw)
	}

	// The acknowledgement is on the sale: one audit row naming the alert and
	// the cashier.
	var action, entityType, entityID string
	var changes map[string]any
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT action, entity_type, entity_id, changes FROM audit_log
		 WHERE action = 'pos.alert.acknowledged' AND entity_id = $1`, sale).
		Scan(&action, &entityType, &entityID, &changes); err != nil {
		t.Fatalf("the acknowledgement left no audit row on the sale: %v", err)
	}
	if entityType != "pos_transaction" {
		t.Errorf("the acknowledgement's entity = %q, want the sale", entityType)
	}
	if fmt.Sprint(changes["alert_id"]) != alertID {
		t.Errorf("the audit row does not name the alert: %v", changes)
	}
	if fmt.Sprint(changes["acknowledged_by"]) == "" {
		t.Errorf("the audit row does not name the cashier: %v", changes)
	}
}

// The refusal names the warning the clerk has to read.
func TestCP11_TheRefusalNamesTheWarning(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.leaveAlert("warning", "Needs a PO on every order", true, false, true)
	sale, total := f.accountCart("1", nil)
	r := f.completeSale(sale, tender("account", total))
	if !strings.Contains(string(r.raw), "Needs a PO on every order") {
		t.Errorf("the refusal %s does not name the warning", r.raw)
	}
}
