// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/money"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventRecorder writes a domain event into the transactional outbox, as the
// LAST statement of the mutation's transaction (ADR 0003 section 2).
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// BranchGuard applies the payload branch rule (ADR 0007 section 2.3).
type BranchGuard interface {
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
}

// Stock is the inventory seam an invoice void and a restocking credit memo
// use: the scale 4 functions of the inventory service, each taking an explicit
// branch and running inside the caller's transaction.
type Stock interface {
	RestockQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error
	UnrestockQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error
}

// OrderReopener is the order module's half of an invoice void (ADR 0005
// 6.2), implemented by order.Service: the invoice module cannot import the
// order module, which imports it.
type OrderReopener interface {
	// LockForInvoiceVoid takes the invoice's order row and the customer's
	// credit serialization (section 11, steps 1 and 1a), in that order.
	LockForInvoiceVoid(ctx context.Context, orderID uuid.UUID) error
	// ReturnBilled returns the voided invoice's billed quantities to its
	// order: the stock back on hand, quantity_fulfilled reduced, allocation
	// re-run for those quantities, the status re-derived. It answers the
	// order's own events for the caller to write last, and the order's new
	// status for the invoice's event.
	ReturnBilled(ctx context.Context, orderID uuid.UUID, billed []BilledLine, actor string) (ReopenResult, error)
}

// ReopenResult is what ReturnBilled did to the order.
type ReopenResult struct {
	Status string // the order's status after the void, lowercase
	Events []outbox.Event
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type Service struct {
	tx        TxRunner
	repo      Repository
	gl        *gl.Service
	account   account.Service
	auditLog  *audit.Logger
	db        *database.DB
	events    EventRecorder
	branches  BranchGuard
	inventory Stock
	orders    OrderReopener
	now       func() time.Time
}

func NewService(repo Repository, glService *gl.Service, accountService account.Service, db *database.DB) *Service {
	return &Service{repo: repo, gl: glService, account: accountService, db: db, now: time.Now}
}

// WithAuditLog sets the audit logger for financial operation tracking.
func (s *Service) WithAuditLog(l *audit.Logger) *Service { s.auditLog = l; return s }

// WithOutbox sets the event writer.
func (s *Service) WithOutbox(e EventRecorder) *Service { s.events = e; return s }

// WithTxRunner replaces the database as the transaction runner (a test gates
// transactions with it; serve leaves the database).
func (s *Service) WithTxRunner(tx TxRunner) *Service { s.tx = tx; return s }

// WithBranchGuard sets the payload branch rule for the write routes.
func (s *Service) WithBranchGuard(g BranchGuard) *Service { s.branches = g; return s }

// WithStock sets the inventory seam a void and a restock use.
func (s *Service) WithStock(st Stock) *Service { s.inventory = st; return s }

// WithOrders sets the order module's half of an invoice void.
func (s *Service) WithOrders(o OrderReopener) *Service { s.orders = o; return s }

// Store is the repository half the C2-3 acts use; the Postgres repository
// implements it. Unit tests that fake only Repository never reach them.
type Store interface {
	Repository
	FulfilmentStore

	ListInvoices(ctx context.Context, f ListFilter) ([]InvoiceSummary, error)
	CountInvoices(ctx context.Context, f ListFilter) (int64, error)
	LockInvoice(ctx context.Context, id uuid.UUID) (*Invoice, error)
	LockCustomerCredit(ctx context.Context, customerID uuid.UUID) error
	BranchLocalDate(ctx context.Context, branchID uuid.UUID, at time.Time) (time.Time, error)
	VoidFactsFor(ctx context.Context, invoiceID uuid.UUID) (VoidFacts, error)
	MarkVoid(ctx context.Context, id uuid.UUID, actor, reason string, voidedOn time.Time) error
	BilledLines(ctx context.Context, invoiceID uuid.UUID) ([]BilledLine, error)
	InvoiceEntryID(ctx context.Context, invoiceID uuid.UUID) (*uuid.UUID, error)

	ListCreditMemos(ctx context.Context, f CreditFilter) ([]CreditMemoSummary, error)
	CountCreditMemos(ctx context.Context, f CreditFilter) (int64, error)
	GetCreditMemo(ctx context.Context, id uuid.UUID) (*CreditMemo, error)
	LockCreditMemo(ctx context.Context, id uuid.UUID) (*CreditMemo, error)
	CreditMemoInvoiceID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	InsertCreditMemo(ctx context.Context, h *CreditHeader) error
	UpdateCreditDraft(ctx context.Context, h *CreditHeader) error
	ReplaceCreditLines(ctx context.Context, memoID uuid.UUID, lines []CreditLine) error
	PostCredit(ctx context.Context, h *CreditHeader, number string, glEntryID *uuid.UUID) error
	MarkCreditVoid(ctx context.Context, id uuid.UUID, actor, reason string, voidedOn time.Time) error
	NextCreditMemoNumber(ctx context.Context) (string, error)
	CreditedAgainstInvoice(ctx context.Context, invoiceID, exclude uuid.UUID, includeDrafts bool) (CreditedAgainst, error)
	CustomerCreditFacts(ctx context.Context, customerID uuid.UUID) (CreditFacts, error)
	OwnedBy(ctx context.Context, customerID uuid.UUID, shipToID, jobID *uuid.UUID) (bool, bool, error)
	TaxInputs(ctx context.Context, branchID uuid.UUID, shipToID *uuid.UUID) (shipRate, branchRate *string, err error)
	LookupProducts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]salesdoc.ProductRef, error)
	ChargeCodeByCode(ctx context.Context, code string) (salesdoc.ChargeCode, bool, error)
}

