// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// CP-07: a deposit taken on an order through the payments route, on the wire.
// The order page's figures come from the order answer: each deposit, the
// total deposited, what is applied and what is still unapplied, and the
// balance due after deposits. The refusals a dealer needs: a deposit larger
// than the balance due after deposits, and one on a cancelled (voided) or
// fully invoiced order.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
)

// serveOrdersAndPayments mounts the order and payment routes on one server,
// wired the way serve wires them, so a deposit rides the real payments route
// against the same order the fulfilment bills.
func (f *fixture) serveOrdersAndPayments() {
	f.t.Helper()
	glSvc := gl.NewService(gl.NewRepository(f.db), nil, slog.Default())
	acct := account.NewService(f.db, glSvc, slog.Default())
	inv := invoice.NewService(invoice.NewRepository(f.db), glSvc, acct, f.db).WithAuditLog(audit.NewLogger(f.db))
	osvc := order.NewService(order.NewRepository(f.db)).WithOutbox(outbox.NewWriter(f.db, "")).WithTxRunner(f.db).
		WithAuditLog(audit.NewLogger(f.db)).WithInvoices(inv).WithDeposits(acct)
	osvc = osvc.WithInventory(inventory.NewService(inventory.NewRepository(f.db)))
	psvc := payment.NewService(f.db, payment.NewRepository(f.db), acct).WithAuditLog(audit.NewLogger(f.db)).WithOutbox(outbox.NewWriter(f.db, ""))
	mux := http.NewServeMux()
	order.NewHandler(osvc).RegisterRoutes(mux)
	payment.NewHandler(psvc).RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(f.db)(roleClaims(middleware.NewBranchMiddleware(f.db).Handler(mux))))
	f.t.Cleanup(f.srv.Close)
}

// doUnique posts with a request id of its own, so the idempotency layer
// never answers a earlier identical post for a later one.
func (f *fixture) doUnique(method, path string, body any, reqID string) resp {
	f.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case nil:
	default:
		raw, _ = json.Marshal(b)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(string(raw)))
	if err != nil {
		f.t.Fatal(err)
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", reqID)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	out := resp{status: res.StatusCode, header: res.Header, raw: buf}
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

// takeDeposit posts a deposit on the order through the payments route.
func (f *fixture) takeDeposit(orderID string, cents int64, method string) resp {
	f.t.Helper()
	return f.doUnique("POST", "/api/v1/payments", map[string]any{
		"customer_id": f.customerID.String(), "order_id": orderID,
		"amount_cents": cents, "method": method, "reference": "CP-07 deposit",
	}, "req-cp07-"+orderID[:8]+"-"+fmt.Sprint(cents)+"-"+method)
}

// depositLegs reads the legs of the deposit's own entries (cents, debit less
// credit by account code): its receipt and the applications of it.
func depositLegs(t *testing.T, db *database.DB, paymentID string) string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(), `
		SELECT a.code, ROUND(SUM(l.debit - l.credit) * 100)::bigint FROM gl_journal_lines l
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id JOIN gl_accounts a ON a.id = l.account_id
		WHERE e.source_ref_id = $1 OR e.source_ref_id IN (SELECT id FROM ar_applications WHERE payment_id = $1::uuid)
		GROUP BY a.code ORDER BY a.code`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var code string
		var net int64
		if err := rows.Scan(&code, &net); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s=%d", code, net))
	}
	return strings.Join(out, ",")
}

