// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The counter end to end (ADR 0005 section 14.2 C2-5): a real Postgres, the
// real invoice, inventory, general ledger and account services wired as
// serve wires them, the real pos handler on one mux behind the real
// idempotency and branch middleware. Requests are JSON and responses are
// read back as JSON, so each test states a wire fact.

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
	"github.com/gablelbm/gable/internal/bankrecon"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/scale"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type fixture struct {
	t *testing.T
	// cashierSub, when set, is the subject every request that names a random
	// one acts as: a test that runs the no header rule (a user with no grants
	// is refused everywhere) grants it the sale's branch.
	cashierSub   string
	db           *database.DB
	srv          *httptest.Server
	service      *pos.Service
	ledger       *gl.Service
	customerID   uuid.UUID
	customerName string
	productID    uuid.UUID
	kitID        uuid.UUID
	sku          string
	register     string
	branchID     uuid.UUID
	yardID       uuid.UUID
	events       *recordingWriter
	priorEntries map[string]bool
	// largeCash is the branch country's large cash rules (CP-17a), wired
	// into the fixture's counter the way serve wires them.
	largeCash *largecash.Service
	// paymentGateway, when an opt sets it, wires the payment route's card
	// refunds at a terminal too, the way serve wires Run Payments.
	paymentGateway payment.PaymentGateway
	// mounts add routes to the fixture's mux after the counter's own, the way
	// serve mounts the receipt pair of the document handler beside them.
	mounts []func(f *fixture, mux *http.ServeMux)
	// terminal, when an opt sets it, is the simulated card terminal the
	// counter's card legs ride (CP-05), with its intent ledger.
	terminal *payment.SimulatedTerminal
	// noAutoTill, when an opt sets it, stops startSale from opening the
	// register's drawer: the API refuses a sale at a register with no open
	// till (CP-08), so the helper opens one with no float, as a cashier would,
	// unless the test is proving that refusal.
	noAutoTill bool
	// scale and scaleDeviceID, when the withScale opt sets them, are the
	// simulated scale the counter reads and the device row the ticket names
	// (CP-19).
	scale         *scale.Simulator
	scaleDeviceID uuid.UUID
}

// withoutTill is the opt of a test that proves the open till rule itself.
func withoutTill() func(*fixture) { return func(f *fixture) { f.noAutoTill = true } }

const defaultBranch = `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`

// lcTestVaultKey seals the payer's sensitive values in the counter's tests
// (CP-17a): an obvious pattern, never a real key shape.
const lcTestVaultKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// recordingWriter wraps the outbox writer, remembering what was written and
// optionally failing one type (the transaction proofs). Contenders write
// through it from several goroutines, so its state is behind a mutex; a test
// sets the failing type with setFail.
type recordingWriter struct {
	inner pos.EventRecorder
	mu    sync.Mutex
	fail  string
	seen  []string
}

// setFail names the event type that fails, "" for none.
func (w *recordingWriter) setFail(eventType string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fail = eventType
}

func (w *recordingWriter) Write(ctx context.Context, ev outbox.Event) error {
	w.mu.Lock()
	if ev.Type == w.fail {
		w.mu.Unlock()
		return fmt.Errorf("outbox insert failed")
	}
	w.seen = append(w.seen, ev.Type)
	w.mu.Unlock()
	return w.inner.Write(ctx, ev)
}

// newFixture builds a walk-in-ready branch: a customer, a product costing
// 3.25 and priced 5.50 with 100 on hand (the branch rate is 8.875 percent),
// a kit of two of them, and serves the pos routes. opts tune the service
// before it is served.
//
// The counter stands on a branch of the fixture's own: a private locations
// row of type BRANCH carrying its own tax rate and its own cash rounding
// rule, its own yard, register, stock and customer, every one of them
// removed when the test ends. No other package's fixture writes that
// branch's row, so no other fixture can move the rate or the rule a test
// here depends on, and the counter's own rows (its sales, its terminal
// intents, its till movements) are named by ids only this fixture knows.
func newFixture(t *testing.T, db *database.DB, opts ...func(*fixture)) *fixture {
	t.Helper()
	return newCounterFixture(t, db, opts...)
}

