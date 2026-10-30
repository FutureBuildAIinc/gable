// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// QE-07: the pre-ship gate on the order's own write routes. An order whose
// source quote carries an unresolved lumber-index exposure (the customer's
// policy is REQUIRE_ACK and the index moved past the threshold) is refused by
// the service with pricing.ErrUnresolvedExposure on confirm and on fulfil.
// The wire contract those two answers must keep is the one the exposure-gate
// route already answers: 409 conflict with the exposure payload as blockers
// (the blocker code and the quote), never a 500, and the same confirm goes
// through once the customer acknowledges the price change on the quote or an
// owner overrides the gate.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// stockAndMoney wires the fulfilment's collaborators the fulfil tests need:
// the real inventory service and the real invoice service over the GL and
// account ledger, the wire fixture's withStock and withMoney in one mod.
func stockAndMoney(db *database.DB) func(*order.Service) *order.Service {
	return func(s *order.Service) *order.Service {
		glSvc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
		acct := account.NewService(db, glSvc, slog.Default())
		inv := invoice.NewService(invoice.NewRepository(db), glSvc, acct, db).WithAuditLog(audit.NewLogger(db))
		return s.WithInventory(inventory.NewService(inventory.NewRepository(db))).WithInvoices(inv).WithDeposits(acct)
	}
}

// overriderForTest bridges pricing.ExposureService.OverrideForOrder to the
// order service's ExposureOverrider seam, exactly as the app's orderwire
// adapter does, without the test importing the app package.
type overriderForTest struct{ svc *pricing.ExposureService }

func (o *overriderForTest) OverrideForOrder(ctx context.Context, orderID uuid.UUID, notes, actor, role string) error {
	_, err := o.svc.OverrideForOrder(ctx, orderID, notes, actor, role)
	return err
}

// exposureFixture is the wire fixture plus the price-protection wiring the
// real server carries on these routes: the checker on the order service's
// confirm and fulfil paths, the exposure service behind the order
// exposure-override route, and the pricing exposure routes (the quote's
// acknowledge among them) on the same mux.
type exposureFixture struct {
	*fixture
	quoteID uuid.UUID
}

// newExposureFixture seeds a customer on the acknowledgement policy and a
// SENT quote rolled up to the state given: a commodity line with an active
// escalator on a seeded market index, so the exposure read, the acknowledge
// and the override all act on real rows.
func newExposureFixture(t *testing.T, db *database.DB, rollup string, mods ...func(*order.Service) *order.Service) *exposureFixture {
	t.Helper()
	f := newFixture(t, db)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %s: %v", sql, err)
		}
	}
	// The customer's snapshotted policy is what arms the gate.
	exec(`UPDATE customers SET price_escalation_policy = 'REQUIRE_ACK' WHERE id = $1`, f.customerID)
	var indexID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM market_indices WHERE index_code = 'RL_SPF_2X4'`).Scan(&indexID); err != nil {
		t.Fatalf("seed market index: %v", err)
	}
	quoteID, lineID, escalatorID := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO quotes (id, customer_id, project_id, state, total_amount, source,
	                          customer_notes, branch_id, exposure_state, exposure_dollars, created_at, updated_at)
	      VALUES ($1, $2, NULL, 'SENT', 550.00, 'manual', 'QE-07 fixture', $3, $4, 120.50, NOW(), NOW())`,
		quoteID, f.customerID, f.branchID, rollup)
	exec(`INSERT INTO quote_lines (id, quote_id, product_id, sku, description, customer_note,
	                              quantity, uom, unit_price, line_total)
	      VALUES ($1, $2, $3, $4, 'QE-07 commodity line', 'none', 100, 'PCS', 5.50, 550.00)`,
		lineID, quoteID, f.productID, f.sku)
	exec(`INSERT INTO price_escalators (id, quote_line_id, market_index_id, escalation_type, escalation_rate,
	                                   base_price, base_index_value, effective_date, expiration_date,
	                                   current_state, policy_at_snapshot, threshold_pct_at_snapshot, is_active)
	      VALUES ($1, $2, $3, 'INDEX_DELTA', 0, 5.50, 412.00, CURRENT_DATE, CURRENT_DATE + 365,
	              $4, 'REQUIRE_ACK', 5.0, TRUE)`, escalatorID, lineID, indexID, rollup)

	// The gate and the routes the dealer clears it through, wired the way
	// orderwire.New and serve's wireExposure wire them.
	logger := slog.Default()
	exposureRepo := pricing.NewExposureRepository(db)
	escalators := pricing.NewEscalatorRepository(db)
	quoteRepo := quote.NewRepository(db)
	checker := pricing.NewExposureChecker(db)
	exposureSvc := pricing.NewExposureService(exposureRepo, escalators, quoteRepo, nil, checker, logger).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	scanner := pricing.NewExposureScanner(exposureRepo, escalators, quoteRepo, nil, db, logger).
		WithOutbox(outbox.NewWriter(db, ""))

	svc := order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithAuditLog(audit.NewLogger(db)).WithExposureGate(checker, &overriderForTest{svc: exposureSvc})
	for _, mod := range mods {
		svc = mod(svc)
	}
	mux := http.NewServeMux()
	order.NewHandler(svc).RegisterRoutes(mux)
	pricing.NewExposureHandler(scanner, checker, exposureRepo, exposureSvc).RegisterRoutes(mux)
	srv := httptest.NewServer(middleware.Idempotency(db)(middleware.NewBranchMiddleware(db).Handler(mux)))
	f.srv = srv

	ef := &exposureFixture{fixture: f, quoteID: quoteID}
	t.Cleanup(func() {
		srv.Close()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quote_exposure_events WHERE quote_id = $1`, quoteID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM price_escalators WHERE id = $1`, escalatorID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quote_lines WHERE id = $1`, lineID)
		_, _ = db.Pool.Exec(ctx, `UPDATE orders SET quote_id = NULL WHERE quote_id = $1`, quoteID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE id = $1`, quoteID)
	})
	return ef
}