// RULE: a deposit on an order is held unapplied (DR 1010 / CR 2200), the
// order answer carries its figures, and the fulfilment applies it to the
// invoice it bills (DR 2200 / CR 1020), leaving the balance the deposit did
// not cover.
func TestCP07_DepositOnOrderAppliedAtInvoice(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)

	f := newFixture(t, db)
	f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "3.2500")

	r := f.create() // 10 PCS pickup at 5.50 = 5500, tax 8.875% = 488, total 5988
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")

	r = f.takeDeposit(id, 4000, "check")
	if r.status != http.StatusCreated {
		t.Fatalf("take the deposit = %d: %s", r.status, r.raw)
	}
	depositID := str(t, r.body, "id")
	if got := num(t, r.body, "unapplied_cents"); got != 4000 {
		t.Errorf("the deposit is %d unapplied, want 4000", got)
	}
	if got := depositLegs(t, db, depositID); got != "1010=4000,2200=-4000" {
		t.Errorf("the deposit's receipt entry nets %s, want 1010=4000,2200=-4000", got)
	}

	// The order answer carries the deposit figures the panel shows.
	r = f.do("GET", "/api/v1/orders/"+id, nil)
	if r.status != http.StatusOK {
		t.Fatalf("read the order = %d: %s", r.status, r.raw)
	}
	deps, ok := r.body["deposits"].([]any)
	if !ok || len(deps) != 1 {
		t.Fatalf("the order answer carries %v deposits, want 1", r.body["deposits"])
	}
	dep, _ := deps[0].(map[string]any)
	if str(t, dep, "id") != depositID {
		t.Errorf("the deposit's id is %v, want the payment just taken", dep["id"])
	}
	if str(t, dep, "method") != "check" {
		t.Errorf("the deposit's method is %v, want check", dep["method"])
	}
	if got := num(t, dep, "amount_cents"); got != 4000 {
		t.Errorf("the deposit's amount is %d, want 4000", got)
	}
	if got := num(t, dep, "unapplied_cents"); got != 4000 {
		t.Errorf("the deposit's unapplied is %d, want 4000", got)
	}
	if apps, ok := dep["applications"].([]any); !ok || len(apps) != 0 {
		t.Errorf("the deposit's applications are %v, want none yet", dep["applications"])
	}
	if got := num(t, r.body, "deposit_total_cents"); got != 4000 {
		t.Errorf("deposit_total_cents = %d, want 4000", got)
	}
	if got := num(t, r.body, "deposit_unapplied_cents"); got != 4000 {
		t.Errorf("deposit_unapplied_cents = %d, want 4000", got)
	}
	if got := num(t, r.body, "balance_due_after_deposits_cents"); got != 1988 {
		t.Errorf("balance_due_after_deposits_cents = %d, want 1988", got)
	}

	// The fulfilment applies the deposit to the invoice it bills.
	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer"})
	if r.status != http.StatusCreated {
		t.Fatalf("fulfil = %d: %s", r.status, r.raw)
	}
	inv := invoiceIDOf(t, r)
	var applied int64
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(ROUND(a.amount * 100)::bigint), 0) FROM ar_applications a
		 WHERE a.payment_id = $1 AND a.invoice_id = $2 AND a.reversed_at IS NULL`, depositID, inv).Scan(&applied); err != nil || applied != 4000 {
		t.Errorf("the invoice carries %d of the deposit (%v), want 4000", applied, err)
	}
	if got := depositLegs(t, db, depositID); got != "1010=4000,1020=-4000,2200=0" {
		t.Errorf("the deposit's entries net %s, want the receipt (1010=4000, 2200=-4000) undone by the application (2200=4000, 1020=-4000)", got)
	}

	// The order answer moves the deposit from unapplied to applied.
	r = f.do("GET", "/api/v1/orders/"+id, nil)
	if r.status != http.StatusOK {
		t.Fatalf("read the order again = %d: %s", r.status, r.raw)
	}
	deps, _ = r.body["deposits"].([]any)
	dep, _ = deps[0].(map[string]any)
	if got := num(t, dep, "unapplied_cents"); got != 0 {
		t.Errorf("after the invoice the deposit is %d unapplied, want 0", got)
	}
	apps, _ := dep["applications"].([]any)
	if len(apps) != 1 {
		t.Fatalf("the deposit names %d applications, want 1", len(apps))
	}
	app, _ := apps[0].(map[string]any)
	if str(t, app, "invoice_id") != inv {
		t.Errorf("the application's invoice is %v, want the one just billed", app["invoice_id"])
	}
	if got := num(t, app, "amount_cents"); got != 4000 {
		t.Errorf("the application is %d, want 4000", got)
	}
	if got := num(t, r.body, "deposit_unapplied_cents"); got != 0 {
		t.Errorf("deposit_unapplied_cents = %d, want 0", got)
	}
	if got := num(t, r.body, "balance_due_after_deposits_cents"); got != 1988 {
		t.Errorf("balance_due_after_deposits_cents = %d, want 1988", got)
	}
}

// RULE: two deposits on an order apply to its invoices oldest first, across
// partial fulfilments.
func TestCP07_TwoDepositsPartialFulfilmentsAppliedOldestFirst(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)

	f := newFixture(t, db)
	f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "3.2500")

	r := f.create() // 10 PCS pickup at 5.50 = 5500, tax 8.875% = 488, total 5988
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")

	r = f.takeDeposit(id, 1000, "cash")
	if r.status != http.StatusCreated {
		t.Fatalf("the first deposit = %d: %s", r.status, r.raw)
	}
	first := str(t, r.body, "id")
	r = f.takeDeposit(id, 2000, "check")
	if r.status != http.StatusCreated {
		t.Fatalf("the second deposit = %d: %s", r.status, r.raw)
	}
	second := str(t, r.body, "id")

	// The line's id and the order's revision, to bill half of it.
	lineID := str(t, f.bodyOfLine(t, id), "id")
	r = f.do("GET", "/api/v1/orders/"+id, nil)

	// The first fulfilment bills 5 of 10: an invoice of 2994, and the
	// deposits apply to it oldest first: the first fully, the second in part.
	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer",
		"lines": []map[string]any{{"order_line_id": lineID, "quantity": "5"}}})
	if r.status != http.StatusCreated {
		t.Fatalf("the first fulfilment = %d: %s", r.status, r.raw)
	}
	inv1 := invoiceIDOf(t, r)
	var firstApplied, secondApplied int64
	for _, pd := range []struct {
		id   string
		dest *int64
	}{{first, &firstApplied}, {second, &secondApplied}} {
		if err := db.Pool.QueryRow(context.Background(),
			`SELECT COALESCE(SUM(ROUND(a.amount * 100)::bigint), 0) FROM ar_applications a
			 WHERE a.invoice_id = $1 AND a.payment_id = $2 AND a.reversed_at IS NULL`, inv1, pd.id).Scan(pd.dest); err != nil {
			t.Fatal(err)
		}
	}
	if firstApplied != 1000 || secondApplied != 1994 {
		t.Errorf("the first invoice applied %d of the first deposit and %d of the second, want 1000 and 1994", firstApplied, secondApplied)
	}

	// The second fulfilment bills the rest: an invoice of 2994, and the 6
	// cents the second deposit still holds apply to it.
	r = f.do("GET", "/api/v1/orders/"+id, nil)
	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer"})
	if r.status != http.StatusCreated {
		t.Fatalf("the second fulfilment = %d: %s", r.status, r.raw)
	}
	inv2 := invoiceIDOf(t, r)
	secondApplied = 0
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(ROUND(a.amount * 100)::bigint), 0) FROM ar_applications a
		 WHERE a.payment_id = $1 AND a.invoice_id = $2 AND a.reversed_at IS NULL`, second, inv2).Scan(&secondApplied); err != nil {
		t.Fatal(err)
	}
	if secondApplied != 6 {
		t.Errorf("the second invoice applied %d of the second deposit, want 6", secondApplied)
	}

	// The order answer holds nothing unapplied and both applications named.
	r = f.do("GET", "/api/v1/orders/"+id, nil)
	if got := num(t, r.body, "deposit_unapplied_cents"); got != 0 {
		t.Errorf("deposit_unapplied_cents = %d, want 0", got)
	}
	deps, _ := r.body["deposits"].([]any)
	if len(deps) != 2 {
		t.Fatalf("the order names %d deposits, want 2", len(deps))
	}
	totalApplied := int64(0)
	for _, d := range deps {
		dm, _ := d.(map[string]any)
		for _, a := range dm["applications"].([]any) {
			am, _ := a.(map[string]any)
			totalApplied += num(t, am, "amount_cents")
		}
	}
	if totalApplied != 3000 {
		t.Errorf("the deposits' applications sum to %d, want 3000", totalApplied)
	}
}