// newCounterFixture builds the counter on a branch the fixture owns.
func newCounterFixture(t *testing.T, db *database.DB, opts ...func(*fixture)) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	f := &fixture{t: t, db: db, customerID: uuid.New(), productID: uuid.New(), kitID: uuid.New(),
		sku: "POS-" + uuid.NewString()[:8], register: "REG-" + uuid.NewString()[:8], scaleDeviceID: uuid.New()}
	// the branch the fixture owns: its own rate and its own rounding rule,
	// on its own row, where no other fixture can reach them
	f.branchID = uuid.New()
	mustExec(t, db, `INSERT INTO locations (id, type, code, name, timezone, default_tax_rate, cash_rounding_mode)
		VALUES ($1, 'BRANCH', $2, 'Counter own branch', 'America/New_York', 0.088750, 'OFF')`,
		f.branchID, "BR-"+f.branchID.String()[:8])
	// the customer's name carries a token of this fixture, so a lookup by
	// name can never meet another run's customer (the lookup tests assert on
	// their own id, never on a global name count)
	f.customerName = "Counter Co " + uuid.NewString()[:8]
	mustExec(t, db, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`,
		f.customerID, f.customerName, "WPOS-"+uuid.NewString()[:8], f.branchID)
	mustExec(t, db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost) VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5, 3.25)`,
		f.productID, f.sku)
	mustExec(t, db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost, is_kit) VALUES ($1, $2, 'bundle kit', 'EA', 11.00, 6.50, TRUE)`,
		f.kitID, "KIT-"+uuid.NewString()[:8])
	mustExec(t, db, `INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position) VALUES ($1, $2, 2, 0)`,
		f.kitID, f.productID)
	f.yardID = uuid.New()
	mustExec(t, db, `INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`,
		f.yardID, "PY-"+f.yardID.String()[:8], f.branchID)
	mustExec(t, db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 100, 0)`, f.productID, f.yardID)
	mustExec(t, db, `INSERT INTO pos_registers (id, location_id, name, branch_id) VALUES ($1, $2, 'Counter test', $3) ON CONFLICT (id) DO UPDATE SET location_id = EXCLUDED.location_id, branch_id = EXCLUDED.branch_id`, f.register, f.yardID, f.branchID)

	logger := slog.Default()
	glSvc := gl.NewService(gl.NewRepository(db), nil, logger)
	f.ledger = glSvc
	acct := account.NewService(db, glSvc, logger)
	stock := inventory.NewService(inventory.NewRepository(db))
	invoices := invoice.NewService(invoice.NewRepository(db), glSvc, acct, db).
		WithAuditLog(audit.NewLogger(db)).WithOutbox(outbox.NewWriter(db, "")).WithStock(stock)
	f.events = &recordingWriter{inner: outbox.NewWriter(db, "")}
	lcVault, verr := payment.NewVault(lcTestVaultKey)
	if verr != nil {
		t.Fatalf("large cash vault: %v", verr)
	}
	f.largeCash = largecash.NewService(db, lcVault, lcTestVaultKey)
	f.service = pos.NewService(db, pos.NewRepository(db), logger).
		WithInvoices(invoices).WithAR(acct).WithInventory(stock).WithLedger(glSvc).
		WithAuditLog(audit.NewLogger(db)).WithOutbox(f.events).WithTxRunner(db).
		WithBankLines(bankrecon.NewDepositLines(db)).WithLargeCash(f.largeCash)
	// the counter guards the AR core's applications as serve wires it (C2-5):
	// an invoice whose sale's void has standing claims takes no application,
	// and a payment whose unapplied cash is a declined void tender's credit
	// applies to no invoice (held to its tender)
	acct.WithInvoiceApplyGuard(f.service)
	acct.WithTenderCreditGuard(f.service)
	for _, o := range opts {
		o(f)
	}

	// the ledger entries that exist before this fixture's acts: its balance
	// reads exclude them, so one test never counts another's entries.
	f.priorEntries = map[string]bool{}
	if rows, err := db.Pool.Query(ctx, `SELECT id::text FROM gl_journal_entries`); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				f.priorEntries[id] = true
			}
		}
		rows.Close()
	}

	mux := http.NewServeMux()
	// the branch guard serve passes the counter (r12 P1-2): the resolution
	// route holds the claim's sale to the caller's grants, so the fixture
	// proves the same hold serve proves
	pos.NewHandler(f.service).WithBranchGuard(middleware.NewBranchGuard(db)).RegisterRoutes(mux)
	// the fixture mounts no branch middleware, so the preview's guard admits
	// every sale; the wall itself is proved in serve (the large cash preview
	// wall test)
	largecash.NewHandler(f.largeCash).WithBranchGuard(admitEverySale{}).RegisterRoutes(mux, nil,
		func(next http.Handler) http.Handler { return next },
		func(next http.Handler) http.Handler { return next })
	invoice.NewHandler(invoices).RegisterRoutes(mux)
	// the payment route, as serve mounts it: the contention tests race real
	// walk-in payments against the sales, and a paymentGateway opt wires its
	// card refunds at a terminal the way serve wires Run Payments
	payments := payment.NewService(db, payment.NewRepository(db), acct)
	if f.paymentGateway != nil {
		payments = payments.WithGateway(f.paymentGateway, "")
	}
	// the payment refund route holds a counter tender's credit to the
	// terminal rail as serve wires it (C2-5, FROM-PRINCIPAL 60b)
	payments = payments.WithTenderCreditGuard(f.service)
	payment.NewHandler(payments).RegisterRoutes(mux)
	for _, m := range f.mounts {
		m(f, mux)
	}
	f.srv = httptest.NewServer(middleware.Idempotency(db)(roleClaims(middleware.NewBranchMiddleware(db).Handler(mux))))
	t.Cleanup(func() {
		f.srv.Close()
		f.cleanup()
	})
	return f
}

func mustExec(t *testing.T, db *database.DB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
	}
}

// roleClaims stands in for authentication: X-Test-Role becomes the claims a
// JWT would carry, X-Test-Roles a roles-only token (no single role field),
// and X-Test-Sub its subject.
func roleClaims(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role := r.Header.Get("X-Test-Role"); role != "" {
			claims := &middleware.UserClaims{Role: role, Roles: []string{role}}
			claims.Subject = r.Header.Get("X-Test-Sub")
			claims.Email = r.Header.Get("X-Test-Email")
			r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, claims))
		} else if roles := r.Header.Get("X-Test-Roles"); roles != "" {
			claims := &middleware.UserClaims{Roles: strings.Split(roles, ",")}
			claims.Subject = r.Header.Get("X-Test-Sub")
			claims.Email = r.Header.Get("X-Test-Email")
			r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, claims))
		}
		next.ServeHTTP(w, r)
	})
}

