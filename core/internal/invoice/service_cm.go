// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// creditContext is what a draft resolves from its request: the invoice it
// names, the customer, branch, currency, ship-to, job and exemption.
type creditContext struct {
	source     *Invoice
	customerID uuid.UUID
	branchID   uuid.UUID
	currency   string
	shipToID   *uuid.UUID
	jobID      *uuid.UUID
	exempt     bool
}

// branchRefused is the payload branch rule's 403 naming the field.
func (s *Service) checkPayloadBranch(ctx context.Context, branch uuid.UUID, field string) error {
	if s.branches == nil {
		return nil
	}
	err := s.branches.CheckPayloadBranch(ctx, branch)
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		return &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
			Message: field + " is outside the branches this caller may target",
			Details: []httpx.FieldError{{Field: field, Code: httpx.CodeForbidden, Message: "not a branch this caller may target"}}}
	}
	return err
}

// resolveCredit settles the draft's context. stored is the memo being edited
// (nil on create): its customer and invoice are fixed once the memo exists.
func (s *Service) resolveCredit(ctx context.Context, st Store, d *CreditInput, stored *CreditMemo) (*creditContext, error) {
	cc := &creditContext{}
	var invoiceID *uuid.UUID = d.InvoiceID
	if stored != nil {
		if d.InvoiceID != nil && (stored.InvoiceID == nil || *stored.InvoiceID != *d.InvoiceID) {
			return nil, validationFailed("one or more fields failed validation",
				httpx.FieldError{Field: "invoice_id", Message: "cannot be changed by an edit: it is fixed when the credit memo is created"})
		}
		if d.CustomerID != nil && *d.CustomerID != stored.CustomerID {
			return nil, validationFailed("one or more fields failed validation",
				httpx.FieldError{Field: "customer_id", Message: "cannot be changed by an edit: it is fixed when the credit memo is created"})
		}
		invoiceID = stored.InvoiceID
	}
	if invoiceID != nil {
		inv, err := st.GetInvoice(ctx, *invoiceID)
		if err != nil {
			var he *httpx.Error
			if errors.As(err, &he) && he.Status == http.StatusNotFound {
				return nil, validationFailed("a referenced record does not exist",
					httpx.FieldError{Field: "invoice_id", Message: "no such invoice"})
			}
			return nil, err
		}
		if inv.Status == InvoiceStatusVoid {
			return nil, conflictBlocker("invoice_void", "the invoice is void: a credit memo cannot name it")
		}
		if d.CustomerID != nil && *d.CustomerID != inv.CustomerID {
			return nil, validationFailed("one or more fields failed validation",
				httpx.FieldError{Field: "customer_id", Message: "is not the invoice's customer"})
		}
		if d.BranchID != nil && *d.BranchID != inv.BranchID {
			return nil, validationFailed("one or more fields failed validation",
				httpx.FieldError{Field: "branch_id", Message: "is not the invoice's branch"})
		}
		cc.source, cc.customerID, cc.branchID, cc.currency = inv, inv.CustomerID, inv.BranchID, inv.Currency
		cc.shipToID, cc.jobID = inv.ShipToID, inv.JobID
	} else if stored != nil {
		cc.customerID = stored.CustomerID
		cc.branchID, cc.currency = stored.BranchID, stored.Currency
	} else {
		cc.customerID = *d.CustomerID
	}
	facts, err := st.CustomerCreditFacts(ctx, cc.customerID)
	if err != nil {
		return nil, err
	}
	cc.exempt = facts.Exempt
	if cc.source == nil && stored == nil {
		cc.currency = facts.Currency
		switch {
		case d.BranchID != nil:
			cc.branchID = *d.BranchID
		case middleware.BranchIDForQuery(ctx) != nil:
			cc.branchID = *middleware.BranchIDForQuery(ctx)
		default:
			cc.branchID = facts.PrimaryBranchID
		}
	}
	if d.ShipToID != nil {
		cc.shipToID = d.ShipToID
	}
	if d.JobID != nil {
		cc.jobID = d.JobID
	}
	shipOK, jobOK, err := st.OwnedBy(ctx, cc.customerID, d.ShipToID, d.JobID)
	if err != nil {
		return nil, err
	}
	var bad []httpx.FieldError
	if !shipOK {
		bad = append(bad, httpx.FieldError{Field: "ship_to_id", Message: "is not a ship-to of this customer"})
	}
	if !jobOK {
		bad = append(bad, httpx.FieldError{Field: "job_id", Message: "is not a job of this customer"})
	}
	if len(bad) > 0 {
		return nil, validationFailed("one or more fields failed validation", bad...)
	}
	return cc, nil
}

