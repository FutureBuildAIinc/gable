// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventRecorder writes a domain event into the transactional outbox, as the
// LAST statement of the mutation's transaction (ADR 0003 section 2).
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// AuditSink writes an audit row (*audit.Logger satisfies it).
type AuditSink interface {
	Log(ctx context.Context, e audit.Entry) error
}

// BranchGuard applies the payload branch rule (ADR 0008 section 11).
type BranchGuard interface {
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Event types the module writes (ADR 0008 section 10).
const (
	EventCreated  = "vendor_invoice.created"
	EventApproved = "vendor_invoice.approved"
	EventPartial  = "vendor_invoice.partial"
	EventPaid     = "vendor_invoice.paid"
	EventVoided   = "vendor_invoice.voided"
)

// Service is the AP module: the vendor invoice acts of ADR 0008 section 7.4
// on the wire contract of ADR 0001, and the payment act they fix. Every
// mutation is one transaction: the write, the journal entry it causes, the
// audit row, then the events last.
type Service struct {
	db       *database.DB
	tx       TxRunner
	repo     *Repository
	gl       *gl.Service
	auditLog AuditSink
	events   EventRecorder
	branches BranchGuard
	logger   *slog.Logger
	now      func() time.Time
}

func NewService(db *database.DB, repo *Repository, glSvc *gl.Service, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: db, repo: repo, gl: glSvc, logger: logger, now: time.Now}
}

// WithOutbox sets the event writer.
func (s *Service) WithOutbox(e EventRecorder) *Service { s.events = e; return s }

// WithTxRunner replaces the database as the transaction runner (a test gates
// transactions with it; serve leaves the database).
func (s *Service) WithTxRunner(tx TxRunner) *Service { s.tx = tx; return s }

// WithAuditLog sets the audit logger.
func (s *Service) WithAuditLog(l AuditSink) *Service { s.auditLog = l; return s }

// WithBranchGuard sets the payload branch rule for the write routes.
func (s *Service) WithBranchGuard(g BranchGuard) *Service { s.branches = g; return s }

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx != nil {
		return s.tx.RunInTx(ctx, fn)
	}
	return s.db.RunInTx(ctx, fn)
}

func (s *Service) record(ctx context.Context, ev outbox.Event) error {
	if s.events == nil {
		return nil
	}
	return s.events.Write(ctx, ev)
}

func (s *Service) recordAll(ctx context.Context, evs []outbox.Event) error {
	for _, ev := range evs {
		if err := s.record(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) audit(ctx context.Context, e audit.Entry) error {
	if s.auditLog == nil {
		return nil
	}
	if err := s.auditLog.Log(ctx, e); err != nil {
		return fmt.Errorf("failed to write the audit row: %w", err)
	}
	return nil
}

// Precondition is the client's revision for an act on the bill named in the
// path (ADR 0001 section 11). An in-process caller sets Any: the same rules
// and the same event, without a client revision to check.
type Precondition struct {
	IfMatch  string
	Revision *int64
	Any      bool
}

func (p Precondition) missing() bool { return !p.Any && p.IfMatch == "" && p.Revision == nil }

func (p Precondition) check(current int64) error {
	if p.Any {
		return nil
	}
	return httpx.CheckRevision(current, p.IfMatch, p.Revision)
}

// Caller is who is acting: the audit subject.
type Caller struct {
	Actor string
	Role  string
}

func conflictBlocker(code, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict, Message: message,
		Details: []httpx.FieldError{httpx.Blocker(code, message)}}
}

// checkBranch is the record branch rule for a branch a payload names.
func (s *Service) checkBranch(ctx context.Context, branch uuid.UUID, field string) error {
	if s.branches == nil {
		return nil
	}
	err := s.branches.CheckPayloadBranch(ctx, branch)
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		return &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
			Message: "the branch is outside the branches this caller may target",
			Details: []httpx.FieldError{{Field: field, Code: httpx.CodeForbidden, Message: "not a branch this caller may target"}}}
	}
	return err
}

func subtotalCents(in *Input) httpx.Cents {
	var sum httpx.Cents
	for _, l := range in.Lines {
		sum += l.LineTotalCents
	}
	return sum
}

