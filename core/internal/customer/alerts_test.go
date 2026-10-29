// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer_test

// The customer alerts on the wire (CP-11): the office leaves a customer a
// note or a warning, the counter reads it, and the warning that asks to be
// acknowledged stops the sale until the clerk says so. The tests state the
// wire facts: the guards the alerts stand behind, the audit row each write
// leaves, and the revision the end is taken on.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/customer/customeractor"
	"github.com/gablelbm/gable/internal/customer/customeraudit"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// RULE (CP-11): an alert is a customer edit, so it stands behind the same
// guard as the customer's other edits; it is audited as one, and its end is
// taken on its own revision.
func TestCP11_AlertAddListEnd(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	cid := str(t, f.create().body, "id")
	base := "/api/v1/customers/" + cid + "/alerts"
	asSales := []string{"X-Test-Role", "sales", "X-Test-Sub", "sub-sales"}
	// The signed-in user is granted the branch, so the customer is in reach
	// and the alert's author is the user that left it.
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`,
		"sub-sales", f.branch); err != nil {
		t.Fatalf("seed user grant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM user_locations WHERE user_sub = 'sub-sales'`)
	})

	// The guards: the three alert routes sit behind the customer's edit
	// guard, the one the customer's own writes use.
	seen := map[string]string{}
	record := func(label string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen[r.Method+" "+r.URL.Path] = label
				w.WriteHeader(http.StatusTeapot)
			})
		}
	}
	mux := http.NewServeMux()
	customer.NewHandler(customer.NewService(nil)).RegisterRoutes(mux, record("sales"), record("finance"))
	alertID := uuid.NewString()
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/customers/" + alertID + "/alerts"},
		{"POST", "/api/v1/customers/" + alertID + "/alerts"},
		{"POST", "/api/v1/customers/" + alertID + "/alerts/" + alertID + "/end"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))
		if rec.Code != http.StatusTeapot || seen[route.method+" "+route.path] != "sales" {
			t.Errorf("%s %s: status %d, guarded by %q, want the sales guard", route.method, route.path, rec.Code, seen[route.method+" "+route.path])
		}
	}

	// A note and a warning.
	note := f.do("POST", base, map[string]any{"kind": "note", "text": "Call before the delivery", "show_at_counter": true}, asSales...)
	if note.status != http.StatusCreated || note.header.Get("ETag") != `"1"` {
		t.Fatalf("add note = %d: %s", note.status, note.raw)
	}
	if note.body["kind"] != "note" || note.body["text"] != "Call before the delivery" || note.body["show_at_counter"] != true {
		t.Errorf("the note = %s", note.raw)
	}
	if note.body["show_at_orders"] != false || note.body["acknowledge_required"] != false {
		t.Errorf("the note speaks where it was told to: %s", note.raw)
	}
	if note.body["created_by"] != "sub-sales" {
		t.Errorf("the note's author = %v, want the signed-in user", note.body["created_by"])
	}
	if note.body["ended_at"] != nil || note.body["ended_by"] != nil || num(t, note.body, "revision") != 1 {
		t.Errorf("a new alert is open at revision 1: %s", note.raw)
	}

	warn := f.do("POST", base, map[string]any{
		"kind": "warning", "text": "Needs a PO on every order", "show_at_counter": true, "acknowledge_required": true,
	}, asSales...)
	if warn.status != http.StatusCreated || warn.body["acknowledge_required"] != true || warn.body["kind"] != "warning" {
		t.Fatalf("add warning = %d: %s", warn.status, warn.raw)
	}

	// A note that asks to be acknowledged is refused: only a warning may ask.
	askNote := f.do("POST", base, map[string]any{"kind": "note", "text": "Asking", "acknowledge_required": true}, asSales...)
	if askNote.status != http.StatusBadRequest {
		t.Errorf("a note asking to be acknowledged = %d, want 400", askNote.status)
	}
	if !hasField(detailsOf(t, askNote), "acknowledge_required") {
		t.Errorf("no detail names acknowledge_required: %s", askNote.raw)
	}
	// The text is bounded at 500 characters, and the kind is a fixed
	// vocabulary.
	if r := f.do("POST", base, map[string]any{"kind": "note", "text": strings.Repeat("a", 501)}, asSales...); r.status != http.StatusBadRequest {
		t.Errorf("an alert over 500 characters = %d, want 400", r.status)
	}
	if r := f.do("POST", base, map[string]any{"kind": "alarm", "text": "x"}, asSales...); r.status != http.StatusBadRequest {
		t.Errorf("an unknown kind = %d, want 400", r.status)
	}
	// An unknown customer is a 404.
	if r := f.do("POST", "/api/v1/customers/"+uuid.NewString()+"/alerts", map[string]any{"kind": "note", "text": "x"}, asSales...); r.status != http.StatusNotFound {
		t.Errorf("an alert on an unknown customer = %d, want 404", r.status)
	}

	// The list is the customer's, an envelope, newest first.
	list := f.do("GET", base+"?include=total", nil)
	if list.status != http.StatusOK || num(t, list.body, "total") != 2 || len(list.body["items"].([]any)) != 2 {
		t.Fatalf("list = %s", list.raw)
	}
	if other := str(t, f.create().body, "id"); !strings.Contains(string(f.do("GET", "/api/v1/customers/"+other+"/alerts", nil).raw), `"items":[]`) {
		t.Errorf("another customer's alerts are not an empty list")
	}

	// The end is on the alert's own revision: without one it is refused, with
	// a stale one it is refused, and with the right one the alert is ended.
	aid := str(t, note.body, "id")
	endPath := base + "/" + aid + "/end"
	if r := f.do("POST", endPath, nil, asSales...); r.status != http.StatusPreconditionRequired {
		t.Errorf("end without a revision = %d, want 428", r.status)
	}
	if r := f.do("POST", endPath, nil, append(asSales, "If-Match", `"9"`)...); r.status != http.StatusConflict {
		t.Errorf("end on a stale revision = %d, want 409", r.status)
	}
	end := f.do("POST", endPath, nil, append(asSales, "If-Match", `"1"`)...)
	if end.status != http.StatusOK || num(t, end.body, "revision") != 2 {
		t.Fatalf("end = %d: %s", end.status, end.raw)
	}
	if end.body["ended_at"] == nil || end.body["ended_by"] != "sub-sales" {
		t.Errorf("the ended alert says who called it off: %s", end.raw)
	}
	// The same end again is refused: the revision moved.
	if r := f.do("POST", endPath, nil, append(asSales, "If-Match", `"1"`)...); r.status != http.StatusConflict {
		t.Errorf("end of an ended alert = %d, want 409", r.status)
	}

	// The ended alert leaves the open list and joins the ended one.
	if open := f.do("GET", base+"?include=total", nil); num(t, open.body, "total") != 1 {
		t.Errorf("the open list = %s, want the warning only", open.raw)
	}
	if ended := f.do("GET", base+"?state=ended&include=total", nil); num(t, ended.body, "total") != 1 {
		t.Errorf("the ended list = %s, want the note", ended.raw)
	}
	if all := f.do("GET", base+"?state=all&include=total", nil); num(t, all.body, "total") != 2 {
		t.Errorf("the whole list = %s, want both alerts", all.raw)
	}

	// The audit: the add and the end each left one row naming the alert.
	actions, changes := f.auditRows(cid)
	if len(actions) != 3 || actions[0] != "customer.alert_added" || actions[2] != "customer.alert_ended" {
		t.Fatalf("audit rows = %v, want the adds and the end", actions)
	}
	if fmt.Sprint(changes[0]["alert_id"]) != str(t, note.body, "id") {
		t.Errorf("the audit row does not name the alert: %v", changes[0])
	}
	if fmt.Sprint(changes[2]["alert_id"]) != str(t, note.body, "id") {
		t.Errorf("the end's audit row does not name the alert: %v", changes[2])
	}
	if fmt.Sprint(changes[2]["ended_by"]) != "sub-sales" {
		t.Errorf("the end's audit row does not name who called it off: %v", changes[2])
	}

	// The event: customer.updated of part alert for each write.
	types, data := f.events(cid)
	var parts []string
	for i, ty := range types {
		if ty == "customer.updated" && data[i]["part"] == "alert" {
			parts = append(parts, fmt.Sprint(data[i]["action"]))
		}
	}
	if fmt.Sprint(parts) != "[added added ended]" {
		t.Errorf("alert events = %v", parts)
	}
}