// cleanup removes everything the fixture produced, children first, in the
// foreign key order the sweep proved.
//
// Every scope names the fixture's own rows: the branch it owns, the customer
// it made, the register it opened. A delete that reached a whole table would
// take another package's rows: the CI Backend job runs every package at the
// same time against one test database, and the payment, serve and worker
// packages write pos_transactions and terminal intents of their own.
func (f *fixture) cleanup() {
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := f.db.Pool.Exec(ctx, sql, args...); err != nil {
			f.t.Logf("cleanup: %s: %v", strings.Join(strings.Fields(sql), " "), err)
		}
	}
	walkIn := `(SELECT id FROM customers WHERE account_number = 'WALK-IN')`
	// The scopes. $1 is the branch the fixture owns, $2 the customer it
	// made, $3 the register it opened. A statement takes only the
	// parameters it uses: a parameter no statement mentions has no type for
	// the server to deduce.
	//
	// The counter's own rows are found by the register as well as by the
	// branch: a return the product writes names the sale it comes from and
	// leaves the branch empty, and a test that moves a sale to another
	// branch leaves the register as the only name the fixture still knows.
	sales := `(SELECT id FROM pos_transactions WHERE branch_id = $1 OR customer_id = $2 OR register_id = $3)`
	pays := `(SELECT id FROM payments WHERE branch_id = $1 OR customer_id = $2
		OR id IN (SELECT payment_id FROM pos_tenders WHERE transaction_id IN ` + sales + `))`
	tenders := `(SELECT id FROM pos_tenders WHERE transaction_id IN ` + sales + ` OR payment_id IN ` + pays + `)`
	returns := `(SELECT id FROM pos_returns WHERE branch_id = $1 OR register_id = $3 OR customer_id = $2
		OR original_transaction_id IN ` + sales + `)`
	invs := `(SELECT id FROM invoices WHERE branch_id = $1 OR customer_id = $2
		OR id IN (SELECT invoice_id FROM pos_transactions WHERE register_id = $3))`
	cms := `(SELECT id FROM credit_memos WHERE branch_id = $1 OR customer_id = $2
		OR id IN (SELECT credit_memo_id FROM pos_returns WHERE register_id = $3))`
	// the same scopes, spelled as text: the outbox and the audit log name
	// their entity by a text id
	salesTxt := `(SELECT id::text FROM pos_transactions WHERE branch_id = $1 OR customer_id = $2 OR register_id = $3)`
	invsTxt := `(SELECT id::text FROM invoices WHERE branch_id = $1 OR customer_id = $2
		OR id IN (SELECT invoice_id FROM pos_transactions WHERE register_id = $3))`
	cmsTxt := `(SELECT id::text FROM credit_memos WHERE branch_id = $1 OR customer_id = $2
		OR id IN (SELECT credit_memo_id FROM pos_returns WHERE register_id = $3))`
	paysTxt := `(SELECT id::text FROM payments WHERE branch_id = $1 OR customer_id = $2
		OR id IN (SELECT payment_id FROM pos_tenders WHERE transaction_id IN ` + sales + `))`
	returnsTxt := `(SELECT id::text FROM pos_returns WHERE branch_id = $1 OR register_id = $3 OR customer_id = $2
		OR original_transaction_id IN ` + sales + `)`
	sessionsTxt := `(SELECT id::text FROM till_sessions WHERE register_id = $3)`
	// the documents the fixture's acts wrote: what its ledger entries, its
	// applications and its refunds name
	docs := `((SELECT id FROM invoices WHERE branch_id = $1 OR customer_id = $2)
		UNION (SELECT id FROM credit_memos WHERE branch_id = $1 OR customer_id = $2)
		UNION (SELECT id FROM payments WHERE branch_id = $1 OR customer_id = $2)
		UNION (SELECT id FROM pos_transactions WHERE branch_id = $1 OR customer_id = $2 OR register_id = $3)
		UNION (SELECT id FROM pos_returns WHERE branch_id = $1 OR customer_id = $2 OR register_id = $3)
		UNION (SELECT id FROM ar_applications WHERE invoice_id IN ` + invs + ` OR payment_id IN ` + pays + ` OR credit_memo_id IN ` + cms + `)
		UNION (SELECT id FROM payment_refunds WHERE payment_id IN ` + pays + ` OR credit_memo_id IN ` + cms + `))`
	// the ledger entries are captured before their source documents go, so
	// the entry delete never misses one that named a payment or an
	// application deleted below
	var entryIDs []string
	// Every entry the fixture's documents own, and every reversal of one of
	// those entries (a void reverses receipt and application entries too, and
	// a reversal whose original goes would trip the reverses foreign key and
	// strand the whole family).
	if rows, err := f.db.Pool.Query(ctx, `SELECT e.id::text FROM gl_journal_entries e WHERE e.source_ref_id::text IN
		(SELECT id::text FROM invoices WHERE id IN `+invs+`)
		OR e.source_ref_id::text IN (SELECT id::text FROM credit_memos WHERE id IN `+cms+`)
		OR e.source_ref_id::text IN (SELECT id::text FROM payments WHERE id IN `+pays+`)
		OR e.source_ref_id::text IN (SELECT id::text FROM ar_applications WHERE invoice_id IN `+invs+`)
		OR e.source_ref_id::text IN (SELECT id::text FROM payment_refunds WHERE payment_id IN `+pays+` OR credit_memo_id IN `+cms+`)
		OR e.source_ref_id::text IN (SELECT id::text FROM till_movements WHERE branch_id = $1)
		OR e.source_ref_id::text IN (SELECT id::text FROM till_day_closes WHERE branch_id = $1)
		OR e.reverses_entry_id IN (SELECT gl_entry_id FROM till_day_closes WHERE branch_id = $1 AND gl_entry_id IS NOT NULL)
		OR e.source_ref_id::text IN (SELECT id::text FROM till_sessions WHERE register_id = $3)
		OR e.reverses_entry_id IN (SELECT o.id FROM gl_journal_entries o WHERE o.source_ref_id::text IN
			((SELECT id::text FROM invoices WHERE id IN `+invs+`)
			UNION (SELECT id::text FROM credit_memos WHERE id IN `+cms+`)
			UNION (SELECT id::text FROM payments WHERE id IN `+pays+`)
			UNION (SELECT id::text FROM ar_applications WHERE invoice_id IN `+invs+`)
			UNION (SELECT id::text FROM payment_refunds WHERE payment_id IN `+pays+` OR credit_memo_id IN `+cms+`)))`,
		f.branchID, f.customerID, f.register); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				entryIDs = append(entryIDs, id)
			}
		}
		rows.Close()
	}
	exec(`DELETE FROM events_outbox WHERE entity_id::text IN `+invsTxt+`
		OR entity_id::text IN `+cmsTxt+`
		OR entity_id::text IN `+paysTxt+`
		OR entity_id::text IN `+salesTxt+`
		OR entity_id::text IN `+returnsTxt+`
		OR entity_id::text IN `+sessionsTxt+`
		OR entity_id::text IN (SELECT id::text FROM till_movements WHERE branch_id = $1)
		OR entity_id::text IN (SELECT id::text FROM till_day_closes WHERE branch_id = $1) OR entity_id = $4`,
		f.branchID, f.customerID, f.register, f.customerID.String())
	exec(`DELETE FROM audit_log WHERE entity_id::text IN `+invsTxt+`
		OR entity_id::text IN `+cmsTxt+`
		OR entity_id::text IN `+salesTxt+`
		OR entity_id::text IN `+returnsTxt+`
		OR entity_id::text IN `+sessionsTxt+`
		OR entity_id::text IN (SELECT id::text FROM till_movements WHERE branch_id = $1)
		OR entity_id::text IN (SELECT id::text FROM till_day_closes WHERE branch_id = $1)
		OR entity_id = $1`,
		f.branchID, f.customerID, f.register)
	// the subledger goes before the applications and the refunds it names:
	// once those rows are gone the walk-in scope below could not find them,
	// and the fixture's sales would leave orphaned rows on the shared
	// customer. The walk-in row itself is shared by every counter in the
	// deployment, so its rows are named by the documents and not by the
	// customer; its balance follows its subledger down by exactly what the
	// deleted rows carried and is never reset.
	exec(`UPDATE customers SET balance_due = balance_due - COALESCE((
		SELECT SUM(amount)::numeric / 100 FROM customer_transactions
		WHERE customer_id IN `+walkIn+` AND reference_id IN `+docs+`), 0)
		WHERE id IN (SELECT id FROM customers WHERE account_number = 'WALK-IN')`,
		f.branchID, f.customerID, f.register)
	exec(`DELETE FROM customer_transactions WHERE customer_id = $2
	OR (customer_id IN `+walkIn+` AND reference_id IN `+docs+`)`,
		f.branchID, f.customerID, f.register)
	exec(`DELETE FROM ar_applications WHERE invoice_id IN `+invs+` OR payment_id IN `+pays+` OR credit_memo_id IN `+cms,
		f.branchID, f.customerID, f.register)
	exec(`DELETE FROM payment_refunds WHERE payment_id IN `+pays+` OR credit_memo_id IN `+cms,
		f.branchID, f.customerID, f.register)
	exec(`DELETE FROM payment_terminal_lost_items WHERE branch_id = $1`, f.branchID)
	exec(`DELETE FROM payment_terminal_intents WHERE branch_id = $1`, f.branchID)
	exec(`DELETE FROM pos_account_overrides WHERE sale_id IN `+sales, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM pos_signatures WHERE sale_id IN `+sales, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM pos_refund_intents WHERE sale_id IN `+sales+` OR tender_id IN `+tenders,
		f.branchID, f.customerID, f.register)
	exec(`DELETE FROM pos_return_lines WHERE return_id IN `+returns, f.branchID, f.customerID, f.register)
	// the scale ticket and its line hold each other's keys; the links go
	// before either side (CP-19). Only the fixture's own rows are unlinked.
	exec(`UPDATE scale_tickets SET sale_line_id = NULL WHERE branch_id = $1 AND sale_line_id IS NOT NULL`, f.branchID)
	exec(`UPDATE scale_tickets SET weigh_in_ticket_id = NULL WHERE branch_id = $1 AND weigh_in_ticket_id IS NOT NULL`, f.branchID)
	exec(`UPDATE pos_line_items SET scale_ticket_id = NULL WHERE transaction_id IN `+sales+` AND scale_ticket_id IS NOT NULL`,
		f.branchID, f.customerID, f.register)
	exec(`DELETE FROM pos_line_items WHERE transaction_id IN `+sales+`
		OR invoice_line_id IN (SELECT id FROM invoice_lines WHERE invoice_id IN `+invs+`)`,
		f.branchID, f.customerID, f.register)
	// the return and the sale name each other; the link goes first
	exec(`UPDATE pos_transactions SET exchange_return_id = NULL WHERE id IN `+sales, f.branchID, f.customerID, f.register)
	exec(`UPDATE pos_returns SET exchange_sale_id = NULL WHERE id IN `+returns, f.branchID, f.customerID, f.register)
	exec(`UPDATE pos_returns SET credit_memo_id = NULL WHERE id IN `+returns, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM pos_returns WHERE id IN `+returns, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM pos_tenders WHERE id IN `+tenders, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM pos_transactions WHERE id IN `+sales, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM tender_lines WHERE invoice_id IN `+invs+` OR payment_id IN `+pays, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM payments WHERE id IN `+pays, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM credit_memo_lines WHERE credit_memo_id IN `+cms, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM credit_memos WHERE id IN `+cms, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM line_tally_rows WHERE invoice_line_id IN (SELECT id FROM invoice_lines WHERE invoice_id IN `+invs+`)
		OR credit_memo_line_id IN (SELECT id FROM credit_memo_lines WHERE credit_memo_id IN `+cms+`)
		OR pos_line_item_id IN (SELECT id FROM pos_line_items WHERE transaction_id IN `+sales+`)`,
		f.branchID, f.customerID, f.register)
	exec(`DELETE FROM invoice_lines WHERE invoice_id IN `+invs, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM invoices WHERE id IN `+invs, f.branchID, f.customerID, f.register)
	exec(`DELETE FROM ar_finance_charge_items WHERE invoice_id IN `+invs+` OR journal_entry_id = ANY($4::uuid[])`,
		f.branchID, f.customerID, f.register, entryIDs)
	// the counter's own acts posted (movements, variances) are named
	// by their rows: release them before the entries go
	exec(`DELETE FROM till_movements WHERE branch_id = $1`, f.branchID)
	exec(`DELETE FROM till_pickup_approvals WHERE till_session_id IN (SELECT id FROM till_sessions WHERE register_id = $1 OR branch_id = $2)`, f.register, f.branchID)
	exec(`DELETE FROM till_day_closes WHERE branch_id = $1`, f.branchID)
	exec(`UPDATE bank_transactions SET matched_journal_entry_id = NULL WHERE matched_journal_entry_id = ANY($1::uuid[])`, entryIDs)
	exec(`UPDATE till_sessions SET gl_entry_id = NULL WHERE (register_id = $1 OR branch_id = $2) AND gl_entry_id IS NOT NULL`, f.register, f.branchID)
	if len(entryIDs) > 0 {
		exec(`DELETE FROM gl_journal_lines WHERE journal_entry_id = ANY($1::uuid[])`, entryIDs)
		exec(`DELETE FROM gl_journal_entries WHERE id = ANY($1::uuid[])`, entryIDs)
	}

	// the large cash rows of the branch the fixture owns (CP-17a), not the
	// whole tables: the serve package's large cash tests write rows of
	// their own against the same database
	exec(`DELETE FROM large_cash_events WHERE branch_id = $1`, f.branchID)
	exec(`DELETE FROM large_cash_tenders WHERE branch_id = $1`, f.branchID)
	exec(`DELETE FROM large_cash_payers WHERE branch_id = $1`, f.branchID)
	exec(`DELETE FROM pos_sync_log WHERE register_id = $1`, f.register)
	exec(`DELETE FROM till_variance_policy WHERE branch_id = $1`, f.branchID)
	exec(`DELETE FROM till_z_reports WHERE register_id = $1 OR branch_id = $2`, f.register, f.branchID)
	exec(`DELETE FROM till_sessions WHERE register_id = $1 OR branch_id = $2`, f.register, f.branchID)
	exec(`DELETE FROM email_deliveries WHERE branch_id = $1`, f.branchID)
	exec(`DELETE FROM inventory WHERE location_id = $1`, f.yardID)
	exec(`DELETE FROM scale_tickets WHERE branch_id = $1`, f.branchID)
	exec(`DELETE FROM scale_devices WHERE branch_id = $1`, f.branchID)
	exec(`DELETE FROM pos_registers WHERE id = $1 OR branch_id = $2`, f.register, f.branchID)
	exec(`DELETE FROM locations WHERE id = $1`, f.yardID)
	exec(`DELETE FROM product_kit_components WHERE kit_product_id = $1`, f.kitID)
	exec(`DELETE FROM projects WHERE customer_id = $1`, f.customerID)
	// the customer's own rows: the links, the limits, the alerts and the
	// holds the counter wrote for it
	exec(`DELETE FROM customer_alerts WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM credit_holds WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM credit_limit_changes WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM customer_branches WHERE customer_id = $1 OR branch_id = $2`, f.customerID, f.branchID)
	exec(`DELETE FROM customer_contacts WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM customer_ship_tos WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM customer_users WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM tax_exemptions WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM pricing_rules WHERE customer_id = $1`, f.customerID)
	exec(`DELETE FROM crm_activities WHERE customer_id = $1`, f.customerID)
	exec(`UPDATE customers SET balance_due = 0 WHERE id = $1`, f.customerID)
	exec(`DELETE FROM products WHERE id = $1 OR id = $2`, f.productID, f.kitID)
	exec(`DELETE FROM customers WHERE id = $1`, f.customerID)
	f.cleanupOwnBranch()
}

// cleanupOwnBranch removes the branch the fixture owns, children first: the
// ledger rows that name it, the links that point at it, and the row itself.
// Every row it touches names this fixture's own branch, so another
// package's rows are never reached.
func (f *fixture) cleanupOwnBranch() {
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := f.db.Pool.Exec(ctx, sql, args...); err != nil {
			f.t.Logf("cleanup: %s: %v", strings.Join(strings.Fields(sql), " "), err)
		}
	}
	// the ledger first: an entry at this branch, the lines under it, and
	// the reversal that names it from either side
	exec(`UPDATE bank_transactions SET matched_journal_entry_id = NULL WHERE matched_journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, f.branchID)
	exec(`DELETE FROM gl_journal_lines WHERE branch_id = $1 OR journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, f.branchID)
	exec(`UPDATE gl_journal_entries SET reverses_entry_id = NULL WHERE reverses_entry_id IN (SELECT id FROM gl_journal_entries WHERE branch_id = $1)`, f.branchID)
	exec(`DELETE FROM gl_journal_entries WHERE branch_id = $1`, f.branchID)
	// the link's audit row names the branch it linked the customer to
	exec(`DELETE FROM audit_log WHERE action = 'customer.branch_linked' AND changes->>'branch_id' = $1`, f.branchID.String())
	exec(`DELETE FROM customer_branches WHERE branch_id = $1`, f.branchID)
	exec(`DELETE FROM locations WHERE id = $1`, f.branchID)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

// requestDeadline bounds one request a test sends.
const requestDeadline = 30 * time.Second

func (f *fixture) do(method, path string, body any, headers ...string) resp {
	f.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	// every request carries a deadline: a contender stuck behind a lock
	// fails its test at the deadline instead of hanging the package to the
	// test timeout (seventh review P3-5)
	ctx, cancel := context.WithTimeout(context.Background(), requestDeadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, f.srv.URL+path, rdr)
	if err != nil {
		f.t.Fatal(err)
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", "req-pos-test")
	for i := 0; i+1 < len(headers); i += 2 {
		v := headers[i+1]
		if f.cashierSub != "" && headers[i] == "X-Test-Sub" {
			if _, err := uuid.Parse(v); err == nil {
				v = f.cashierSub
			}
		}
		req.Header.Set(headers[i], v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		// Errorf, not Fatal: contenders call this from their own goroutines
		f.t.Errorf("%s %s did not answer within %s: %v", method, path, requestDeadline, err)
		return resp{raw: []byte(err.Error())}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

func str(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	s, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s is %T (%v), want a string", key, m[key], m[key])
	}
	return s
}

func num(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	n, ok := m[key].(json.Number)
	if !ok {
		t.Fatalf("%s is %T (%v), want an integer", key, m[key], m[key])
	}
	v, err := n.Int64()
	if err != nil {
		t.Fatalf("%s = %s, want an integer: %v", key, n, err)
	}
	return v
}

func rev(t *testing.T, r resp) int64 { return num(t, r.body, "revision") }

func errorOf(t *testing.T, r resp) (code string, blockers []string, fields []string) {
	t.Helper()
	e, ok := r.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope in %s", r.raw)
	}
	code, _ = e["code"].(string)
	if ds, ok := e["details"].([]any); ok {
		for _, d := range ds {
			dm := d.(map[string]any)
			if c, _ := dm["code"].(string); c != "" {
				blockers = append(blockers, c)
			}
			if fld, _ := dm["field"].(string); fld != "" {
				fields = append(fields, fld)
			}
		}
	}
	return code, blockers, fields
}

// ---------------------------------------------------------------------------
// Making sales through the routes.
// ---------------------------------------------------------------------------

// startSale opens a cart on the fixture's register.
func (f *fixture) startSale(customer *uuid.UUID) string {
	f.t.Helper()
	f.ensureTill()
	body := map[string]any{"register_id": f.register}
	if customer != nil {
		body["customer_id"] = customer.String()
	}
	r := f.do("POST", "/api/v1/pos/transactions", body, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
	if r.status != http.StatusCreated {
		f.t.Fatalf("start sale = %d: %s", r.status, r.raw)
	}
	if loc := r.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/pos/transactions/") {
		f.t.Errorf("Location = %q", loc)
	}
	return str(f.t, r.body, "id")
}

func mustUUID(t *testing.T) string {
	t.Helper()
	return uuid.NewString()
}

// addLine adds a product line of qty to the sale.
func (f *fixture) addLine(saleID string, line map[string]any) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/transactions/"+saleID+"/items", map[string]any{"line": line},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// productLine is a stocked product line of qty.
func (f *fixture) productLine(qty string) map[string]any {
	return map[string]any{"product_id": f.productID.String(), "quantity": qty}
}

// tender is one tender of cents by method.
func tender(method string, cents int64) map[string]any {
	return map[string]any{"method": method, "amount_cents": cents}
}

// completeSale completes the sale with the tenders and the sale's current
// revision, answering the body.
func (f *fixture) completeSale(saleID string, tenders ...map[string]any) resp {
	f.t.Helper()
	cur := f.getSale(f.t, saleID)
	return f.do("POST", "/api/v1/pos/transactions/"+saleID+"/complete",
		map[string]any{"tenders": tenders, "revision": num(f.t, cur, "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// saleOf makes a one-line sale of qty and completes it with the tenders.
func (f *fixture) saleOf(qty string, tenders ...map[string]any) (saleID string, body resp) {
	f.t.Helper()
	saleID = f.startSale(nil)
	if r := f.addLine(saleID, f.productLine(qty)); r.status != http.StatusOK {
		f.t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, tenders...)
	if r.status != http.StatusOK {
		f.t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	return saleID, r
}

// cashFor answers a cash tender of exactly the sale's current total.
func (f *fixture) cashFor(saleID string) map[string]any {
	f.t.Helper()
	var total int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT ROUND(total * 100)::bigint FROM pos_transactions WHERE id = $1`, saleID).Scan(&total); err != nil {
		f.t.Fatal(err)
	}
	return tender("cash", total)
}