func totalCents(in *Input) httpx.Cents { return subtotalCents(in) + in.TaxCents }

func mustJSON(v map[string]any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

// event builds one vendor_invoice.* event: a small summary, never the
// document (ADR 0008 section 10).
func (s *Service) event(typ string, from Status, inv *Summary) outbox.Event {
	branch := inv.BranchID
	return outbox.Event{
		Type: typ, EntityType: "vendor_invoice", EntityID: inv.ID, BranchID: &branch,
		Data: mustJSON(map[string]any{
			"number": inv.Number, "vendor_invoice_number": inv.VendorInvoiceNumber, "vendor_id": inv.VendorID,
			"branch_id": inv.BranchID, "status": inv.Status, "from_status": from, "revision": inv.Revision,
			"total_cents": int64(inv.TotalCents), "amount_open_cents": int64(inv.AmountOpenCents), "currency": inv.Currency,
		}),
	}
}

// reloaded reads the bill's summary again inside the transaction, for the
// event and the response.
func (s *Service) reloaded(ctx context.Context, id uuid.UUID) (*Summary, error) {
	inv, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &inv.Summary, nil
}

// resolve holds what a create takes from the database: the vendor, the
// references, the currency default and the branch.
func (s *Service) resolve(ctx context.Context, in *Input) (branch uuid.UUID, currency string, err error) {
	ok, err := s.repo.VendorExists(ctx, in.VendorID)
	if err != nil {
		return uuid.Nil, "", err
	}
	if !ok {
		return uuid.Nil, "", httpx.BadRequest("a referenced record does not exist",
			httpx.FieldError{Field: "vendor_id", Message: "no such vendor"})
	}
	if in.POID != nil {
		ok, err := s.repo.POExists(ctx, *in.POID)
		if err != nil {
			return uuid.Nil, "", err
		}
		if !ok {
			return uuid.Nil, "", httpx.BadRequest("a referenced record does not exist",
				httpx.FieldError{Field: "po_id", Message: "no such purchase order"})
		}
	}
	for i, l := range in.Lines {
		if l.PurchaseOrderLineID != nil {
			ok, err := s.repo.POLineExists(ctx, *l.PurchaseOrderLineID)
			if err != nil {
				return uuid.Nil, "", err
			}
			if !ok {
				return uuid.Nil, "", httpx.BadRequest("a referenced record does not exist",
					httpx.FieldError{Field: fmt.Sprintf("lines[%d].purchase_order_line_id", i), Message: "no such purchase order line"})
			}
		}
		if l.ProductID != nil {
			ok, err := s.repo.ProductExists(ctx, *l.ProductID)
			if err != nil {
				return uuid.Nil, "", err
			}
			if !ok {
				return uuid.Nil, "", httpx.BadRequest("a referenced record does not exist",
					httpx.FieldError{Field: fmt.Sprintf("lines[%d].product_id", i), Message: "no such product"})
			}
		}
	}
	currency = in.Currency
	if currency == "" {
		var setting string
		if err := s.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT COALESCE((SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD')`).Scan(&setting); err != nil {
			return uuid.Nil, "", err
		}
		currency = setting
	}
	branch = uuid.Nil
	if in.BranchID != nil {
		branch = *in.BranchID
	} else if ctxBranch := middleware.BranchIDForQuery(ctx); ctxBranch != nil {
		branch = *ctxBranch
	} else {
		var def uuid.UUID
		if err := s.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&def); err != nil {
			return uuid.Nil, "", err
		}
		branch = def
	}
	if err := s.checkBranch(ctx, branch, "branch_id"); err != nil {
		return uuid.Nil, "", err
	}
	return branch, currency, nil
}

// Create enters a vendor bill: it starts pending, owing its whole total, with
// Gable's own number and the vendor's as vendor_invoice_number.
func (s *Service) Create(ctx context.Context, in *Input, who Caller) (*Invoice, error) {
	branch, currency, err := s.resolve(ctx, in)
	if err != nil {
		return nil, err
	}
	var out *Invoice
	err = s.inTx(ctx, func(ctx context.Context) error {
		inv, err := s.repo.Create(ctx, in, branch, currency)
		if err != nil {
			return err
		}
		out = inv
		if err := s.audit(ctx, audit.Entry{Action: "vendor_invoice.created", EntityType: "vendor_invoice",
			EntityID: inv.ID, UserID: who.Actor, Changes: map[string]interface{}{
				"number": inv.Number, "vendor_invoice_number": inv.VendorInvoiceNumber, "vendor_id": inv.VendorID,
				"total_cents": int64(inv.TotalCents), "line_count": len(in.Lines), "currency": inv.Currency}}); err != nil {
			return err
		}
		return s.record(ctx, s.event(EventCreated, "", &inv.Summary))
	})
	if err != nil {
		return nil, err
	}
	s.logger.Info("vendor invoice entered", "id", out.ID, "number", out.Number, "total_cents", int64(out.TotalCents))
	return out, nil
}

// Get reads one bill with its lines, held to the caller's branch wall.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Invoice, error) {
	return s.repo.Get(ctx, id)
}