// bodyOfLine reads the order answer's first billable line, for its id.
func (f *fixture) bodyOfLine(t *testing.T, orderID string) map[string]any {
	t.Helper()
	r := f.do("GET", "/api/v1/orders/"+orderID, nil)
	if r.status != http.StatusOK {
		t.Fatalf("read the order = %d: %s", r.status, r.raw)
	}
	lines, _ := r.body["lines"].([]any)
	for _, l := range lines {
		lm, _ := l.(map[string]any)
		if lm["line_type"] != "product" {
			continue
		}
		return lm
	}
	t.Fatalf("no product line on the order")
	return nil
}

// RULE: a deposit larger than the order's balance due after deposits is
// refused with plain words, and so is one on a cancelled (voided) or fully
// invoiced order.
func TestCP07_DepositOverBalanceRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)

	f := newFixture(t, db)
	f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "3.2500")

	r := f.create() // total 5988
	id := str(t, r.body, "id")
	f.transition(id, 1, "confirmed")

	r = f.takeDeposit(id, 5988, "cash")
	if r.status != http.StatusCreated {
		t.Fatalf("a deposit of exactly the balance due = %d: %s", r.status, r.raw)
	}
	// The balance due after deposits is now 0: any further deposit is over.
	r = f.takeDeposit(id, 100, "cash")
	if r.status != http.StatusConflict {
		t.Fatalf("a deposit past the balance due = %d: %s, want 409", r.status, r.raw)
	}
	if msg, _ := r.body["error"].(map[string]any); true {
		m, _ := msg["message"].(string)
		if !strings.Contains(strings.ToLower(m), "balance") {
			t.Errorf("the refusal says %q, want plain words about the balance due", m)
		}
	}

	// A single deposit larger than the order's total is refused outright.
	r2 := f.create(f.line("10"))
	id2 := str(t, r2.body, "id")
	f.transition(id2, 1, "confirmed")
	r = f.takeDeposit(id2, 5989, "check")
	if r.status != http.StatusConflict {
		t.Fatalf("a deposit larger than the order = %d: %s, want 409", r.status, r.raw)
	}
}

func TestCP07_DepositOnVoidedOrderRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)

	f := newFixture(t, db)
	f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	f.stock(f.productID, "10")

	r := f.create() // total 5988
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "cancelled", "reason", "CP-07: the customer walked away")
	if r.status != http.StatusOK {
		t.Fatalf("cancel = %d: %s", r.status, r.raw)
	}

	r = f.takeDeposit(id, 1000, "cash")
	if r.status != http.StatusConflict {
		t.Fatalf("a deposit on a cancelled order = %d: %s, want 409", r.status, r.raw)
	}
	if msg, _ := r.body["error"].(map[string]any); true {
		m, _ := msg["message"].(string)
		if !strings.Contains(strings.ToLower(m), "cancelled") {
			t.Errorf("the refusal says %q, want plain words about the cancelled order", m)
		}
	}

	// A deposit on a fully invoiced order is refused with its own words.
	r2 := f.create(f.line("10"))
	id2 := str(t, r2.body, "id")
	r2 = f.transition(id2, 1, "confirmed")
	r2 = f.fulfil(id2, revision(t, r2), map[string]any{"picked_up_by": "Counter customer"})
	if r2.status != http.StatusCreated {
		t.Fatalf("fulfil = %d: %s", r2.status, r2.raw)
	}
	r = f.takeDeposit(id2, 1000, "cash")
	if r.status != http.StatusConflict {
		t.Fatalf("a deposit on a fully invoiced order = %d: %s, want 409", r.status, r.raw)
	}
	if msg, _ := r.body["error"].(map[string]any); true {
		m, _ := msg["message"].(string)
		if !strings.Contains(strings.ToLower(m), "invoiced") {
			t.Errorf("the refusal says %q, want plain words about the fully invoiced order", m)
		}
	}
}

// RULE: two deposits that together would pass the balance rule cannot both
// land: the check runs inside the payment's transaction on a locked order.
func TestCP07_DepositContendersCannotBothPassTheBalance(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)

	f := newFixture(t, db)
	f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	f.stock(f.productID, "10")

	r := f.create() // total 5988
	id := str(t, r.body, "id")
	f.transition(id, 1, "confirmed")

	var wg sync.WaitGroup
	statuses := make([]int, 3)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := f.doUnique("POST", "/api/v1/payments", map[string]any{
				"customer_id": f.customerID.String(), "order_id": id,
				"amount_cents": 4000, "method": "cash", "reference": "CP-07 contender",
			}, fmt.Sprintf("req-cp07-race-%d", i))
			statuses[i] = res.status
		}(i)
	}
	wg.Wait()
	created := 0
	for _, s := range statuses {
		if s == http.StatusCreated {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%d of 3 contending deposits of 4000 on a 5988 order landed, want exactly 1: %v", created, statuses)
	}
}

