// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// SEC-NOHEADER-1: a user context with no branch (no X-Branch-Id, the
// multi branch switch on, default_branch_required off) is held to the
// user's own grants on every branch scoped read and act, never "no branch
// means every branch" (FROM-PRINCIPAL 59). Each route family drives a user
// granted only A, with no header, reading and acting on B's records, every
// refusal measured by the rows that name the twins. An administrator with
// no header keeps the reach ADR 0007 section 2.3 gives it, and a user with
// no grants at all is refused fail closed.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// noheaderGrant grants a user one branch, the fixture's cleanup removes it
// (every sub this file uses starts with pin-).
func noheaderGrant(t *testing.T, f *keyPinFixture, sub string, branch uuid.UUID) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'sec-noheader')
		 ON CONFLICT DO NOTHING`,
		sub, branch); err != nil {
		t.Fatalf("grant %s: %v", sub, err)
	}
}

// noheaderRegister makes a register on the branch's yard and removes it
// with everything its legs write.
func noheaderRegister(t *testing.T, f *keyPinFixture, branch, yard uuid.UUID) string {
	t.Helper()
	ctx := context.Background()
	id := "NH-" + uuid.NewString()[:8]
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO pos_registers (id, location_id, branch_id, name) VALUES ($1, $2, $3, 'noheader')`,
		id, yard, branch); err != nil {
		t.Fatalf("seed register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM till_z_reports WHERE till_session_id IN
			(SELECT id FROM till_sessions WHERE register_id = $1)`, id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE register_id = $1`, id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM till_sessions WHERE register_id = $1`, id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_registers WHERE id = $1`, id)
	})
	return id
}

// noheaderCount counts rows the caller names, so a refusal that wrote
// nothing is seen as zero.
func noheaderCount(t *testing.T, f *keyPinFixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// noheaderPurgeReturn removes a return this file's control legs write, with
// its lines and its GL entry.
func noheaderPurgeReturn(t *testing.T, f *keyPinFixture, registerID string) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() {
		ids := `SELECT id FROM pos_returns WHERE register_id = $1`
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN
			(SELECT id FROM gl_journal_entries WHERE source_ref_id IN (SELECT id FROM pos_returns WHERE register_id = $1))`, registerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE source_ref_id IN (`+ids+`)`, registerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_return_lines WHERE return_id IN (`+ids+`)`, registerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_returns WHERE register_id = $1`, registerID)
	})
}

// cashierNoHeader sends one request as the cashier the grants name, with no
// X-Branch-Id: the branch middleware settles no branch for a non admin
// user while default_branch_required is off.
func (f *keyPinFixture) cashierNoHeader(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	return f.userAs(t, "cashier", method, path, body, nil)
}

