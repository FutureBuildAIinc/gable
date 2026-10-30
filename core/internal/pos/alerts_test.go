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
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/customer/customeractor"
	"github.com/gablelbm/gable/internal/customer/customeraudit"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// withCustomers mounts the customer routes on the fixture's mux, wired the
// way serve wires them, so a test can drive the real alert routes alongside
// the counter routes.
func withCustomers() func(*fixture) {
	return func(f *fixture) {
		f.mounts = append(f.mounts, func(f *fixture, mux *http.ServeMux) {
			svc := customer.NewService(customer.NewRepository(f.db)).
				WithOutbox(f.events).
				WithTxRunner(f.db).
				WithAudit(customeraudit.New(audit.NewLogger(f.db))).
				WithActorResolver(customeractor.New())
			customer.NewHandler(svc).RegisterRoutes(mux)
		})
	}
}

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
	mustExec(f.t, f.db, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Quiet Co', $2, $3)`, other, "WPOS-"+uuid.NewString()[:8], f.branchID)
	t.Cleanup(func() { mustExec(f.t, f.db, `DELETE FROM customers WHERE id = $1`, other) })
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

// RULE (CP-11, P2-1): an exchange for a customer with an acknowledge-required
// counter warning is refused without the acknowledgement and succeeds when the
// exchange request names the acknowledged alert, recording the acknowledgement
// audit row on the replacement sale.
func TestCP11_ExchangeCarriesAlertAcknowledgement(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, total := f.accountCart("2", nil)
	if r := f.completeSale(sale, tender("account", total)); r.status != http.StatusOK {
		t.Fatalf("complete original sale = %d: %s", r.status, r.raw)
	}
	cur := f.getSale(t, sale)
	line := str(t, cur["lines"].([]any)[0].(map[string]any), "id")

	alertID := f.leaveAlert("warning", "Needs a PO on every order", true, false, true)
	stockBefore := f.stock()
	invBefore := f.counterInvoices(t)

	// Without the acknowledgement, the exchange is refused 409 alert_unacknowledged
	// and nothing is written.
	refused := f.exchange("", map[string]any{
		"original_sale_id": sale, "reason": "wrong size",
		"returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines":     f.replaceWith("2"),
		"tenders":        []map[string]any{tender("cash", 599)},
	})
	code, blockers, _ := errorOf(t, refused)
	if refused.status != http.StatusConflict || code != "conflict" || len(blockers) != 1 || blockers[0] != "alert_unacknowledged" {
		t.Fatalf("an exchange without the acknowledgement = %d: %s, want 409 alert_unacknowledged", refused.status, refused.raw)
	}
	if f.stock() != stockBefore || f.counterInvoices(t) != invBefore {
		t.Errorf("a refused exchange moved stock or wrote an invoice")
	}

	// With the acknowledgement named on the exchange, the exchange completes and
	// records the acknowledgement on the replacement sale.
	ok := f.exchange("", map[string]any{
		"original_sale_id":       sale,
		"reason":                 "wrong size",
		"returned_lines":         []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines":             f.replaceWith("2"),
		"tenders":                []map[string]any{tender("cash", 599)},
		"acknowledged_alert_ids": []string{alertID},
	})
	if ok.status != http.StatusCreated {
		t.Fatalf("the acknowledged exchange = %d: %s, want 201", ok.status, ok.raw)
	}
	newSale, _ := ok.body["sale"].(map[string]any)
	newSaleID := str(t, newSale, "id")
	var changes map[string]any
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT changes FROM audit_log WHERE action = 'pos.alert.acknowledged' AND entity_id = $1`, newSaleID).
		Scan(&changes); err != nil {
		t.Fatalf("the acknowledged exchange left no audit row on the replacement sale: %v", err)
	}
	if fmt.Sprint(changes["alert_id"]) != alertID {
		t.Errorf("the exchange acknowledgement audit row = %v, want alert_id %s", changes, alertID)
	}
}

// RULE (CP-11, P2-2): a warning added through the real customer alert route
// while sales complete at the counter (pool size 4, three contenders) shares
// the customer's step 1a serialization lock with the completion's alert check,
// even on cash sales, so an acknowledge-required warning cannot commit inside
// a completion's window between its alert check and its commit.
func TestCP11_AlertAddedWhileSalesComplete(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), withCustomers())
	f.openTill(0)
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM audit_log WHERE entity_id = $1`, f.customerID)
	})

	sales := make([]string, 3)
	for i := range sales {
		sales[i] = f.startSale(&f.customerID)
		if r := f.addLine(sales[i], f.productLine("1")); r.status != http.StatusOK {
			t.Fatalf("add line = %d: %s", r.status, r.raw)
		}
	}

	// A separate test connection holds the product's inventory row (step 6),
	// so the first completion stops at stock out: after it has checked the
	// customer's counter alerts, before it commits.
	stall, err := database.Connect(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stall.Close() })
	stallTx, err := stall.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stallTx.Rollback(context.Background())
	var stallXID string
	if err := stallTx.QueryRow(context.Background(), `
		SELECT pg_current_xact_id()::text FROM inventory WHERE product_id = $1 AND location_id = $2
		FOR UPDATE OF inventory`, f.productID, f.yardID).Scan(&stallXID); err != nil {
		t.Fatalf("hold the inventory row: %v", err)
	}

	ctx := context.Background()
	statuses := make([]int, 3)
	var wg sync.WaitGroup
	for i := range sales {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.completeSale(sales[i], tender("cash", 599)).status
		}(i)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := f.db.Pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_locks WHERE NOT granted AND transactionid = $1`, stallXID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := f.scalar(`SELECT count(*) FROM pg_locks WHERE NOT granted AND transactionid = $1`, stallXID).(int64); n == 0 {
		t.Fatal("no completion ever reached its stock out: the stall proved nothing")
	}

	// The office posts an acknowledge-required counter warning through the
	// real customer alert route. It must wait on the customer's step 1a lock
	// and never commit while a sale completion's transaction is open.
	added := make(chan resp, 1)
	go func() {
		added <- f.do("POST", "/api/v1/customers/"+f.customerID.String()+"/alerts",
			map[string]any{
				"kind":                 "warning",
				"text":                 "Verify job PO at counter",
				"show_at_counter":      true,
				"show_at_orders":       false,
				"acknowledge_required": true,
			},
			"X-Test-Role", "admin", "X-Test-Sub", mustUUID(f.t))
	}()
	select {
	case r := <-added:
		t.Fatalf("the alert route answered %d while a sale completion was open in its window: %s", r.status, r.raw)
	case <-time.After(1500 * time.Millisecond):
		// still waiting on the customer's step 1a lock held by the completion
	}

	if err := stallTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	alertResp := <-added
	if alertResp.status != http.StatusCreated {
		t.Fatalf("add alert = %d: %s, want 201", alertResp.status, alertResp.raw)
	}
	alertID := str(t, alertResp.body, "id")
	var alertCreatedAt time.Time
	if err := f.db.Pool.QueryRow(ctx, `SELECT created_at FROM customer_alerts WHERE id = $1`, alertID).Scan(&alertCreatedAt); err != nil {
		t.Fatal(err)
	}

	// Every contender either completed before the alert committed (200) or ran
	// after the alert committed and was refused 409 alert_unacknowledged.
	for i, st := range statuses {
		if st != http.StatusOK && st != http.StatusConflict {
			t.Errorf("sale %d = %d, want 200 or 409", i, st)
			continue
		}
		if st == http.StatusOK {
			var completedAt time.Time
			if err := f.db.Pool.QueryRow(ctx, `SELECT completed_at FROM pos_transactions WHERE id = $1`, sales[i]).Scan(&completedAt); err != nil {
				t.Fatal(err)
			}
			if !completedAt.Before(alertCreatedAt) {
				t.Errorf("sale %s completed at %s, not before the alert at %s", sales[i], completedAt, alertCreatedAt)
			}
		}
	}

	// A sale after the warning is refused without acknowledgement.
	next := f.startSale(&f.customerID)
	if r := f.addLine(next, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(next, tender("cash", 599)); r.status != http.StatusConflict {
		t.Errorf("a sale after the warning = %d, want 409: %s", r.status, r.raw)
	}
}