func (s *Service) recordCredit(ctx context.Context, cm *CreditMemo, eventType, from string) error {
	data := map[string]any{
		"number": cm.Number, "customer_id": cm.CustomerID, "status": cm.Status.Status(), "revision": cm.Revision,
		"currency": cm.Currency, "total_cents": int64(cm.TotalCents),
	}
	if from != "" {
		data["from_status"] = from
	}
	if cm.InvoiceID != nil {
		data["invoice_id"] = cm.InvoiceID
	}
	branch := cm.BranchID
	return s.record(ctx, outbox.Event{Type: eventType, EntityType: "credit_memo", EntityID: cm.ID,
		BranchID: &branch, Data: marshal(data)})
}

func (s *Service) GetCreditMemo(ctx context.Context, id uuid.UUID) (*CreditMemo, error) {
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	return st.GetCreditMemo(ctx, id)
}

// ListCreditMemos answers one page and whether another follows.
func (s *Service) ListCreditMemos(ctx context.Context, f CreditFilter, wantTotal bool) (items []CreditMemoSummary, hasMore bool, total *int64, err error) {
	st, err := s.store()
	if err != nil {
		return nil, false, nil, err
	}
	probe := f
	probe.Limit = f.Limit + 1
	items, err = st.ListCreditMemos(ctx, probe)
	if err != nil {
		return nil, false, nil, err
	}
	if len(items) > f.Limit {
		items, hasMore = items[:f.Limit], true
	}
	if wantTotal {
		n, err := st.CountCreditMemos(ctx, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return items, hasMore, total, nil
}

// CreateCreditMemo writes a credit memo draft (ADR 0005 6.3): no number, the
// lines built and totalled, the event last.
func (s *Service) CreateCreditMemo(ctx context.Context, d *CreditInput, actor string) (*CreditMemo, error) {
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	var out *CreditMemo
	err = s.inTx(ctx, func(ctx context.Context) error {
		if d.BranchID != nil {
			if err := s.checkPayloadBranch(ctx, *d.BranchID, "branch_id"); err != nil {
				return err
			}
		}
		cc, err := s.resolveCredit(ctx, st, d, nil)
		if err != nil {
			return err
		}
		if err := s.checkPayloadBranch(ctx, cc.branchID, "branch_id"); err != nil {
			return err
		}
		id := uuid.New()
		b, err := s.buildForDraft(ctx, st, d, cc, uuid.Nil, nil)
		if err != nil {
			return err
		}
		date, err := st.BranchLocalDate(ctx, cc.branchID, s.now())
		if err != nil {
			return err
		}
		h := &CreditHeader{ID: id, CustomerID: cc.customerID, InvoiceID: d.InvoiceID, BranchID: cc.branchID, ProjectID: cc.jobID,
			ShipToID: cc.shipToID, Currency: cc.currency, ReasonCode: d.ReasonCode, Reason: d.Reason, MemoDate: date,
			SubtotalCents: b.subtotal, TaxCents: b.tax, TotalCents: b.total(), TaxRate: b.taxRate}
		if err := st.InsertCreditMemo(ctx, h); err != nil {
			return err
		}
		if err := st.ReplaceCreditLines(ctx, id, b.lines); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_memo.created", EntityType: "credit_memo", EntityID: id, UserID: actor,
				Changes: map[string]interface{}{"customer_id": cc.customerID, "invoice_id": d.InvoiceID, "total_cents": b.total()}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if out, err = st.GetCreditMemo(ctx, id); err != nil {
			return err
		}
		return s.recordCredit(ctx, out, EventCreditCreated, "")
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// buildForDraft builds a draft's lines against what the invoice's other
// credit memos have credited: the extensions telescope against the posted
// ones, the exceeds_billed guard counts the drafts too.
func (s *Service) buildForDraft(ctx context.Context, st Store, d *CreditInput, cc *creditContext, exclude uuid.UUID, current []CreditLine) (*creditBuild, error) {
	var amounts, guard CreditedAgainst
	if cc.source != nil {
		var err error
		if amounts, err = st.CreditedAgainstInvoice(ctx, cc.source.ID, exclude, false); err != nil {
			return nil, err
		}
		if guard, err = st.CreditedAgainstInvoice(ctx, cc.source.ID, exclude, true); err != nil {
			return nil, err
		}
	}
	ids := map[uuid.UUID]bool{}
	for i := range current {
		ids[current[i].ID] = true
	}
	return s.buildCredit(ctx, st, d, cc.source, amounts, guard, ids, cc.branchID, cc.shipToID, cc.exempt)
}

// UpdateCreditMemo replaces a draft's header and lines on the client's
// revision; any other status is 409 credit_memo_not_draft.
func (s *Service) UpdateCreditMemo(ctx context.Context, id uuid.UUID, d *CreditInput, pre Precondition, actor string) (*CreditMemo, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	var out *CreditMemo
	err = s.inTx(ctx, func(ctx context.Context) error {
		cm, err := st.LockCreditMemo(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkBranch(ctx, cm.BranchID, "credit memo"); err != nil {
			return err
		}
		if err := pre.check(cm.Revision); err != nil {
			return err
		}
		if cm.Status != CreditDraft {
			return conflictBlocker("credit_memo_not_draft", "only a draft credit memo can be edited: it is "+cm.Status.Status())
		}
		cc, err := s.resolveCredit(ctx, st, d, cm)
		if err != nil {
			return err
		}
		b, err := s.buildForDraft(ctx, st, d, cc, id, cm.Lines)
		if err != nil {
			return err
		}
		date, err := st.BranchLocalDate(ctx, cc.branchID, s.now())
		if err != nil {
			return err
		}
		h := &CreditHeader{ID: id, CustomerID: cc.customerID, InvoiceID: cm.InvoiceID, BranchID: cc.branchID, ProjectID: cc.jobID,
			ShipToID: cc.shipToID, Currency: cc.currency, ReasonCode: d.ReasonCode, Reason: d.Reason, MemoDate: date,
			SubtotalCents: b.subtotal, TaxCents: b.tax, TotalCents: b.total(), TaxRate: b.taxRate}
		if err := st.UpdateCreditDraft(ctx, h); err != nil {
			return err
		}
		if err := st.ReplaceCreditLines(ctx, id, b.lines); err != nil {
			return err
		}
		if out, err = st.GetCreditMemo(ctx, id); err != nil {
			return err
		}
		return s.recordCredit(ctx, out, EventCreditUpdated, "")
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TransitionCreditMemo runs the credit memo lifecycle's transitions (ADR 0005
// 6.3): draft to open (the post) and draft or open to void. The statuses past
// open arrive with C2-4's applications and are not reachable by a client.
func (s *Service) TransitionCreditMemo(ctx context.Context, id uuid.UUID, to CreditStatus, pre Precondition, body Transition) (*CreditMemo, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	switch to {
	case CreditOpen:
		return s.postCredit(ctx, id, pre, body)
	case CreditVoid:
		return s.voidCredit(ctx, id, pre, body)
	}
	return nil, httpx.InvalidStateTransition("a credit memo moves to open (post) or void; " + to.Status() + " is reached by applications and refunds")
}

// postCredit posts a draft: it locks the invoice it names (so an invoice
// void's has_credit_memos check cannot race it), rebuilds the lines against
// what was posted, restocks, mints the gapless number late, posts the entry
// and the subledger credit, and writes credit_memo.posted last.
func (s *Service) postCredit(ctx context.Context, id uuid.UUID, pre Precondition, body Transition) (*CreditMemo, error) {
	if err := requireFinance(body.Role, "posting a credit memo"); err != nil {
		return nil, err
	}
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	var out *CreditMemo
	err = s.inTx(ctx, func(ctx context.Context) error {
		cm, err := st.LockCreditMemo(ctx, id) // step 3
		if err != nil {
			return notFound(err)
		}
		if err := s.checkBranch(ctx, cm.BranchID, "credit memo"); err != nil {
			return err
		}
		if err := pre.check(cm.Revision); err != nil {
			return err
		}
		if cm.Status != CreditDraft {
			return httpx.InvalidStateTransition("only a draft credit memo can be posted: it is " + cm.Status.Status())
		}
		var source *Invoice
		if cm.InvoiceID != nil {
			if source, err = st.LockInvoice(ctx, *cm.InvoiceID); err != nil { // step 4
				return notFound(err)
			}
			if source.Status == InvoiceStatusVoid {
				return conflictBlocker("invoice_void", "the invoice is void: a credit memo cannot be posted against it")
			}
		}
		facts, err := st.CustomerCreditFacts(ctx, cm.CustomerID)
		if err != nil {
			return err
		}
		d := draftFromStored(cm)
		var amounts CreditedAgainst
		if source != nil {
			if amounts, err = st.CreditedAgainstInvoice(ctx, source.ID, cm.ID, false); err != nil {
				return err
			}
		}
		ids := map[uuid.UUID]bool{}
		for i := range cm.Lines {
			ids[cm.Lines[i].ID] = true
		}
		b, err := s.buildCredit(ctx, st, d, source, amounts, amounts, ids, cm.BranchID, cm.ShipToID, facts.Exempt)
		if err != nil {
			return err
		}
		total := b.total()
		if total >= 0 {
			return conflictBlocker("empty_credit_memo", "the credit memo credits nothing: add a priced line")
		}

		// Step 6: the restocked goods back on hand, in (product id, line id) order.
		if err := s.restockLines(ctx, cm.BranchID, b.lines, true); err != nil {
			return err
		}

		// Steps 7 and 8: the customer row, then the gapless counter, late.
		if err := st.LockCustomer(ctx, cm.CustomerID); err != nil {
			return err
		}
		number, err := st.NextCreditMemoNumber(ctx)
		if err != nil {
			return err
		}
		date, err := st.BranchLocalDate(ctx, cm.BranchID, s.now())
		if err != nil {
			return err
		}

		// Step 9: the entry. Revenue and tax come back, the receivable falls,
		// and a restocked line returns its cost to inventory at the cost that
		// left (COGS reverses at the original cost).
		legs := []gl.Leg{{AccountCode: gl.AccountCodeAR, Description: "Accounts Receivable", Credit: -total}}
		for _, g := range salesdoc.RevenueGroups(creditLinesAsDoc(b.lines)) {
			legs = append(legs, gl.Leg{AccountCode: g.AccountCode, Description: "Revenue " + g.AccountCode, Debit: -int64(g.Cents)})
		}
		restocked := restockSum(b.lines)
		legs = append(legs,
			gl.Leg{AccountCode: gl.AccountCodeSalesTax, Description: "Sales Tax Payable", Debit: -b.tax},
			gl.Leg{AccountCode: gl.AccountCodeInventory, Description: "Inventory", Debit: restocked},
			gl.Leg{AccountCode: gl.AccountCodeCOGS, Description: "Cost of Goods Sold", Credit: restocked})
		cmID := cm.ID
		var glID *uuid.UUID
		if s.gl != nil {
			entry, err := s.gl.PostEntry(ctx, gl.PostingInput{EntryDate: date, Memo: "Credit memo " + number, Source: gl.SourceCreditMemo,
				SourceRefID: &cmID, Currency: cm.Currency, PostedBy: body.Actor, Legs: legs})
			if err != nil {
				return mapPostingError(fmt.Errorf("failed to post the credit memo entry: %w", err))
			}
			if entry != nil {
				glID = &entry.ID
			}
		}
		if s.account != nil {
			if _, err := s.account.PostTransaction(ctx, cm.CustomerID, account.TransactionTypeCreditMemo, total, &cmID, "Credit memo "+number); err != nil {
				return fmt.Errorf("failed to post the credit memo to the account ledger: %w", err)
			}
		}
		h := &CreditHeader{ID: cm.ID, MemoDate: date, SubtotalCents: b.subtotal, TaxCents: b.tax, TotalCents: total, TaxRate: b.taxRate}
		if err := st.ReplaceCreditLines(ctx, cm.ID, b.lines); err != nil {
			return err
		}
		if err := st.PostCredit(ctx, h, number, glID); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_memo.posted", EntityType: "credit_memo", EntityID: cm.ID, UserID: body.Actor,
				Changes: map[string]interface{}{"number": number, "customer_id": cm.CustomerID, "invoice_id": cm.InvoiceID, "total_cents": total}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if out, err = st.GetCreditMemo(ctx, cm.ID); err != nil {
			return err
		}
		return s.recordCredit(ctx, out, EventCreditPosted, CreditDraft.Status())
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// restockLines moves the restocked lines' quantities into (add) or out of
// (not add) on hand, in (product id, line id) order (section 11 step 6).
func (s *Service) restockLines(ctx context.Context, branchID uuid.UUID, lines []CreditLine, add bool) error {
	var idx []int
	for i := range lines {
		if lines[i].Restock && lines[i].ProductID != nil && lines[i].Quantity != nil {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return nil
	}
	if s.inventory == nil {
		return errors.New("invoice: the stock seam is not wired")
	}
	sort.SliceStable(idx, func(a, b int) bool {
		la, lb := &lines[idx[a]], &lines[idx[b]]
		if la.ProductID.String() != lb.ProductID.String() {
			return la.ProductID.String() < lb.ProductID.String()
		}
		return la.ID.String() < lb.ID.String()
	})
	for _, i := range idx {
		l := &lines[i]
		qty := -*l.Quantity
		if add {
			if err := s.inventory.RestockQty(ctx, *l.ProductID, branchID, qty); err != nil {
				return err
			}
			continue
		}
		if err := s.inventory.UnrestockQty(ctx, *l.ProductID, branchID, qty); err != nil {
			if errors.Is(err, inventory.ErrInsufficientAvailable) {
				return conflictBlocker("restock_consumed", "the restocked goods are no longer on hand: the credit memo cannot be voided")
			}
			return err
		}
	}
	return nil
}

func creditLinesAsDoc(lines []CreditLine) []salesdoc.Line {
	out := make([]salesdoc.Line, len(lines))
	for i := range lines {
		out[i] = lines[i].Line
	}
	return out
}

// voidCredit voids a draft (it consumes no number) or an open credit memo
// with no application: it reverses the entry and the restock, and puts the
// credit back on the receivable.
func (s *Service) voidCredit(ctx context.Context, id uuid.UUID, pre Precondition, body Transition) (*CreditMemo, error) {
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	var out *CreditMemo
	err = s.inTx(ctx, func(ctx context.Context) error {
		head, err := st.GetCreditMemo(ctx, id) // unlocked: learn the customer
		if err != nil {
			return notFound(err)
		}
		if head.Status != CreditDraft {
			if err := requireFinance(body.Role, "voiding a posted credit memo"); err != nil {
				return err
			}
			// Voiding a credit memo adds to the customer's exposure: it takes
			// the customer's credit serialization (section 11 step 1a).
			if err := st.LockCustomerCredit(ctx, head.CustomerID); err != nil {
				return err
			}
		}
		cm, err := st.LockCreditMemo(ctx, id) // step 3
		if err != nil {
			return notFound(err)
		}
		if err := s.checkBranch(ctx, cm.BranchID, "credit memo"); err != nil {
			return err
		}
		if head.Status == CreditDraft && cm.Status != CreditDraft {
			// A post committed between the read and the lock: the finance role
			// check and the credit serialization (step 1a) were skipped on the
			// strength of a draft. Take them on the retry.
			return conflictBlocker("credit_memo_changed", "the credit memo was posted while it was being voided: reload it and retry")
		}
		if err := pre.check(cm.Revision); err != nil {
			return err
		}
		switch cm.Status {
		case CreditVoid:
			return httpx.InvalidStateTransition("the credit memo is already void")
		case CreditPartial, CreditApplied:
			return conflictBlocker("has_applications", "the credit memo has been used: reverse its applications and refunds before voiding it")
		}
		date, err := st.BranchLocalDate(ctx, cm.BranchID, s.now())
		if err != nil {
			return err
		}
		from := cm.Status
		if cm.Status == CreditOpen {
			if err := s.restockLines(ctx, cm.BranchID, cm.Lines, false); err != nil { // step 6
				return err
			}
			if err := st.LockCustomer(ctx, cm.CustomerID); err != nil { // step 7
				return err
			}
			if cm.GLEntryID != nil && s.gl != nil { // step 9
				if _, err := s.gl.PostReversal(ctx, gl.ReversalInput{EntryID: *cm.GLEntryID, EntryDate: date, Currency: cm.Currency,
					Reason: "credit memo voided", PostedBy: body.Actor}); err != nil {
					return mapPostingError(err)
				}
			}
			if s.account != nil {
				cmID := cm.ID
				num := ""
				if cm.Number != nil {
					num = *cm.Number
				}
				if _, err := s.account.PostTransaction(ctx, cm.CustomerID, account.TransactionTypeReversal, -int64(cm.TotalCents), &cmID, "Void of credit memo "+num); err != nil {
					return fmt.Errorf("failed to reverse the credit memo in the account ledger: %w", err)
				}
			}
		}
		if err := st.MarkCreditVoid(ctx, id, body.Actor, body.Reason, date); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_memo.voided", EntityType: "credit_memo", EntityID: id, UserID: body.Actor,
				Changes: map[string]interface{}{"previous_status": from.Status(), "reason": body.Reason, "total_cents": int64(cm.TotalCents)}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if out, err = st.GetCreditMemo(ctx, id); err != nil {
			return err
		}
		return s.recordCredit(ctx, out, EventCreditVoided, from.Status())
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