// TestNoHeader_RegistersAndTills is the registers and tills family: a
// cashier granted only A, no header, opening, closing and reading B's
// drawer. The open and the close write nothing and answer the register or
// record refusal; the reads answer missing; the lists carry only A's rows.
func TestNoHeader_RegistersAndTills(t *testing.T) {
	f := newKeyPinFixture(t)
	noheaderGrant(t, f, "pin-cashier", f.branchA)
	regNB := noheaderRegister(t, f, f.branchB, f.yardB)
	regNA := noheaderRegister(t, f, f.branchA, f.yardA)
	t.Cleanup(func() {
		// A close that lands before the fix writes a Z report the fixture's
		// own cleanup does not know.
		_, _ = f.db.Pool.Exec(context.Background(),
			`DELETE FROM till_z_reports WHERE till_session_id = $1 AND id <> $2`, f.tillB, f.zrB)
	})

	// Open on B's register: refused, no session written.
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM till_sessions WHERE register_id = $1`, regNB); n != 0 {
		t.Fatalf("pre: %d sessions on the B register", n)
	}
	status, buf := f.cashierNoHeader(t, "POST", "/api/v1/pos/till/open",
		`{"register_id":"`+regNB+`","opening_float_cents":1000}`)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a different branch than the request") {
		t.Errorf("no header open on B's register: %d %s, want the 403 naming the register rule", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM till_sessions WHERE register_id = $1`, regNB); n != 0 {
		t.Errorf("the refused open wrote %d sessions on B's register, want none", n)
	}

	// Close B's open drawer: refused, the drawer stays open, and no Z report
	// is written beyond the one the fixture froze (zrB).
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM till_z_reports WHERE till_session_id = $1`, f.tillB); n != 1 {
		t.Fatalf("pre: %d Z reports on B's session, want the fixture's one", n)
	}
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/till/"+f.tillB.String()+"/close",
		`{"counted_by_method":{"cash":5000}}`)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a branch this caller may not target") {
		t.Errorf("no header close of B's session: %d %s, want the 403 naming the record rule", status, buf)
	}
	if s := noheaderCount(t, f, `SELECT COUNT(*) FROM till_sessions WHERE id = $1 AND status = 'OPEN'`, f.tillB); s != 1 {
		t.Errorf("the refused close left B's session open in %d rows, want its one OPEN row", s)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM till_z_reports WHERE till_session_id = $1 AND id <> $2`, f.tillB, f.zrB); n != 0 {
		t.Errorf("the refused close wrote %d Z reports, want none", n)
	}

	// Current, report and Z report on B's drawer read as none or missing.
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/till/current?register_id="+f.regB, "")
	if status != http.StatusNotFound {
		t.Errorf("no header current till on B's register: %d %s, want 404 (no open session)", status, buf)
	}
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/till/"+f.tillB.String()+"/report", "")
	if status != http.StatusNotFound {
		t.Errorf("no header till report on B's session: %d %s, want 404", status, buf)
	}
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/till/"+f.tillB.String()+"/zreport", "")
	if status != http.StatusNotFound {
		t.Errorf("no header Z report on B's session: %d %s, want 404", status, buf)
	}

	// The Z report list carries A's rows only.
	var zDay string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT (NOW() AT TIME ZONE COALESCE((SELECT timezone FROM locations WHERE id = $1), 'UTC'))::date::text`,
		f.branchA).Scan(&zDay); err != nil {
		t.Fatalf("branch A business day: %v", err)
	}
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/zreports?date="+zDay, "")
	if status != http.StatusOK || !strings.Contains(string(buf), f.zrA.String()) {
		t.Errorf("no header Z report list: %d %s, want A's row", status, buf)
	}
	if strings.Contains(string(buf), f.zrB.String()) {
		t.Errorf("no header Z report list carries B's row: %s", buf)
	}

	// A session of no branch: no grant decides it, so the no header close is
	// refused with the record rule's 403 (the drawer stays open, no Z
	// report), while an administrator closes it.
	nullSess := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO till_sessions (id, register_id, branch_id, cashier_id, status, opening_float)
		 VALUES ($1, $2, NULL, $3, 'OPEN', 10)`, nullSess, regNA, uuid.New()); err != nil {
		t.Fatalf("seed null branch session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM till_z_reports WHERE till_session_id = $1`, nullSess)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM till_sessions WHERE id = $1`, nullSess)
	})
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/till/"+nullSess.String()+"/close",
		`{"counted_by_method":{"cash":1000}}`)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a branch this caller may not target") {
		t.Errorf("no header close of a NULL branch session: %d %s, want the 403 the record rule names", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM till_sessions WHERE id = $1 AND status = 'OPEN'`, nullSess); n != 1 {
		t.Errorf("the refused close left the NULL branch session open in %d rows, want its one", n)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM till_z_reports WHERE till_session_id = $1`, nullSess); n != 0 {
		t.Errorf("the refused close wrote %d Z reports on the NULL branch session, want none", n)
	}
	status, buf = f.userAs(t, "admin", "POST", "/api/v1/pos/till/"+nullSess.String()+"/close",
		`{"counted_by_method":{"cash":1000}}`, nil)
	if status != http.StatusOK {
		t.Errorf("admin close of a NULL branch session: %d %s, want 200", status, buf)
	}

	// The controls on A: the open lands, and the reads answer.
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/till/open",
		`{"register_id":"`+regNA+`","opening_float_cents":1000}`)
	if status != http.StatusCreated {
		t.Errorf("no header open on A's register: %d %s, want 201", status, buf)
	}
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/till/current?register_id="+f.regA, "")
	if status != http.StatusOK || !strings.Contains(string(buf), f.tillA.String()) {
		t.Errorf("no header current till on A's register: %d %s, want the session", status, buf)
	}
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/till/"+f.tillA.String()+"/report", "")
	if status != http.StatusOK {
		t.Errorf("no header till report on A's session: %d %s, want 200", status, buf)
	}
}