func (s *Service) store() (Store, error) {
	st, ok := s.repo.(Store)
	if !ok {
		return nil, errors.New("invoice: the repository cannot run the invoice acts")
	}
	return st, nil
}

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx != nil {
		return s.tx.RunInTx(ctx, fn)
	}
	if s.db == nil {
		return fn(ctx)
	}
	return s.db.RunInTx(ctx, fn)
}

// Precondition is the client's revision for a transition or an edit (ADR 0001
// section 11).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

func (p Precondition) missing() bool { return p.IfMatch == "" && p.Revision == nil }

func (p Precondition) check(current int64) error {
	return httpx.CheckRevision(current, p.IfMatch, p.Revision)
}

func conflictBlocker(code, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict, Message: message,
		Details: []httpx.FieldError{httpx.Blocker(code, message)}}
}

func notFound(err error) error {
	var he *httpx.Error
	if errors.As(err, &he) {
		return err
	}
	return httpx.NotFound("not found")
}

// checkBranch is the record branch rule (ADR 0007 section 2.3) for a document
// a path id addresses on the write routes: the document's branch must be one
// the caller may target, else 403 forbidden naming id. It fails closed.
func (s *Service) checkBranch(ctx context.Context, branch uuid.UUID, what string) error {
	if s.branches == nil {
		return nil
	}
	err := s.branches.CheckPayloadBranch(ctx, branch)
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		return &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
			Message: what + " is outside the branches this caller may target",
			Details: []httpx.FieldError{{Field: "id", Code: httpx.CodeForbidden, Message: "not a branch this caller may target"}}}
	}
	return err
}

// financeRoles may void and post (ADR 0005 6.2 and 6.3).
var financeRoles = map[string]bool{"admin": true, "owner": true, "finance": true}

// requireFinance holds an act to the finance roles; an empty role (an in
// process caller, a machine key) passes, as the order's release does.
func requireFinance(role, what string) error {
	if role != "" && !financeRoles[role] {
		return &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
			Message: what + " needs the admin, owner or finance role"}
	}
	return nil
}

// record writes one event through the transaction's executor, last.
func (s *Service) record(ctx context.Context, ev outbox.Event) error {
	if s.events == nil {
		return nil
	}
	return s.events.Write(ctx, ev)
}