// linkedToQuote points the order at its source quote, the one act the quote
// convert performs that these tests need: the gate resolves the order's quote.
func (f *exposureFixture) linkedToQuote(t *testing.T, orderID string) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(), `UPDATE orders SET quote_id = $2 WHERE id = $1`, orderID, f.quoteID); err != nil {
		t.Fatalf("link order to quote: %v", err)
	}
}

// blocker finds the detail entry whose code is the one named.
func blocker(t *testing.T, details []map[string]any, code string) map[string]any {
	t.Helper()
	for _, d := range details {
		if d["code"] == code {
			return d
		}
	}
	t.Fatalf("no %s blocker in %v", code, details)
	return nil
}

// RULE (QE-07, the gate on the order's own transition): confirming an order
// whose source quote holds an unresolved exposure answers 409 conflict with
// the exposure payload as blockers, the blocker code and the quote among
// them, never a 500.
func TestQE07_ConfirmBlockedByExposureAnswers409(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newExposureFixture(t, db, "ACK_REQUIRED")

	r := f.create()
	id := str(t, r.body, "id")
	f.linkedToQuote(t, id)

	r = f.transition(id, 1, "confirmed")
	if r.status != http.StatusConflict {
		t.Fatalf("confirm on an exposure-held order = %d, want 409: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "conflict" {
		t.Errorf("code = %q, want conflict", code)
	}
	// The blocker code the desk branches on, and the quote it names.
	if b := blocker(t, details, "exposure_code"); b["message"] != "UNRESOLVED_EXPOSURE" {
		t.Errorf("exposure_code blocker = %v, want UNRESOLVED_EXPOSURE", b["message"])
	}
	found := false
	for _, d := range details {
		if msg, _ := d["message"].(string); strings.Contains(msg, f.quoteID.String()) {
			found = true
		}
	}
	if !found {
		t.Errorf("no blocker carries the source quote %s: %v", f.quoteID, details)
	}
	// The refused confirm moved nothing: the order is still a draft on its revision.
	g := f.do("GET", "/api/v1/orders/"+id, nil)
	if str(t, g.body, "status") != "draft" || revision(t, g) != 1 {
		t.Errorf("the refused confirm moved the order: %s", g.raw)
	}
}

// RULE (the same gate on the fulfilment): an order confirmed while its quote
// was clear is refused at fulfil once the index moves past the threshold, the
// same 409 with the exposure blockers, never a 500.
func TestQE07_FulfilBlockedByExposureAnswers409(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newExposureFixture(t, db, "OK", stockAndMoney(db))
	f.cleanMoney()
	f.stock(f.productID, "10")

	r := f.create()
	id := str(t, r.body, "id")
	f.linkedToQuote(t, id)

	// Confirmed while the quote was clear...
	r = f.transition(id, 1, "confirmed")
	if r.status != 200 {
		t.Fatalf("confirm with a clear quote = %d: %s", r.status, r.raw)
	}
	// ...then the index moves past the threshold and the scanner rolls the
	// quote up to ACK_REQUIRED before the order ships.
	if _, err := db.Pool.Exec(context.Background(),
		`UPDATE quotes SET exposure_state = 'ACK_REQUIRED', exposure_dollars = 120.50 WHERE id = $1`, f.quoteID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(),
		`UPDATE price_escalators SET current_state = 'ACK_REQUIRED' WHERE quote_line_id IN (SELECT id FROM quote_lines WHERE quote_id = $1)`, f.quoteID); err != nil {
		t.Fatal(err)
	}

	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer"})
	if r.status != http.StatusConflict {
		t.Fatalf("fulfil on an exposure-held order = %d, want 409: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "conflict" {
		t.Errorf("code = %q, want conflict", code)
	}
	if b := blocker(t, details, "exposure_code"); b["message"] != "UNRESOLVED_EXPOSURE" {
		t.Errorf("exposure_code blocker = %v, want UNRESOLVED_EXPOSURE", b["message"])
	}
	// Nothing billed: the refused fulfilment left no invoice behind.
	var invoices int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE customer_id = $1`, f.customerID).Scan(&invoices); err != nil {
		t.Fatal(err)
	}
	if invoices != 0 {
		t.Errorf("%d invoices for a refused fulfilment, want none", invoices)
	}
}

// RULE (clearing the gate, path one): once the customer acknowledges the
// price change on the quote, the same confirm goes through.
func TestQE07_ConfirmAfterAcknowledgeSucceeds(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newExposureFixture(t, db, "ACK_REQUIRED")

	r := f.create()
	id := str(t, r.body, "id")
	f.linkedToQuote(t, id)

	if bad := f.transition(id, 1, "confirmed"); bad.status != http.StatusConflict {
		t.Fatalf("confirm before the acknowledgment = %d, want 409: %s", bad.status, bad.raw)
	}
	ack := f.do("POST", "/api/v1/quotes/"+f.quoteID.String()+"/exposure/acknowledge",
		map[string]any{"method": "VERBAL", "notes": "customer accepted the new price by phone"})
	if ack.status != 200 {
		t.Fatalf("acknowledge = %d: %s", ack.status, ack.raw)
	}
	// The order's revision did not move: the same precondition confirms.
	r = f.transition(id, 1, "confirmed")
	if r.status != 200 || str(t, r.body, "status") != "confirmed" {
		t.Fatalf("confirm after the acknowledgment = %d %q: %s", r.status, r.body["status"], r.raw)
	}
}

// RULE (clearing the gate, path two): an owner override recorded through the
// order's own override route clears the quote's exposure and the confirm goes
// through.
func TestQE07_ConfirmAfterOverrideSucceeds(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newExposureFixture(t, db, "ACK_REQUIRED")

	r := f.create()
	id := str(t, r.body, "id")
	f.linkedToQuote(t, id)

	if bad := f.transition(id, 1, "confirmed"); bad.status != http.StatusConflict {
		t.Fatalf("confirm before the override = %d, want 409: %s", bad.status, bad.raw)
	}
	ov := f.do("POST", "/api/v1/orders/"+id+"/exposure-override",
		map[string]any{"notes": "owner accepts the margin risk on this order"})
	if ov.status != 200 {
		t.Fatalf("exposure override = %d: %s", ov.status, ov.raw)
	}
	r = f.transition(id, 1, "confirmed")
	if r.status != 200 || str(t, r.body, "status") != "confirmed" {
		t.Fatalf("confirm after the override = %d %q: %s", r.status, r.body["status"], r.raw)
	}
}