// ensureTill opens the register's drawer with no float when none is open, the
// way a cashier opens the day before the first sale.
func (f *fixture) ensureTill() { f.t.Helper(); f.ensureTillOn(f.register) }

// ensureTillOn is ensureTill for another register of the fixture.
func (f *fixture) ensureTillOn(register string) {
	f.t.Helper()
	if f.noAutoTill {
		return
	}
	var open int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM till_sessions WHERE register_id = $1 AND status = 'OPEN'`, register).Scan(&open); err != nil {
		f.t.Fatal(err)
	}
	if open == 0 {
		f.openTillOn(register, 0)
	}
}

// allowAnyVariance lifts the fixture branch's variance threshold out of reach,
// for the tests whose subject is not the sign off and whose count is off by
// design: the close then posts its variance as it did before the threshold.
func (f *fixture) allowAnyVariance() {
	f.t.Helper()
	mustExec(f.t, f.db, `INSERT INTO till_variance_policy (branch_id, threshold_cents) VALUES ($1, 100000000)
		ON CONFLICT (branch_id) DO UPDATE SET threshold_cents = EXCLUDED.threshold_cents`, f.branchID)
}

// seedTill opens the register's drawer straight in the table, for the tests
// that run with the branch wall on and no header to open it through the route.
func (f *fixture) seedTill() {
	f.t.Helper()
	mustExec(f.t, f.db, `INSERT INTO till_sessions (register_id, branch_id, cashier_id, status, opening_float)
		VALUES ($1, $2, $3, 'OPEN', 0)`, f.register, f.branchID, uuid.New())
}

// openTill opens the fixture register's drawer with a float.
func (f *fixture) openTill(floatCents int64) string {
	f.t.Helper()
	return f.openTillOn(f.register, floatCents)
}

// openTillOn opens a register's drawer with a float.
func (f *fixture) openTillOn(register string, floatCents int64) string {
	f.t.Helper()
	r := f.do("POST", "/api/v1/pos/till/open", map[string]any{"register_id": register, "opening_float_cents": floatCents},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
	if r.status != http.StatusCreated {
		f.t.Fatalf("open till = %d: %s", r.status, r.raw)
	}
	return str(f.t, r.body, "id")
}

// ---------------------------------------------------------------------------
// Reading the ledger, the stock and the events.
// ---------------------------------------------------------------------------

type leg struct{ debit, credit int64 }

// legsFor sums, per account code, the legs of every entry whose source
// reference is one of the ids.
func (f *fixture) legsFor(ids ...string) (entries int, legs map[string]leg) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM gl_journal_entries WHERE source_ref_id::text = ANY($1)`, ids).Scan(&entries); err != nil {
		f.t.Fatal(err)
	}
	rows, err := f.db.Pool.Query(ctx, `
		SELECT a.code, ROUND(SUM(l.debit) * 100)::bigint, ROUND(SUM(l.credit) * 100)::bigint
		FROM gl_journal_lines l JOIN gl_journal_entries e ON e.id = l.journal_entry_id JOIN gl_accounts a ON a.id = l.account_id
		WHERE e.source_ref_id::text = ANY($1) GROUP BY a.code`, ids)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	legs = map[string]leg{}
	for rows.Next() {
		var code string
		var l leg
		if err := rows.Scan(&code, &l.debit, &l.credit); err != nil {
			f.t.Fatal(err)
		}
		legs[code] = l
	}
	return entries, legs
}