// TestNoHeader_Sales is the sales family: creating a sale on B's register,
// reading, listing, adding and removing lines, completing and voiding B's
// sale. Every act writes nothing and answers the register or record
// refusal; the reads answer missing; the list carries only A's rows.
func TestNoHeader_Sales(t *testing.T) {
	f := newKeyPinFixture(t)
	noheaderGrant(t, f, "pin-cashier", f.branchA)
	regNB := noheaderRegister(t, f, f.branchB, f.yardB)
	regNA := noheaderRegister(t, f, f.branchA, f.yardA)
	openTillOn(t, f, regNA, f.branchA)

	// Create on B's register: refused, nothing written.
	status, buf := f.cashierNoHeader(t, "POST", "/api/v1/pos/transactions", `{"register_id":"`+regNB+`"}`)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a different branch than the request") {
		t.Errorf("no header sale on B's register: %d %s, want the 403 naming the register rule", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE register_id = $1`, regNB); n != 0 {
		t.Errorf("the refused sale wrote %d transactions on B's register, want none", n)
	}

	// Read and list B's seeded sale: missing, and absent from the list.
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/transactions/"+f.txnB.String(), "")
	if status != http.StatusNotFound {
		t.Errorf("no header read of B's sale: %d %s, want 404", status, buf)
	}
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/transactions", "")
	if status != http.StatusOK || !strings.Contains(string(buf), f.txnA.String()) {
		t.Errorf("no header sale list: %d %s, want A's row", status, buf)
	}
	if strings.Contains(string(buf), f.txnB.String()) {
		t.Errorf("no header sale list carries B's row: %s", buf)
	}

	// Add a line to B's sale: refused, no line written.
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_line_items WHERE transaction_id = $1`, f.txnB); n != 1 {
		t.Fatalf("pre: %d lines on B's sale", n)
	}
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/transactions/"+f.txnB.String()+"/items",
		`{"line":{"product_id":"`+f.posProd.String()+`","quantity":"1"}}`)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a branch this caller may not target") {
		t.Errorf("no header add item to B's sale: %d %s, want the 403 naming the record rule", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_line_items WHERE transaction_id = $1`, f.txnB); n != 1 {
		t.Errorf("the refused add left %d lines on B's sale, want its one", n)
	}

	// Remove a line of B's sale: refused, the line stays.
	status, buf = f.cashierNoHeader(t, "DELETE", "/api/v1/pos/transactions/"+f.txnB.String()+"/items/"+f.lineB.String(), "")
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a branch this caller may not target") {
		t.Errorf("no header remove item of B's sale: %d %s, want the 403 naming the record rule", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_line_items WHERE id = $1`, f.lineB); n != 1 {
		t.Errorf("the refused remove left B's line in %d rows, want its one", n)
	}

	// B's line named under A's sale: the hold checks the path's sale, so the
	// removal wall is the line's own guard. A later edit cannot drop it
	// unseen: without the wall the line deletes under a sale the caller
	// holds.
	status, buf = f.cashierNoHeader(t, "DELETE", "/api/v1/pos/transactions/"+f.txnA.String()+"/items/"+f.lineB.String(), "")
	if status >= 500 {
		t.Errorf("no header remove of B's line under A's sale: %d %s, want the wall's silent miss", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_line_items WHERE id = $1`, f.lineB); n != 1 {
		t.Errorf("the walled remove deleted B's line under A's sale: %d rows, want its one", n)
	}

	// Complete B's sale: refused, no tender, the sale stays open.
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/transactions/"+f.txnB.String()+"/complete",
		`{"tenders":[{"method":"cash","amount_cents":100}]}`)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a branch this caller may not target") {
		t.Errorf("no header complete of B's sale: %d %s, want the 403 naming the record rule", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE id = $1 AND status = 'OPEN'`, f.txnB); n != 1 {
		t.Errorf("the refused complete left B's sale open in %d rows, want its one OPEN row", n)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_tenders WHERE transaction_id = $1`, f.txnB); n != 0 {
		t.Errorf("the refused complete wrote %d tenders, want none", n)
	}

	// Void B's sale: refused, the sale stays open.
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/transactions/"+f.txnB.String()+"/void", `{"reason":"pin"}`)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a branch this caller may not target") {
		t.Errorf("no header void of B's sale: %d %s, want the 403 naming the record rule", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE id = $1 AND status = 'OPEN'`, f.txnB); n != 1 {
		t.Errorf("the refused void left B's sale open in %d rows, want its one OPEN row", n)
	}

	// The controls on A: the create lands and reads back.
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/transactions", `{"register_id":"`+regNA+`"}`)
	if status != http.StatusCreated {
		t.Errorf("no header sale on A's register: %d %s, want 201", status, buf)
	}
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/transactions/"+f.txnA.String(), "")
	if status != http.StatusOK {
		t.Errorf("no header read of A's sale: %d %s, want 200", status, buf)
	}
}