// RULE (ADR 0005 section 11 step 1): the deposit takes the order row with
// FOR NO KEY UPDATE, which stays compatible with the FOR KEY SHARE a foreign
// key insert takes on the row, so a deposit never waits behind one and never
// deadlocks a payment that names the same order.
func TestCP07_DepositDoesNotWaitBehindAnOrderKeyShare(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)

	f := newFixture(t, db)
	f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	f.stock(f.productID, "10")

	r := f.create()
	id := str(t, r.body, "id")
	f.transition(id, 1, "confirmed")

	// Hold the order's key share the way the allocation subscriber's request
	// insert takes it, then take a deposit: it must answer at once.
	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		err := db.RunInTx(context.Background(), func(ctx context.Context) error {
			// The lock belongs to this transaction: it goes through the
			// executor the context resolves, never the pool.
			if _, err := db.GetExecutor(ctx).Exec(ctx, `SELECT 1 FROM orders WHERE id = $1 FOR KEY SHARE`, id); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
		if err != nil {
			t.Errorf("hold the key share: %v", err)
		}
	}()
	<-held
	defer close(release)
	done := make(chan resp, 1)
	go func() { done <- f.takeDeposit(id, 100, "cash") }()
	select {
	case r := <-done:
		if r.status != http.StatusCreated {
			t.Fatalf("the deposit behind a key share = %d: %s, want 201", r.status, r.raw)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("the deposit waited behind a FOR KEY SHARE on the order row (ADR 0005 section 11 step 1 wants FOR NO KEY UPDATE)")
	}
}

// RULE: a deposit and a payment that names the same order with an
// application race without deadlocking: the clerk sees two payments, never a
// 500.
func TestCP07_DepositAndAnOrderedPaymentDoNotDeadlock(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)

	f := newFixture(t, db)
	f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "3.2500")

	r := f.create() // total 5988
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	lineID := str(t, f.bodyOfLine(t, id), "id")
	r = f.do("GET", "/api/v1/orders/"+id, nil)
	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer",
		"lines": []map[string]any{{"order_line_id": lineID, "quantity": "5"}}})
	if r.status != http.StatusCreated {
		t.Fatalf("fulfil half = %d: %s", r.status, r.raw)
	}
	inv := invoiceIDOf(t, r)

	var wg sync.WaitGroup
	statuses := make([]int, 2)
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		res := f.doUnique("POST", "/api/v1/payments", map[string]any{
			"customer_id": f.customerID.String(), "order_id": id,
			"amount_cents": 100, "method": "cash",
		}, "req-cp07-dl-a")
		statuses[0] = res.status
	}()
	go func() {
		defer wg.Done()
		<-start
		res := f.doUnique("POST", "/api/v1/payments", map[string]any{
			"customer_id": f.customerID.String(), "order_id": id,
			"amount_cents": 500, "method": "check",
			"applications": []map[string]any{{"invoice_id": inv, "amount_cents": 500}},
		}, "req-cp07-dl-b")
		statuses[1] = res.status
	}()
	close(start)
	wg.Wait()
	for i, s := range statuses {
		if s != http.StatusCreated {
			t.Errorf("contender %d = %d, want 201 (a deadlock on the order row shows as a 500)", i, s)
		}
	}
}

// RULE: the cash of a payment that names an order and carries applications
// obeys the deposit rules too, judged on the part that stays unapplied: the
// same money is not refused one way and held the other.
func TestCP07_UnappliedCashOfAnOrderedPaymentHonoursTheDepositRules(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)

	f := newFixture(t, db)
	f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "3.2500")

	r := f.create() // total 5988
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer"})
	if r.status != http.StatusCreated {
		t.Fatalf("fulfil = %d: %s", r.status, r.raw)
	}
	inv := invoiceIDOf(t, r)

	// The whole order is invoiced: a deposit is refused...
	r = f.takeDeposit(id, 1000, "cash")
	if r.status != http.StatusConflict {
		t.Fatalf("a deposit on the fully invoiced order = %d, want 409", r.status)
	}
	// ...and the same cash is not let in as the unapplied part of a payment
	// that names the order: 1000 applied, 2000 left over.
	r = f.doUnique("POST", "/api/v1/payments", map[string]any{
		"customer_id": f.customerID.String(), "order_id": id,
		"amount_cents": 3000, "method": "check",
		"applications": []map[string]any{{"invoice_id": inv, "amount_cents": 1000}},
	}, "req-cp07-app-rule")
	if r.status != http.StatusConflict {
		t.Fatalf("a payment leaving unapplied cash on the fully invoiced order = %d: %s, want 409", r.status, r.raw)
	}
	// A payment fully applied to the order's invoice is a payment, not a
	// deposit: it lands.
	r = f.doUnique("POST", "/api/v1/payments", map[string]any{
		"customer_id": f.customerID.String(), "order_id": id,
		"amount_cents": 1000, "method": "cash",
		"applications": []map[string]any{{"invoice_id": inv, "amount_cents": 1000}},
	}, "req-cp07-app-ok")
	if r.status != http.StatusCreated {
		t.Fatalf("a payment fully applied to the order's invoice = %d: %s, want 201", r.status, r.raw)
	}
	if got := num(t, r.body, "unapplied_cents"); got != 0 {
		t.Errorf("the fully applied payment holds %d unapplied, want 0", got)
	}
}