// accountBalance is an account's balance over the entries this fixture's
// own documents sourced: the documents at the branch the fixture owns, the
// counter's own acts and this register's till sessions, and every reversal of
// one of those. A package running beside this one against the same database
// never enters the count (fourth review P2-5, the shared database fixture
// sweep).
func (f *fixture) accountBalance(code string) int64 {
	f.t.Helper()
	invs := `(SELECT id FROM invoices WHERE customer_id = $3 OR id IN (SELECT invoice_id FROM pos_transactions WHERE branch_id = $2 OR register_id = $4))`
	cms := `(SELECT id FROM credit_memos WHERE branch_id = $2 OR customer_id = $3)`
	pays := `(SELECT id FROM payments WHERE branch_id = $2 OR customer_id = $3)`
	docs := `((SELECT id::text FROM invoices WHERE id IN ` + invs + `)
		UNION (SELECT id::text FROM credit_memos WHERE id IN ` + cms + `)
		UNION (SELECT id::text FROM payments WHERE id IN ` + pays + `)
		UNION (SELECT id::text FROM ar_applications WHERE invoice_id IN ` + invs + ` OR payment_id IN ` + pays + ` OR credit_memo_id IN ` + cms + `)
		UNION (SELECT id::text FROM payment_refunds WHERE payment_id IN ` + pays + ` OR credit_memo_id IN ` + cms + `)
		UNION (SELECT id::text FROM pos_transactions WHERE branch_id = $2)
		UNION (SELECT id::text FROM pos_returns WHERE branch_id = $2)
		UNION (SELECT id::text FROM till_sessions WHERE register_id = $4))`
	var n int64
	if err := f.db.Pool.QueryRow(context.Background(), `
		SELECT COALESCE(ROUND((SUM(l.debit) - SUM(l.credit)) * 100)::bigint, 0)
		FROM gl_journal_lines l
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		JOIN gl_accounts a ON a.id = l.account_id
		WHERE a.code = $1 AND NOT (e.id::text = ANY($5::text[]))
			AND (e.source_ref_id::text IN `+docs+`
				OR e.reverses_entry_id IN (SELECT o.id FROM gl_journal_entries o WHERE o.source_ref_id::text IN `+docs+`))`,
		code, f.branchID, f.customerID, f.register, f.entryExclusions()).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// The fixture's own document counts (fourth review P2-5): every count a
// test asserts is scoped to what this fixture's acts wrote, so the pos
// suite passes beside the other packages writing the same tables in one
// database (the way CI runs it): the counter's invoices, its memos, its
// register's returns, its payments and its refunds. The scope is the branch
// the fixture owns, a row no other fixture knows, or the register it opened.

func (f *fixture) counterInvoices(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM invoices WHERE branch_id = $1 OR customer_id = $2`, f.branchID, f.customerID)
}

// counterSales counts the counter's own sales: the ones at the branch the
// fixture owns. The payment, serve and worker packages write sales of their
// own, on the deployment's default branch; they never enter this count.
func (f *fixture) counterSales(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE branch_id = $1`, f.branchID)
}