func marshal(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// ---------------------------------------------------------------------------
// Reads.
// ---------------------------------------------------------------------------

func (s *Service) GetInvoice(ctx context.Context, id uuid.UUID) (*Invoice, error) {
	return s.repo.GetInvoice(ctx, id)
}

// GetInvoiceRecord reads an invoice for a caller that holds the record's own
// branch to the payload branch rule itself (the document print and email
// routes, PR 39): only the request's context branch narrows the read.
func (s *Service) GetInvoiceRecord(ctx context.Context, id uuid.UUID) (*Invoice, error) {
	if r, ok := s.repo.(interface {
		GetInvoiceRecord(ctx context.Context, id uuid.UUID) (*Invoice, error)
	}); ok {
		return r.GetInvoiceRecord(ctx, id)
	}
	return s.repo.GetInvoice(ctx, id)
}

// ListInvoices answers one page and whether another follows (the repository
// reads limit+1), with the filtered total on request.
func (s *Service) ListInvoices(ctx context.Context, f ListFilter, wantTotal bool) (items []InvoiceSummary, hasMore bool, total *int64, err error) {
	st, err := s.store()
	if err != nil {
		return nil, false, nil, err
	}
	probe := f
	probe.Limit = f.Limit + 1
	items, err = st.ListInvoices(ctx, probe)
	if err != nil {
		return nil, false, nil, err
	}
	if len(items) > f.Limit {
		items, hasMore = items[:f.Limit], true
	}
	if wantTotal {
		n, err := st.CountInvoices(ctx, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return items, hasMore, total, nil
}

// GetCustomerOpenBalanceCents returns the customer's live outstanding AR balance
// (sum of open invoices), in cents.
func (s *Service) GetCustomerOpenBalanceCents(ctx context.Context, customerID uuid.UUID) (int64, error) {
	return s.repo.SumOpenBalanceCents(ctx, customerID)
}

// ExistsInvoiceForOrder reports whether the order has an invoice not void.
func (s *Service) ExistsInvoiceForOrder(ctx context.Context, orderID uuid.UUID) (bool, error) {
	return s.repo.ExistsInvoiceForOrder(ctx, orderID)
}

// ---------------------------------------------------------------------------
// The void (ADR 0005 6.2).
// ---------------------------------------------------------------------------

// Transition is the body of an invoice transition.
type Transition struct {
	Reason string
	Actor  string
	Role   string
}

// VoidInvoice voids an unpaid invoice in one transaction, in section 11's
// order: the invoice's order row and the customer's credit serialization, the
// invoice, the stock back on hand and the order's lines and status, the
// customer row for the subledger, the reversal entry, then the events last.
// It reverses the invoice's whole entry (dated the void date), returns its
// billed stock to on hand, reduces the order lines' fulfilled quantities,
// re-runs allocation for those quantities and derives the order status.
func (s *Service) VoidInvoice(ctx context.Context, id uuid.UUID, pre Precondition, body Transition) (*Invoice, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	if err := requireFinance(body.Role, "voiding an invoice"); err != nil {
		return nil, err
	}
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	var out *Invoice
	err = s.inTx(ctx, func(ctx context.Context) error {
		// Read the invoice unlocked to learn its order, the first lock.
		head, err := st.GetInvoice(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkBranch(ctx, head.BranchID, "invoice"); err != nil {
			return err
		}
		if head.OrderID != nil && s.orders != nil {
			if err := s.orders.LockForInvoiceVoid(ctx, *head.OrderID); err != nil {
				return err
			}
		} else if err := st.LockCustomerCredit(ctx, head.CustomerID); err != nil {
			return err
		}
		inv, err := st.LockInvoice(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(inv.Revision); err != nil {
			return err
		}
		if inv.Status == InvoiceStatusVoid {
			return httpx.InvalidStateTransition("the invoice is already void")
		}
		facts, err := st.VoidFactsFor(ctx, id)
		if err != nil {
			return err
		}
		switch {
		case inv.Status != InvoiceStatusUnpaid || facts.Payments > 0 || facts.AppliedMemos > 0:
			// paid, partial, written off, a payment recorded or an applied
			// credit memo naming it: reverse them first (C2-4's live
			// applications take over this check)
			return conflictBlocker("has_applications", "the invoice has payments or applied credit memos: reverse them before voiding it")
		case facts.LiveMemos > 0:
			return conflictBlocker("has_credit_memos", "a credit memo names the invoice: void the credit memos first")
		}

		// Section 11 step 6: the order's stock and lines.
		var reopen ReopenResult
		if inv.OrderID != nil && s.orders != nil {
			billed, err := st.BilledLines(ctx, id)
			if err != nil {
				return err
			}
			if reopen, err = s.orders.ReturnBilled(ctx, *inv.OrderID, billed, body.Actor); err != nil {
				return err
			}
		}

		// Step 7 and 9: the customer row (the subledger's lock), then the
		// reversal of the invoice's whole entry, dated the void date.
		date, err := st.BranchLocalDate(ctx, inv.BranchID, s.now())
		if err != nil {
			return err
		}
		if err := st.LockCustomer(ctx, inv.CustomerID); err != nil {
			return err
		}
		// The entry to reverse: the invoice's own, else the one the legacy path
		// posted (an invoice written before C2-2, a counter account charge).
		// An invoice with money and no entry is refused: voiding it would move
		// the subledger and leave the ledger as it was.
		entryID := inv.GLEntryID
		if entryID == nil && s.gl != nil {
			if entryID, err = st.InvoiceEntryID(ctx, id); err != nil {
				return err
			}
			if entryID == nil && inv.TotalCents != 0 {
				return conflictBlocker("no_ledger_entry", "the invoice has no journal entry to reverse: it cannot be voided here")
			}
		}
		if entryID != nil && s.gl != nil {
			if _, err := s.gl.PostReversal(ctx, gl.ReversalInput{EntryID: *entryID, EntryDate: date,
				Currency: inv.Currency, Reason: "invoice " + inv.Number + " voided", PostedBy: body.Actor}); err != nil {
				return mapPostingError(err)
			}
		}
		if s.account != nil {
			invID := inv.ID
			if _, err := s.account.PostTransaction(ctx, inv.CustomerID, account.TransactionTypeReversal, -int64(inv.TotalCents), &invID, "Void of invoice "+inv.Number); err != nil {
				return fmt.Errorf("failed to reverse the invoice in the account ledger: %w", err)
			}
		}
		if err := st.MarkVoid(ctx, id, body.Actor, body.Reason, date); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "invoice.voided", EntityType: "invoice", EntityID: id, UserID: body.Actor,
				Changes: map[string]interface{}{"number": inv.Number, "reason": body.Reason, "total_cents": int64(inv.TotalCents)},
			}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if out, err = st.GetInvoice(ctx, id); err != nil {
			return err
		}

		// Step 10: the events, last.
		data := map[string]any{
			"number": out.Number, "customer_id": out.CustomerID, "status": out.Status.Status(),
			"from_status": inv.Status.Status(), "revision": out.Revision, "currency": out.Currency,
			"total_cents": int64(out.TotalCents),
		}
		if out.OrderID != nil {
			data["order_id"] = out.OrderID
			if reopen.Status != "" {
				data["order_status"] = reopen.Status
			}
		}
		branch := out.BranchID
		if err := s.record(ctx, outbox.Event{Type: EventInvoiceVoided, EntityType: "invoice", EntityID: out.ID,
			BranchID: &branch, Data: marshal(data)}); err != nil {
			return err
		}
		for _, ev := range reopen.Events {
			if err := s.record(ctx, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// mapPostingError maps the ledger's refusals to the wire: a closed period is
// a 409 with the blocker period_closed (ADR 0005 8.1).
func mapPostingError(err error) error {
	if errors.Is(err, gl.ErrPeriodClosed) {
		return conflictBlocker("period_closed", "the entry would be dated into a closed fiscal period")
	}
	return err
}

// ---------------------------------------------------------------------------
// The legacy writers (the counter's account charge; C2-5 retires them).
// ---------------------------------------------------------------------------

// DefaultTaxRate is the fallback sales tax rate of the counter's legacy account
// charge when its branch has none configured (C2-5 retires it with the path;
// the orders and the credit memos refuse instead, ADR 0005 section 3).
const DefaultTaxRate = 0.0825 // 8.25%

// CreateInvoice is the counter's account charge (the legacy path): the
// repository locks the customer, numbers the invoice through the counter and
// derives the due date from the customer's terms.
func (s *Service) CreateInvoice(ctx context.Context, inv *LegacyInvoice) error {
	if len(inv.Lines) == 0 {
		return fmt.Errorf("invoice must have lines")
	}
	if inv.Status == "" {
		inv.Status = InvoiceStatusUnpaid
	}

	callerSuppliedSubtotal := inv.Subtotal != 0
	if inv.Subtotal == 0 {
		var subtotal int64
		for _, line := range inv.Lines {
			// Round, never truncate: 100 cents x 8.29 is exactly 829 in
			// decimal but 828.99999999999989 in float64.
			subtotal += money.RoundToCents(float64(line.PriceEach) * line.Quantity)
		}
		inv.Subtotal = subtotal
	}
	// Callers that pre-computed the invoice (POS account-charge sales, where
	// the register already ran the exemption-aware calculation) supply BOTH
	// the subtotal and the tax-inclusive total, and are honored as-is. A total
	// on its own is not evidence that anyone calculated tax, so a total without
	// a subtotal is recomputed.
	if inv.TotalAmount == 0 || !callerSuppliedSubtotal {
		if inv.TaxRate == 0 {
			var branchID *uuid.UUID
			if inv.BranchID != uuid.Nil {
				branchID = &inv.BranchID
			}
			if rate, ok := s.repo.GetBranchTaxRate(ctx, branchID); ok {
				inv.TaxRate = rate
			} else {
				inv.TaxRate = DefaultTaxRate
			}
		}
		// Round half up: $10.00 at 8.25% is exactly 82.5 cents.
		inv.TaxAmount = money.RoundToCents(float64(inv.Subtotal) * inv.TaxRate)
		inv.TotalAmount = inv.Subtotal + inv.TaxAmount
	}

	return s.inTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.CreateInvoice(txCtx, inv); err != nil {
			return err
		}
		// Audit log: inside the transaction, so it shares the invoice's fate.
		if s.auditLog != nil {
			if err := s.auditLog.Log(txCtx, audit.Entry{
				Action:     "invoice.created",
				EntityType: "invoice",
				EntityID:   inv.ID,
				Changes: map[string]interface{}{
					"customer_id":  inv.CustomerID,
					"order_id":     inv.OrderID,
					"total_amount": inv.TotalAmount,
					"status":       inv.Status,
				},
			}); err != nil {
				return fmt.Errorf("failed to write audit log: %w", err)
			}
		}
		return nil
	})
}

// PostInvoiceToLedger posts an already-created legacy invoice to the GL (DR
// Accounts Receivable / CR Sales Revenue) and the customer AR subledger. It
// runs inside the caller's transaction so the invoice, entry and subledger
// commit atomically.
func (s *Service) PostInvoiceToLedger(ctx context.Context, inv *LegacyInvoice) error {
	if s.gl != nil {
		if err := s.gl.SyncInvoice(ctx, inv.ID.String(), inv.TotalAmount); err != nil {
			return fmt.Errorf("failed to post invoice to GL: %w", err)
		}
	}
	if s.account != nil {
		if _, err := s.account.PostTransaction(ctx, inv.CustomerID, account.TransactionTypeInvoice, inv.TotalAmount, &inv.ID, "Invoice #"+inv.ID.String()); err != nil {
			return fmt.Errorf("failed to post invoice to account ledger: %w", err)
		}
	}
	return nil
}

// PostCashSaleToGL posts a POS cash sale to the GL (DR Cash / CR Sales
// Revenue), best effort AFTER the sale commits (C2-5 moves it inside).
func (s *Service) PostCashSaleToGL(ctx context.Context, posTxID string, amountCents int64) error {
	if s.gl == nil {
		return nil
	}
	return s.gl.SyncCashSale(ctx, posTxID, amountCents)
}

// PostCashReturnToGL posts a POS cash refund to the GL (DR Sales Revenue / CR
// Cash), the mirror of PostCashSaleToGL; best effort, after the return
// commits. It returns the entry ID (uuid.Nil when no GL is wired).
func (s *Service) PostCashReturnToGL(ctx context.Context, returnID string, amountCents int64) (uuid.UUID, error) {
	if s.gl == nil {
		return uuid.Nil, nil
	}
	return s.gl.SyncCashReturn(ctx, returnID, amountCents)
}

// PostAccountReturnToLedger books a POS return refunded as store credit: the
// GL leg (DR Sales Revenue / CR Accounts Receivable) plus a balance-reducing
// subledger entry. Best-effort at the POS layer (C2-5 turns it into a credit
// memo). Returns the GL entry ID (uuid.Nil when no GL is wired).
func (s *Service) PostAccountReturnToLedger(ctx context.Context, customerID, returnID uuid.UUID, amountCents int64) (uuid.UUID, error) {
	var glEntryID uuid.UUID
	if s.gl != nil {
		id, err := s.gl.SyncAccountReturn(ctx, returnID.String(), amountCents)
		if err != nil {
			return uuid.Nil, fmt.Errorf("failed to post account return to GL: %w", err)
		}
		glEntryID = id
	}
	if s.account != nil {
		if _, err := s.account.PostTransaction(ctx, customerID, account.TransactionTypeRefund, -amountCents, &returnID, "POS return credit #"+returnID.String()); err != nil {
			return glEntryID, fmt.Errorf("failed to post account return to subledger: %w", err)
		}
	}
	return glEntryID, nil
}

// sortedProductIDs returns the distinct product ids of the lines in id order
// (section 11, step 6: inventory rows are taken in product id order).
func sortedProductIDs(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].String() < out[b].String() })
	return out
}