// noheaderStock holds stock of the POS product in A's yard: a synced sale
// issues it and a return restocks it (the fixture's cleanup removes the
// product's inventory rows).
func noheaderStock(t *testing.T, f *keyPinFixture) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 100, 0)`,
		f.posProd, f.yardA); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
}

// noheaderTaxRate gives the branch a default tax rate: a return or a sale
// completion refuses without one (tax_rate_not_configured), and the fixture's
// branches carry none.
func noheaderTaxRate(t *testing.T, f *keyPinFixture, branch uuid.UUID) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE locations SET default_tax_rate = 0.05 WHERE id = $1`, branch); err != nil {
		t.Fatalf("set the branch tax rate: %v", err)
	}
}

// TestNoHeader_Returns is the returns family: recording a return on B's
// register, reading and listing B's return. The create writes nothing and
// answers the register refusal; the reads answer missing; the list carries
// only A's rows.
func TestNoHeader_Returns(t *testing.T) {
	f := newKeyPinFixture(t)
	noheaderGrant(t, f, "pin-cashier", f.branchA)
	regNB := noheaderRegister(t, f, f.branchB, f.yardB)
	regNA := noheaderRegister(t, f, f.branchA, f.yardA)
	noheaderPurgeReturn(t, f, regNA)
	// A cash return pays out of an open drawer: A's seeded register holds one.
	noheaderPurgeReturn(t, f, f.regA)
	noheaderTaxRate(t, f, f.branchA)
	noheaderStock(t, f)

	body := func(register string) string {
		return `{"register_id":"` + register + `","refund_method":"cash","reason":"pin","lines":[{"product_id":"` +
			f.posProd.String() + `","quantity":"1","unit_price_ten_thousandths":10000}]}`
	}

	// Create on B's register: refused, no return written.
	status, buf := f.cashierNoHeader(t, "POST", "/api/v1/pos/returns", body(regNB))
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a different branch than the request") {
		t.Errorf("no header return on B's register: %d %s, want the 403 naming the register rule", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_returns WHERE register_id = $1`, regNB); n != 0 {
		t.Errorf("the refused return wrote %d returns on B's register, want none", n)
	}

	// Read and list B's seeded return: missing, and absent from the list.
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/returns/"+f.retB.String(), "")
	if status != http.StatusNotFound {
		t.Errorf("no header read of B's return: %d %s, want 404", status, buf)
	}
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/returns", "")
	if status != http.StatusOK || !strings.Contains(string(buf), f.retA.String()) {
		t.Errorf("no header return list: %d %s, want A's row", status, buf)
	}
	if strings.Contains(string(buf), f.retB.String()) {
		t.Errorf("no header return list carries B's row: %s", buf)
	}

	// The control on A: the return lands.
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/returns", body(f.regA))
	if status != http.StatusCreated {
		t.Errorf("no header return on A's register: %d %s, want 201", status, buf)
	}
}

// TestNoHeader_Sync is the offline sync family: a batch naming B's register
// is refused by the register's wall (ADR 0011 6.3), and one naming A's is
// refused by rule (money never queues where the branch has no offline
// policy), each before any item runs and before the log row.
func TestNoHeader_Sync(t *testing.T) {
	f := newKeyPinFixture(t)
	ctx := context.Background()
	noheaderGrant(t, f, "pin-cashier", f.branchA)
	noheaderTaxRate(t, f, f.branchA)
	noheaderStock(t, f)
	batch := "nh-" + uuid.NewString()[:8]
	clientB, clientA := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_line_items WHERE transaction_id IN ($1, $2)`, clientB, clientA)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id IN ($1, $2)`, clientB, clientA)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_sync_log WHERE batch_id = $1`, batch)
	})
	body := func(register string, client uuid.UUID) string {
		return `{"batch_id":"` + batch + `","register_id":"` + register +
			`","device_epoch":1,"reported_seq":0,"items":[{"client_id":"` + client.String() +
			`","cashier_id":"` + uuid.NewString() + `","items":[{"product_id":"` + f.posProd.String() +
			`","quantity":"1"}],"tenders":[{"method":"cash","amount_cents":100000}],"client_created_at":"` +
			time.Now().UTC().Format(time.RFC3339) + `"}]}`
	}

	status, buf := f.cashierNoHeader(t, "POST", "/api/v1/pos/sync", body(f.regB, clientB))
	if status != http.StatusForbidden || !strings.Contains(string(buf), "belongs to a different branch") {
		t.Errorf("no header sync on B's register: %d %s, want the register wall's 403", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_sync_log WHERE batch_id = $1`, batch); n != 0 {
		t.Errorf("the refused sync wrote %d log rows, want none", n)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE id = $1`, clientB); n != 0 {
		t.Errorf("the refused sync wrote %d transactions, want none", n)
	}

	// The control: a batch on A's register passes the wall and is refused by
	// rule, nothing written.
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/sync", body(f.regA, clientA))
	if status != http.StatusConflict || !strings.Contains(string(buf), "money_never_queues") {
		t.Errorf("no header sync on A's register: %d %s, want the rule's 409", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_sync_log WHERE batch_id = $1`, batch); n != 0 {
		t.Errorf("the refused control sync wrote %d log rows, want none", n)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE id = $1`, clientA); n != 0 {
		t.Errorf("the refused control sync wrote %d transactions, want none", n)
	}
}

// TestNoHeader_OtherFamilies is the sweep: the families whose repository
// filter was their only wall. Orders and the purchase order list did not
// hold the record's branch to a no header user's grants and now do; the
// quote and purchase order by id routes, and the invoice, payment,
// delivery and document print routes, already held it (their wall, record
// check or guard carries the grants arm), which the green legs here prove.
func TestNoHeader_OtherFamilies(t *testing.T) {
	f := newKeyPinFixture(t)
	w := f.seedWalledTwins(t)
	noheaderGrant(t, f, "pin-sales", f.branchA)
	noheaderGrant(t, f, "pin-purchasing", f.branchA)
	noheaderGrant(t, f, "pin-warehouse", f.branchA)

	// Orders: the by id read and the old write routes now carry the record
	// branch rule the allocate, fulfilment and retry writes already had (the
	// module's 403 naming id); the list carries only A's rows.
	status, buf := f.userAs(t, "sales", "GET", "/api/v1/orders/"+w.ordB.String(), "", nil)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "outside the branches this caller may target") {
		t.Errorf("no header read of B's order: %d %s, want the 403 the record rule serves", status, buf)
	}
	status, buf = f.userAs(t, "sales", "GET", "/api/v1/orders", "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), w.ordA.String()) {
		t.Errorf("no header order list: %d %s, want A's row", status, buf)
	}
	if strings.Contains(string(buf), w.ordB.String()) {
		t.Errorf("no header order list carries B's row: %s", buf)
	}
	// The transition act: refused, nothing written (the seeded order stays
	// CONFIRMED).
	status, buf = f.userAs(t, "sales", "POST", "/api/v1/orders/"+w.ordB.String()+"/transitions",
		`{"to":"cancelled","reason":"no header","revision":1}`, nil)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "outside the branches this caller may target") {
		t.Errorf("no header transition of B's order: %d %s, want the 403 the record rule serves", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM orders WHERE id = $1 AND status = 'CONFIRMED'`, w.ordB); n != 1 {
		t.Errorf("the refused transition left B's order confirmed in %d rows, want its one", n)
	}
	// The update act: the same record check answers the module's 403 before
	// the draft status rule can (without it the refusal was a later 409).
	status, buf = f.userAs(t, "sales", "PUT", "/api/v1/orders/"+w.ordB.String(),
		`{"customer_id":"`+f.custB.String()+`","delivery_type":"pickup","revision":1,"lines":[]}`, nil)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "outside the branches this caller may target") {
		t.Errorf("no header update of B's order: %d %s, want the 403 the record rule serves", status, buf)
	}

	// Quotes: the by id read already holds through the service's record
	// check (the quote's branch held to the caller's grants, 403), and the
	// list already carried the grants wall.
	status, buf = f.userAs(t, "sales", "GET", "/api/v1/quotes/"+f.quoteB.String(), "", nil)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "outside the branches this caller may target") {
		t.Errorf("no header read of B's quote: %d %s, want the 403 the record check serves", status, buf)
	}
	status, buf = f.userAs(t, "sales", "GET", "/api/v1/quotes", "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), f.quoteA.String()) {
		t.Errorf("no header quote list: %d %s, want A's row", status, buf)
	}
	if strings.Contains(string(buf), f.quoteB.String()) {
		t.Errorf("no header quote list carries B's row: %s", buf)
	}

	// Purchase orders: the by id routes already hold through the handler's
	// record check (a 403 whose logged words the generic body carries); the
	// list did not and now carries only A's rows.
	status, buf = f.userAs(t, "purchasing", "GET", "/api/v1/purchase-orders/"+w.poB.String(), "", nil)
	if status != http.StatusForbidden {
		t.Errorf("no header read of B's purchase order: %d %s, want the 403 the record check serves", status, buf)
	}
	status, buf = f.userAs(t, "purchasing", "GET", "/api/v1/purchase-orders", "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), w.poA.String()) {
		t.Errorf("no header purchase order list: %d %s, want A's row", status, buf)
	}
	if strings.Contains(string(buf), w.poB.String()) {
		t.Errorf("no header purchase order list carries B's row: %s", buf)
	}

	// Invoices and payments already wall their reads on the grants.
	status, buf = f.userAs(t, "sales", "GET", "/api/v1/invoices/"+w.invB.String(), "", nil)
	if status != http.StatusNotFound {
		t.Errorf("no header read of B's invoice: %d %s, want 404", status, buf)
	}
	status, buf = f.userAs(t, "sales", "GET", "/api/v1/payments/"+w.payB.String(), "", nil)
	if status != http.StatusNotFound {
		t.Errorf("no header read of B's payment: %d %s, want 404", status, buf)
	}

	// Deliveries already wall the stop read through its order's branch.
	status, buf = f.userAs(t, "warehouse", "GET", "/api/v1/delivery/deliveries/"+w.delB.String(), "", nil)
	if status != http.StatusNotFound {
		t.Errorf("no header read of B's delivery: %d %s, want 404", status, buf)
	}

	// The document print route already holds the record's own branch to the
	// guard, which is what scopes a no header user to the grants.
	status, buf = f.userAs(t, "sales", "GET", "/api/v1/documents/print/invoice/"+w.invB.String(), "", nil)
	if status != http.StatusForbidden {
		t.Errorf("no header print of B's invoice: %d %s, want the 403 the guard serves", status, buf)
	}
}

// TestNoHeader_UserWithNoGrants is the fail closed arm: a user with no
// grants at all reads nothing and writes nothing, on A's records as on
// B's, because no grant decides them.
func TestNoHeader_UserWithNoGrants(t *testing.T) {
	f := newKeyPinFixture(t)
	regNA := noheaderRegister(t, f, f.branchA, f.yardA)

	status, buf := f.cashierNoHeader(t, "GET", "/api/v1/pos/till/current?register_id="+f.regA, "")
	if status != http.StatusNotFound {
		t.Errorf("grants-less current till on A's register: %d %s, want 404 (no open session)", status, buf)
	}
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/transactions/"+f.txnA.String(), "")
	if status != http.StatusNotFound {
		t.Errorf("grants-less read of A's sale: %d %s, want 404", status, buf)
	}
	status, buf = f.cashierNoHeader(t, "GET", "/api/v1/pos/transactions", "")
	if status != http.StatusOK || strings.Contains(string(buf), f.txnA.String()) {
		t.Errorf("grants-less sale list: %d %s, want no rows", status, buf)
	}
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/till/open",
		`{"register_id":"`+regNA+`","opening_float_cents":1000}`)
	if status != http.StatusForbidden {
		t.Errorf("grants-less open on A's register: %d %s, want 403", status, buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM till_sessions WHERE register_id = $1`, regNA); n != 0 {
		t.Errorf("the grants-less open wrote %d sessions, want none", n)
	}
}

// TestNoHeader_AdminKeepsReach is the ADR 0007 section 2.3 control: "With
// no context branch, an administrator or owner may name any branch, as the
// X-Branch-Id header already lets them". An administrator with no header
// keeps reading every branch's rows.
func TestNoHeader_AdminKeepsReach(t *testing.T) {
	f := newKeyPinFixture(t)
	w := f.seedWalledTwins(t)

	status, buf := f.userAs(t, "admin", "GET", "/api/v1/pos/transactions/"+f.txnB.String(), "", nil)
	if status != http.StatusOK {
		t.Errorf("admin no header read of B's sale: %d %s, want 200", status, buf)
	}
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/pos/till/current?register_id="+f.regB, "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), f.tillB.String()) {
		t.Errorf("admin no header current till on B's register: %d %s, want the session", status, buf)
	}
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/pos/transactions", "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), f.txnB.String()) {
		t.Errorf("admin no header sale list: %d %s, want B's row beside A's", status, buf)
	}
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/orders/"+w.ordB.String(), "", nil)
	if status != http.StatusOK {
		t.Errorf("admin no header read of B's order: %d %s, want 200", status, buf)
	}
}