// List answers one page and whether another follows.
func (s *Service) List(ctx context.Context, f ListFilter, wantTotal bool) ([]Summary, bool, *int64, error) {
	asked := f.Limit
	f.Limit = asked + 1
	items, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, false, nil, err
	}
	more := len(items) > asked
	if more {
		items = items[:asked]
	}
	var total *int64
	if wantTotal {
		n, err := s.repo.Count(ctx, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return items, more, total, nil
}

// Transition runs POST /ap/invoices/{id}/transitions: to approved (the
// approve of ADR 0008 7.4) or to voided (the transition that reverses the
// entry).
func (s *Service) Transition(ctx context.Context, id uuid.UUID, in *TransitionInput, pre Precondition, who Caller) (*Invoice, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	switch in.To {
	case StatusApproved:
		return s.approve(ctx, id, pre, who, nil)
	case StatusVoided:
		return s.void(ctx, id, pre, in.Reason, who)
	case StatusPending, StatusPartial, StatusPaid:
		return nil, httpx.InvalidStateTransition("a bill never moves to " + in.To.Status() + " by a transition")
	}
	return nil, httpx.InvalidStateTransition("a bill never moves there by a transition")
}

// approve is the act ADR 0008 7.4 fixes: it loads the lines, posts the entry
// through gl.PostEntry inside the approve transaction, and a posting failure
// fails the act. approver is the recorded approver (the caller's UUID when it
// has one); a nil approver with an in-process caller records none.
func (s *Service) approve(ctx context.Context, id uuid.UUID, pre Precondition, who Caller, approver *uuid.UUID) (*Invoice, error) {
	var out *Invoice
	err := s.inTx(ctx, func(ctx context.Context) error {
		locked, err := s.repo.Lock(ctx, id)
		if err != nil {
			return err
		}
		if err := pre.check(locked.Revision); err != nil {
			return err
		}
		if locked.Status != StatusPending {
			return httpx.InvalidStateTransition("only a pending bill can be approved",
				httpx.Blocker("invoice_not_pending", "the bill is "+locked.Status.Status()))
		}
		lines, err := s.repo.LinesWithCodes(ctx, id)
		if err != nil {
			return err
		}
		var subtotal httpx.Cents
		for _, l := range lines {
			if l.AccountCode == "" {
				return conflictBlocker("line_needs_account",
					fmt.Sprintf("line %d has no account: the approve entry debits each line's account", l.Position))
			}
			subtotal += l.LineTotalCents
		}
		entryID, err := s.postInvoiceEntry(ctx, &locked.Summary, lines, subtotal, who.Actor)
		if err != nil {
			return err
		}
		var approvedBy *uuid.UUID
		if approver != nil {
			approvedBy = approver
		} else if id, err := uuid.Parse(who.Actor); err == nil {
			approvedBy = &id
		}
		if err := s.repo.MarkApproved(ctx, id, approvedBy, entryID); err != nil {
			return err
		}
		if err := s.audit(ctx, audit.Entry{Action: "vendor_invoice.approved", EntityType: "vendor_invoice",
			EntityID: id, UserID: who.Actor, Changes: map[string]interface{}{
				"number": locked.Number, "total_cents": int64(locked.TotalCents), "gl_entry_id": entryID}}); err != nil {
			return err
		}
		after, err := s.reloaded(ctx, id)
		if err != nil {
			return err
		}
		if err := s.record(ctx, s.event(EventApproved, StatusPending, after)); err != nil {
			return err
		}
		full, err := s.repo.Get(ctx, id)
		if err != nil {
			return err
		}
		out = full
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.logger.Info("vendor invoice approved", "id", id, "total_cents", int64(out.TotalCents))
	return out, nil
}

// postInvoiceEntry builds and posts the approve entry: a debit leg per line
// on the line's account, the tax spread across the line accounts pro rata to
// their extensions (the last line taking the remainder, the freight rule of
// ADR 0008 3.5), and the credit to Accounts Payable for the whole total. The
// entry rides the approve transaction; a posting failure fails the act.
func (s *Service) postInvoiceEntry(ctx context.Context, inv *Summary, lines []LineWithCode, subtotal httpx.Cents, actor string) (*uuid.UUID, error) {
	tax := int64(inv.TaxCents)
	remaining := tax
	legs := make([]gl.Leg, 0, len(lines)+1)
	for i, l := range lines {
		debit := int64(l.LineTotalCents)
		switch {
		case i == len(lines)-1:
			debit += remaining
		case int64(subtotal) > 0:
			share := tax * int64(l.LineTotalCents) / int64(subtotal)
			debit += share
			remaining -= share
		}
		legs = append(legs, gl.Leg{AccountCode: l.AccountCode, Description: l.Description, Debit: debit})
	}
	legs = append(legs, gl.Leg{AccountCode: gl.AccountCodeAP, Description: "Accounts Payable", Credit: int64(inv.TotalCents)})
	entry, err := s.gl.PostEntry(ctx, gl.PostingInput{
		EntryDate: mustDate(inv.InvoiceDate), Memo: "Vendor invoice " + inv.Number,
		Source: gl.SourceVendorInv, SourceRefID: &inv.ID, Currency: inv.Currency, PostedBy: actor, Legs: legs,
	})
	if err != nil {
		if errors.Is(err, gl.ErrPeriodClosed) {
			return nil, conflictBlocker("period_closed", "the entry would be dated into a closed fiscal period")
		}
		if errors.Is(err, gl.ErrUnbalanced) {
			return nil, conflictBlocker("entry_unbalanced", "the bill's lines do not balance against its total")
		}
		return nil, fmt.Errorf("failed to post the vendor invoice entry: %w", err)
	}
	if entry == nil {
		return nil, nil
	}
	return &entry.ID, nil
}

// void is the transition that reverses the entry: a pending bill has nothing
// posted and simply dies; an approved one's entry is reversed whole. A bill
// with money applied is refused: the payments have to be undone first.
func (s *Service) void(ctx context.Context, id uuid.UUID, pre Precondition, reason string, who Caller) (*Invoice, error) {
	if reason == "" {
		return nil, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "reason", Message: "is required to void a bill"})
	}
	var out *Invoice
	err := s.inTx(ctx, func(ctx context.Context) error {
		locked, err := s.repo.Lock(ctx, id)
		if err != nil {
			return err
		}
		if err := pre.check(locked.Revision); err != nil {
			return err
		}
		from := locked.Status
		if locked.AmountPaidCents > 0 {
			return httpx.InvalidStateTransition("the bill carries payments: they must be undone before it is voided",
				httpx.Blocker("has_payments", fmt.Sprintf("%d cents have been applied to this bill", int64(locked.AmountPaidCents))))
		}
		if !from.allows(StatusVoided) {
			return httpx.InvalidStateTransition("a " + from.Status() + " bill cannot be voided")
		}
		var reversal *uuid.UUID
		if locked.GLEntryID != nil {
			entry, err := s.gl.PostReversal(ctx, gl.ReversalInput{
				EntryID: *locked.GLEntryID, EntryDate: s.now(), Currency: locked.Currency,
				Reason: reason, PostedBy: who.Actor,
			})
			if err != nil {
				if errors.Is(err, gl.ErrPeriodClosed) {
					return conflictBlocker("period_closed", "the reversal would be dated into a closed fiscal period")
				}
				return fmt.Errorf("failed to reverse the vendor invoice entry: %w", err)
			}
			if entry != nil {
				reversal = &entry.ID
			}
		}
		if err := s.repo.MarkVoided(ctx, id); err != nil {
			return err
		}
		if err := s.audit(ctx, audit.Entry{Action: "vendor_invoice.voided", EntityType: "vendor_invoice",
			EntityID: id, UserID: who.Actor, Changes: map[string]interface{}{
				"number": locked.Number, "reason": reason, "reversal_entry_id": reversal}}); err != nil {
			return err
		}
		after, err := s.reloaded(ctx, id)
		if err != nil {
			return err
		}
		if err := s.record(ctx, s.event(EventVoided, from, after)); err != nil {
			return err
		}
		full, err := s.repo.Get(ctx, id)
		if err != nil {
			return err
		}
		out = full
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ApproveInvoice is the in-process approve (the matcher's auto approve): the
// same transition without a client revision, attributed to the given
// approver.
func (s *Service) ApproveInvoice(ctx context.Context, invoiceID uuid.UUID, approverID uuid.UUID) (*Invoice, error) {
	return s.approve(ctx, invoiceID, Precondition{Any: true}, Caller{Actor: approverID.String()}, &approverID)
}

// GetVendorInvoice is the in-process read of one bill with its lines.
func (s *Service) GetVendorInvoice(ctx context.Context, id uuid.UUID) (*Invoice, error) {
	return s.repo.Get(ctx, id)
}

// ListVendorInvoices is the in-process listing matching reads: every bill of
// the vendor (or every vendor), optionally by storage status.
func (s *Service) ListVendorInvoices(ctx context.Context, vendorID *uuid.UUID, status string) ([]Invoice, error) {
	ids, err := s.repo.List(ctx, ListFilter{VendorID: vendorID, Limit: 100000})
	if err != nil {
		return nil, err
	}
	out := make([]Invoice, 0, len(ids))
	for _, h := range ids {
		if status != "" && storageStatus(h.Status) != status && h.Status.Status() != status {
			continue
		}
		full, err := s.repo.Get(ctx, h.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, *full)
	}
	return out, nil
}

// PayVendor records a payment to a vendor and applies it to the bills it
// names, in id order, under their locks: only an approved or partial bill
// takes money (ADR 0008 7.4). The entry posts through gl.PostEntry inside the
// act's transaction; a posting failure fails the act.
func (s *Service) PayVendor(ctx context.Context, in *PayInput, who Caller) (*APPayment, error) {
	// The invoices are locked in id order (ADR 0008 section 9 step 5a), so
	// every contender takes them in one order; the money then applies in the
	// order the request named them, the order the desk pays them in.
	lockedIDs := make([]uuid.UUID, len(in.InvoiceIDs))
	copy(lockedIDs, in.InvoiceIDs)
	sort.Slice(lockedIDs, func(i, j int) bool { return lockedIDs[i].String() < lockedIDs[j].String() })

	var pmt *APPayment
	err := s.inTx(ctx, func(ctx context.Context) error {
		p := &APPayment{
			VendorID:    in.VendorID,
			Amount:      in.AmountCents,
			Method:      in.Method,
			CheckNumber: in.CheckNumber,
			Reference:   in.Reference,
			PaymentDate: in.PaymentDate,
			Status:      "COMPLETE",
		}
		if err := s.repo.CreatePayment(ctx, p); err != nil {
			return err
		}
		pmt = p

		locked := make(map[uuid.UUID]*Summary, len(lockedIDs))
		for _, id := range lockedIDs {
			inv, err := s.repo.LockForPayment(ctx, id, in.VendorID)
			if err != nil {
				return err
			}
			if inv == nil {
				return httpx.BadRequest("a referenced record does not exist",
					httpx.FieldError{Field: fmt.Sprintf("invoice_ids[%d]", indexOfUUID(in.InvoiceIDs, id)),
						Message: "no such invoice for this vendor"})
			}
			if inv.Status != StatusApproved && inv.Status != StatusPartial {
				return httpx.InvalidStateTransition("only an approved or partial bill takes a payment",
					httpx.Blocker("invoice_not_approved", inv.Number+" is "+inv.Status.Status()))
			}
			locked[id] = inv
		}

		var events []outbox.Event
		remaining := in.AmountCents
		for _, id := range in.InvoiceIDs {
			inv := locked[id]
			open := int64(inv.AmountOpenCents)
			if open <= 0 {
				continue
			}
			apply := remaining
			if apply > open {
				apply = open
			}
			if apply <= 0 {
				break
			}
			app := &APPaymentApplication{PaymentID: p.ID, InvoiceID: id, Amount: apply}
			if err := s.repo.CreatePaymentApplication(ctx, app); err != nil {
				return err
			}
			paid, err := s.repo.ApplyToInvoice(ctx, id, apply)
			if err != nil {
				return err
			}
			remaining -= apply
			after, err := s.reloaded(ctx, id)
			if err != nil {
				return err
			}
			from := inv.Status
			typ := EventPartial
			if paid {
				typ = EventPaid
			}
			events = append(events, s.event(typ, from, after))
			if remaining <= 0 {
				break
			}
		}
		if remaining > 0 {
			return httpx.InvalidStateTransition("the payment exceeds what the named bills still owe",
				httpx.Blocker("exceeds_open", fmt.Sprintf("%d cents were asked and %d cents are open", in.AmountCents, in.AmountCents-remaining)))
		}
		if len(locked) > 0 {
			ref := p.ID
			entry, err := s.gl.PostEntry(ctx, gl.PostingInput{
				EntryDate: mustDate(in.PaymentDate), Memo: "Vendor payment " + p.ID.String(),
				Source: gl.SourceVendorPmt, SourceRefID: &ref, PostedBy: who.Actor,
				Legs: []gl.Leg{
					{AccountCode: gl.AccountCodeAP, Description: "Accounts Payable", Debit: in.AmountCents - remaining},
					{AccountCode: gl.AccountCodeCash, Description: "Cash", Credit: in.AmountCents - remaining},
				},
			})
			if err != nil {
				if errors.Is(err, gl.ErrPeriodClosed) {
					return conflictBlocker("period_closed", "the entry would be dated into a closed fiscal period")
				}
				return fmt.Errorf("failed to post the vendor payment entry: %w", err)
			}
			_ = entry
		}
		if err := s.audit(ctx, audit.Entry{Action: "vendor_payment.recorded", EntityType: "ap_payment",
			EntityID: p.ID, UserID: who.Actor, Changes: map[string]interface{}{
				"vendor_id": in.VendorID, "amount_cents": in.AmountCents, "method": string(in.Method),
				"invoices": len(locked)}}); err != nil {
			return err
		}
		return s.recordAll(ctx, events)
	})
	if err != nil {
		return nil, err
	}
	s.logger.Info("vendor payment recorded", "id", pmt.ID, "vendor_id", pmt.VendorID, "amount_cents", pmt.Amount)
	return pmt, nil
}

func indexOfUUID(haystack []uuid.UUID, needle uuid.UUID) int {
	for i, v := range haystack {
		if v == needle {
			return i
		}
	}
	return 0
}

// mustDate parses a YYYY-MM-DD wire date into the posting's business date;
// an unparsable value answers today rather than failing the posting.
func mustDate(s string) time.Time {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return time.Now()
}

// ListPayments lists a vendor's payments.
func (s *Service) ListPayments(ctx context.Context, vendorID *uuid.UUID) ([]APPayment, error) {
	return s.repo.ListPayments(ctx, vendorID)
}

// GetAgingSummary returns the AP aging report by vendor.
func (s *Service) GetAgingSummary(ctx context.Context) ([]APAgingSummary, error) {
	return s.repo.GetAgingSummary(ctx)
}
