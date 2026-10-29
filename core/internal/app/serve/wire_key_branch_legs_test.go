// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The coverage half of the WALLED claim (ADR 0007 section 5.5): the tests
// that drive walled routes with a key bound to branch A mark the legs that
// drive them against the foreign twin (or the reach and list legs that prove
// the pin's confinement), and TestKeyBranchPin_ZWalledRoutesAllHaveALeg holds
// the route allowlist against the marks, so a WALLED entry no foreign twin leg
// drives fails by name. An A control alone covers nothing (round 10 P3-6):
// the marks are explicit, on the driving leg, never on the control. The
// recorder is per process: the coverage test runs after the drivers (file
// order) and skips when a -run filter kept one of them from running.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// walledLegDrivers are the tests whose legs the coverage test needs.
var walledLegDrivers = []string{"sweep", "pos", "body", "catalog", "scale"}

var walledLegs = struct {
	sync.Mutex
	seen    map[string]bool
	drivers map[string]bool
}{seen: map[string]bool{}, drivers: map[string]bool{}}

// record makes the fixture count the twin legs its drivers mark, from here on.
func (f *keyPinFixture) record() { f.recording = true }

// twinLeg marks one route as driven by a foreign twin leg (or a reach or list
// leg that proves the pin holds), for the coverage test. It is the driver's
// explicit call on its driving leg, never the A control.
func (f *keyPinFixture) twinLeg(method, path string) {
	if !f.recording {
		return
	}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	walledLegs.Lock()
	walledLegs.seen[method+" "+path] = true
	walledLegs.Unlock()
}

// driverDone notes that a driving test ran to its end.
func driverDone(t *testing.T, name string) {
	t.Helper()
	if t.Failed() {
		return
	}
	walledLegs.Lock()
	walledLegs.drivers[name] = true
	walledLegs.Unlock()
}