// RULE (the lane rule on transactions): the end of an alert runs on its own
// connection, never reaching for a second one from the pool, and three racers
// on one alert leave exactly one winner: the end is taken on the alert's
// revision, so the losers are refused as stale.
func TestCP11_ConcurrentEndsLeaveOneWinner(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	prefix := "TXA-" + uuid.NewString()[:8] + "-"
	t.Cleanup(func() {
		ctx := context.Background()
		pat := prefix + "%"
		ids := `SELECT id FROM customers WHERE account_number LIKE $1`
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'customer' AND entity_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type = 'customer' AND entity_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_alerts WHERE customer_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE account_number LIKE $1`, pat)
	})
	svc := customer.NewService(customer.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(customeraudit.New(audit.NewLogger(db))).
		WithActorResolver(customeractor.New())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cust, err := svc.Create(ctx, &customer.Draft{AccountNumber: prefix + "1", Name: "Tx Alerts Co", IsActive: true})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	alert, err := svc.AddAlert(ctx, cust.ID, &customer.AlertDraft{
		Kind: customer.AlertWarning, Text: "Needs a PO on every order", ShowAtCounter: true, AcknowledgeRequired: true,
	})
	if err != nil {
		t.Fatalf("add alert: %v", err)
	}

	const contenders = 3
	var winners, stale int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.EndAlert(ctx, cust.ID, alert.ID, customer.Precondition{Revision: &alert.Revision})
			mu.Lock()
			defer mu.Unlock()
			var he *httpx.Error
			switch {
			case err == nil:
				winners++
			case errors.As(err, &he) && he.Status == http.StatusConflict:
				stale++
			default:
				t.Errorf("racer: unexpected error %v", err)
			}
		}()
	}
	wg.Wait()
	if winners != 1 || stale != contenders-1 {
		t.Errorf("winners=%d stale=%d, want exactly one winner and %d refused", winners, stale, contenders-1)
	}
	var ended int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM customer_alerts WHERE id = $1 AND ended_at IS NOT NULL AND revision = 2`, alert.ID).Scan(&ended); err != nil {
		t.Fatal(err)
	}
	if ended != 1 {
		t.Errorf("%d alerts ended, want one", ended)
	}
}