func (f *fixture) counterMemos(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM credit_memos WHERE branch_id = $1 OR customer_id = $2`, f.branchID, f.customerID)
}

func (f *fixture) counterReturns(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM pos_returns WHERE register_id = $1`, f.register)
}

func (f *fixture) counterPayments(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM payments WHERE branch_id = $1 OR customer_id = $2`, f.branchID, f.customerID)
}

func (f *fixture) counterEntries(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM gl_journal_entries e WHERE e.source_ref_id::text IN
		((SELECT id::text FROM invoices WHERE branch_id = $1)
		UNION (SELECT id::text FROM payments WHERE branch_id = $1)
		UNION (SELECT id::text FROM pos_transactions WHERE branch_id = $1)
		UNION (SELECT id::text FROM pos_returns WHERE branch_id = $1)
		UNION (SELECT id::text FROM till_sessions WHERE register_id = $2))`, f.branchID, f.register)
}

func (f *fixture) counterRefunds(t *testing.T) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM payment_refunds WHERE payment_id IN
		(SELECT id FROM payments WHERE branch_id = $1)
		OR credit_memo_id IN (SELECT credit_memo_id FROM pos_returns WHERE branch_id = $1)`, f.branchID)
}

// entryExclusions lists the prior entry ids, with a sentinel that is never
// an id so the array is never empty.
func (f *fixture) entryExclusions() []string {
	out := make([]string, 0, len(f.priorEntries)+1)
	out = append(out, "00000000-0000-0000-0000-000000000000")
	for id := range f.priorEntries {
		out = append(out, id)
	}
	return out
}

// stock reads on hand/allocated of the fixture's product.
func (f *fixture) stock() string {
	f.t.Helper()
	var s string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT sum(quantity)::text || '/' || sum(allocated)::text FROM inventory WHERE product_id = $1`, f.productID).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

// balance reads the customer's subledger balance in cents.
func (f *fixture) balance() int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1`, f.customerID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) scalar(sql string, args ...any) any {
	f.t.Helper()
	var v any
	if err := f.db.Pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func countOf(t *testing.T, db *database.DB, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// eventTypes lists an entity's event types in order.
func eventTypes(t *testing.T, db *database.DB, entityType, entityID string) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT type FROM events_outbox WHERE entity_type = $1 AND entity_id = $2 ORDER BY position`, entityType, entityID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ty string
		if err := rows.Scan(&ty); err != nil {
			t.Fatal(err)
		}
		out = append(out, ty)
	}
	return out
}

// admitEverySale is a branch guard that holds nothing, for a fixture with no
// branch middleware.
type admitEverySale struct{}

func (admitEverySale) CheckPayloadBranch(context.Context, uuid.UUID) error { return nil }