// twinState is a digest, per table, of every row that names branch B's twin
// rows by id: the rows themselves and the rows that point at them. A refused
// leg that wrote anywhere B owns or is named by shows as a changed digest,
// whatever other packages do to the same tables meanwhile.
func (f *keyPinFixture) twinState(t *testing.T, w *walledTwins) map[string]string {
	t.Helper()
	q := func(id uuid.UUID) string { return "'" + id.String() + "'" }
	custB, branchB := q(f.custB), q(f.branchB)
	reg := "'" + f.regB + "'"
	rows := map[string]string{
		"orders":          `SELECT * FROM orders WHERE id = ` + q(w.ordB) + ` OR customer_id = ` + custB + ` OR branch_id = ` + branchB,
		"order_lines":     `SELECT * FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE id = ` + q(w.ordB) + ` OR customer_id = ` + custB + ` OR branch_id = ` + branchB + `)`,
		"invoices":        `SELECT * FROM invoices WHERE id = ` + q(w.invB) + ` OR customer_id = ` + custB + ` OR branch_id = ` + branchB,
		"credit_memos":    `SELECT * FROM credit_memos WHERE id = ` + q(w.memoB) + ` OR customer_id = ` + custB + ` OR branch_id = ` + branchB,
		"payments":        `SELECT * FROM payments WHERE id = ` + q(w.payB) + ` OR customer_id = ` + custB + ` OR branch_id = ` + branchB,
		"payment_refunds": `SELECT * FROM payment_refunds WHERE payment_id = ` + q(w.payB) + ` OR credit_memo_id = ` + q(w.memoB),
		"ar_applications": `SELECT * FROM ar_applications WHERE id = ` + q(w.appB) + ` OR customer_id = ` + custB + ` OR invoice_id = ` + q(w.invB) + ` OR payment_id = ` + q(w.payB) + ` OR credit_memo_id = ` + q(w.memoB),
		"customers":       `SELECT * FROM customers WHERE id = ` + custB,
		"activities":      `SELECT * FROM crm_activities WHERE customer_id = ` + custB,
		"contacts":        `SELECT * FROM customer_contacts WHERE customer_id = ` + custB,
		"ship_tos":        `SELECT * FROM customer_ship_tos WHERE customer_id = ` + custB,
		"alerts":          `SELECT * FROM customer_alerts WHERE customer_id = ` + custB,
		"quotes":          `SELECT * FROM quotes WHERE id = ` + q(f.quoteB) + ` OR branch_id = ` + branchB,
		"quote_lines":     `SELECT * FROM quote_lines WHERE quote_id = ` + q(f.quoteB),
		"deliveries":      `SELECT * FROM deliveries WHERE id = ` + q(w.delB) + ` OR order_id = ` + q(w.ordB) + ` OR route_id = ` + q(w.routeB),
		"delivery_routes": `SELECT * FROM delivery_routes WHERE id = ` + q(w.routeB),
		"drafts":          `SELECT * FROM drafts WHERE id IN (` + q(w.draftQB) + `, ` + q(w.draftOB) + `) OR branch_id = ` + branchB,
		"purchase_orders": `SELECT * FROM purchase_orders WHERE id = ` + q(w.poB) + ` OR branch_id = ` + branchB,
		"po_lines":        `SELECT * FROM purchase_order_lines WHERE po_id = ` + q(w.poB),
		"po_freight":      `SELECT * FROM po_freight_charges WHERE po_id = ` + q(w.poB),
		"inventory":       `SELECT * FROM inventory WHERE location_id = ` + q(f.yardB),
		"locations":       `SELECT * FROM locations WHERE id IN (` + q(f.yardB) + `, ` + branchB + `) OR parent_id IN (` + q(f.yardB) + `, ` + branchB + `)`,
		"pos_txns":        `SELECT * FROM pos_transactions WHERE id = ` + q(f.txnB) + ` OR branch_id = ` + branchB + ` OR register_id = ` + reg,
		"pos_lines":       `SELECT * FROM pos_line_items WHERE transaction_id = ` + q(f.txnB),
		"pos_tenders":     `SELECT * FROM pos_tenders WHERE transaction_id = ` + q(f.txnB),
		"pos_returns":          `SELECT * FROM pos_returns WHERE branch_id = ` + branchB + ` OR register_id = ` + reg,
		"tills":                `SELECT * FROM till_sessions WHERE id = ` + q(f.tillB) + ` OR register_id = ` + reg,
		"z_reports":            `SELECT * FROM till_z_reports WHERE branch_id = ` + branchB,
		"credit_holds":         `SELECT * FROM credit_holds WHERE id = ` + q(w.holdB) + ` OR customer_id = ` + custB,
		"credit_limit_changes": `SELECT * FROM credit_limit_changes WHERE customer_id = ` + custB,
	}
	out := make(map[string]string, len(rows))
	for name, sel := range rows {
		var d string
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT md5(COALESCE(string_agg(x::text, ',' ORDER BY x::text), '')) FROM (`+sel+`) x`).Scan(&d); err != nil {
			t.Fatalf("twin state %s: %v", name, err)
		}
		out[name] = d
	}
	return out
}

// changedTables names the tables whose twin digest differs.
func changedTables(before, after map[string]string) []string {
	var out []string
	for name, d := range before {
		if after[name] != d {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// multipartFile builds a one file multipart body and its content type.
func multipartFile(field, name, content string) (string, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile(field, name)
	_, _ = part.Write([]byte(content))
	_ = mw.Close()
	return buf.String(), mw.FormDataContentType()
}

// customerReplace builds a PUT /customers/{id} body from the customer's
// current values with the change given applied, on the revision it reads.
func (f *keyPinFixture) customerReplace(t *testing.T, id uuid.UUID, change map[string]any) string {
	return f.customerReplaceAs(t, "admin", id, change)
}

// customerReplaceAs is customerReplace with the record read as the role given:
// a role that may write a restricted field but not list customers (finance,
// item 57) still reads the record it replaces.
func (f *keyPinFixture) customerReplaceAs(t *testing.T, role string, id uuid.UUID, change map[string]any) string {
	t.Helper()
	cur := f.readJSONAs(t, role, "/api/v1/customers/"+id.String())
	body := map[string]any{"revision": cur["revision"]}
	for _, k := range []string{"account_number", "name", "email", "phone", "address", "is_active",
		"price_profile_id", "salesperson_id", "credit_limit_cents", "currency", "payment_terms_id", "po_required", "job_required"} {
		body[k] = cur[k]
	}
	for k, v := range change {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

// stream opens a server sent event stream as the key given, reads it for the
// time given and returns the status with everything it sent.
func (f *keyPinFixture) stream(t *testing.T, raw, path string, d time.Duration) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", f.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body) // the deadline ends the stream; what arrived before it is the answer
	return res.StatusCode, string(buf)
}

// madeWith runs a create that must answer want, with the headers given.
func (f *keyPinFixture) madeWith(t *testing.T, who, table, method, path, body string, hdr map[string]string, want int) uuid.UUID {
	t.Helper()
	status, buf := f.send(t, who, method, path, body, hdr)
	if status != want {
		t.Errorf("%s %s %s: %d %s, want %d", who, method, path, status, buf, want)
		return uuid.Nil
	}
	var doc struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(buf, &doc); err != nil || doc.ID == uuid.Nil {
		return uuid.Nil
	}
	if table != "" {
		f.madeRow(t, table, doc.ID)
	}
	return doc.ID
}

// purgeFixtureRows removes, children first, every document row the fixture's
// customers and branches picked up while tests drove the routes, so the shared
// database keeps nothing of a run and the customer and branch rows can go.
func purgeFixtureRows(ctx context.Context, db *database.DB, customers, branches []uuid.UUID) {
	// The ids are the fixture's own generated uuids, written into the
	// statements as literals so a statement need not use both lists.
	literal := func(ids []uuid.UUID) string {
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, "'"+id.String()+"'")
		}
		return "ARRAY[" + strings.Join(parts, ",") + "]::uuid[]"
	}
	lists := strings.NewReplacer("$1", literal(customers), "$2", literal(branches))
	own := func(table string) string {
		return `(SELECT id FROM ` + table + ` WHERE customer_id = ANY($1) OR branch_id = ANY($2))`
	}
	for _, stmt := range []string{
		`DELETE FROM credit_limit_changes WHERE customer_id = ANY($1)`,
		`DELETE FROM credit_holds WHERE customer_id = ANY($1)`,
		`DELETE FROM ar_applications WHERE customer_id = ANY($1)`,
		`DELETE FROM payment_refunds WHERE payment_id IN ` + own("payments") + ` OR credit_memo_id IN ` + own("credit_memos"),
		`DELETE FROM tender_lines WHERE payment_id IN ` + own("payments") + ` OR invoice_id IN ` + own("invoices"),
		`DELETE FROM payments WHERE customer_id = ANY($1) OR branch_id = ANY($2)`,
		`DELETE FROM credit_memo_lines WHERE credit_memo_id IN ` + own("credit_memos"),
		`DELETE FROM credit_memos WHERE customer_id = ANY($1) OR branch_id = ANY($2)`,
		`DELETE FROM invoice_lines WHERE invoice_id IN ` + own("invoices"),
		`DELETE FROM invoices WHERE customer_id = ANY($1) OR branch_id = ANY($2)`,
		`DELETE FROM order_fulfillment_requests WHERE order_id IN ` + own("orders"),
		`DELETE FROM order_allocation_requests WHERE order_id IN ` + own("orders"),
		`DELETE FROM delivery_qty_adjustments WHERE delivery_id IN (SELECT id FROM deliveries WHERE order_id IN ` + own("orders") + `)`,
		`DELETE FROM delivery_pod_photos WHERE delivery_id IN (SELECT id FROM deliveries WHERE order_id IN ` + own("orders") + `)`,
		`DELETE FROM delivery_notifications WHERE delivery_id IN (SELECT id FROM deliveries WHERE order_id IN ` + own("orders") + `)`,
		`DELETE FROM deliveries WHERE order_id IN ` + own("orders"),
		`DELETE FROM order_lines WHERE order_id IN ` + own("orders"),
		`DELETE FROM orders WHERE customer_id = ANY($1) OR branch_id = ANY($2)`,
		`DELETE FROM quote_exposure_events WHERE quote_id IN ` + own("quotes"),
		`DELETE FROM quote_lines WHERE quote_id IN ` + own("quotes"),
		`DELETE FROM quotes WHERE customer_id = ANY($1) OR branch_id = ANY($2)`,
		`DELETE FROM draft_events WHERE draft_id IN (SELECT id FROM drafts WHERE branch_id = ANY($2))`,
		`DELETE FROM drafts WHERE branch_id = ANY($2)`,
		`DELETE FROM po_freight_charges WHERE po_id IN (SELECT id FROM purchase_orders WHERE branch_id = ANY($2))`,
		`DELETE FROM purchase_order_lines WHERE po_id IN (SELECT id FROM purchase_orders WHERE branch_id = ANY($2))`,
		`DELETE FROM purchase_orders WHERE branch_id = ANY($2)`,
		`DELETE FROM pos_tenders WHERE transaction_id IN (SELECT id FROM pos_transactions WHERE branch_id = ANY($2))`,
		`DELETE FROM pos_line_items WHERE transaction_id IN (SELECT id FROM pos_transactions WHERE branch_id = ANY($2))`,
		`DELETE FROM pos_return_lines WHERE return_id IN (SELECT id FROM pos_returns WHERE branch_id = ANY($2))`,
		`DELETE FROM pos_returns WHERE branch_id = ANY($2)`,
		`DELETE FROM pos_transactions WHERE branch_id = ANY($2)`,
		`DELETE FROM customer_ship_tos WHERE customer_id = ANY($1)`,
		`DELETE FROM customer_alerts WHERE customer_id = ANY($1)`,
		`DELETE FROM customer_contacts WHERE customer_id = ANY($1)`,
		`DELETE FROM crm_activities WHERE customer_id = ANY($1)`,
		`DELETE FROM yard_schedule_windows WHERE yard_id = ANY($2)`,
	} {
		_, _ = db.Pool.Exec(ctx, lists.Replace(stmt))
	}
}
