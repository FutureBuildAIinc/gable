// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// ExchangeIn is a parsed counter exchange: the goods that come back, the
// goods that replace them, what is tendered for the difference and how the
// money goes back when the return is worth more.
type ExchangeIn struct {
	RegisterID     string
	OriginalSaleID *uuid.UUID
	CustomerID     *uuid.UUID
	// RefundMethod is how the surplus goes back when the return is worth more
	// than the replacement: cash out of the drawer, or left as store credit on
	// the account.
	RefundMethod RefundMethod
	Reason       string
	Returned     []ReturnLineIn
	Items        []salesdoc.ParsedLine
	// Tenders pay the difference when the replacement is worth more.
	Tenders    []TenderIn
	PickedUpBy string
}

// ExchangeFigures are the money of an exchange, in cents.
type ExchangeFigures struct {
	// ReturnedCents is the credit the return is worth, net of any restocking
	// fee (a positive figure); RestockingFeeCents is the fee withheld.
	ReturnedCents      httpx.Cents `json:"returned_cents"`
	RestockingFeeCents httpx.Cents `json:"restocking_fee_cents"`
	// ReplacementCents is the replacement sale's total with its tax.
	ReplacementCents httpx.Cents `json:"replacement_cents"`
	// DifferenceCents is the replacement less the return: positive is what the
	// customer pays, negative is what goes back to them.
	DifferenceCents httpx.Cents `json:"difference_cents"`
}

// ExchangeResult is the exchange once made: the return, the sale and the
// figures.
type ExchangeResult struct {
	Return *Return `json:"return"`
	Sale   *Sale   `json:"sale"`
	ExchangeFigures
	// RefundedCents is the surplus paid back in cash (zero when it stayed
	// on the account or there was none).
	RefundedCents httpx.Cents `json:"refunded_cents"`
}

// exchangePreview carries a rolled back exchange's figures out of its
// transaction: the preview runs the whole act and then refuses to commit it.
type exchangePreview struct{ figures ExchangeFigures }

func (e *exchangePreview) Error() string { return "exchange preview: nothing was written" }

// Exchange rings the return and the replacement sale in ONE act: one
// transaction in section 11's lock order (the sale rows, the customer's
// credit, the inventory rows in product order, then the AR core), one credit
// memo and one sale linked to each other. The memo's credit is a leg of the
// sale up to the sale's total, tender is taken only for the difference, and
// money goes back when the return is worth more. With preview true nothing is
// written: the figures come back and the transaction is rolled back.
func (s *Service) Exchange(ctx context.Context, cashierID uuid.UUID, in *ExchangeIn, actor string, preview bool) (*ExchangeResult, error) {
	if s.ar == nil || s.invoices == nil || s.inventory == nil {
		return nil, errNotConfigured
	}
	if in.RefundMethod == RefundCard {
		return nil, conflict("exchange_card_refund",
			"a card refund of an exchange's surplus is made as a card return of the goods through POST /pos/returns, then the replacement is rung as its own sale")
	}
	if s.tax != nil && s.tax.ProviderConfigured() {
		return nil, conflict("exchange_provider_tax",
			"the tax provider prices a sale before its transaction opens, which an exchange cannot: ring the return and the replacement as two acts")
	}
	for i := range in.Tenders {
		t := &in.Tenders[i]
		if t.Method == TenderStoreCredit {
			return nil, invalid(fmt.Sprintf("tenders[%d].method", i), "an exchange applies the returned credit itself: tender only the difference")
		}
		if t.TokenID != "" {
			return nil, conflict("exchange_card_token",
				"the card terminal charge of an exchange's difference is taken as its own sale: record the card tender without a token")
		}
	}
	if len(in.Items) == 0 {
		return nil, invalid("sale_lines", "name at least one replacement line")
	}
	ri := &ReturnIn{RegisterID: in.RegisterID, OriginalSaleID: in.OriginalSaleID, CustomerID: in.CustomerID,
		RefundMethod: RefundAccount, Reason: in.Reason, Lines: in.Returned, Exchange: true}
	st, err := s.priceReturn(ctx, ri, true)
	if err != nil {
		return nil, err
	}
	st.cashierID, st.actor = cashierID, actor
	walkInID, _, err := s.repo.WalkInCustomer(ctx)
	if err != nil {
		return nil, err
	}
	isWalkIn := *st.customerID == walkInID
	var saleCustomer *uuid.UUID
	if !isWalkIn {
		saleCustomer = st.customerID
	}
	if isWalkIn && in.RefundMethod == RefundAccount {
		return nil, conflict("walk_in_account",
			"store credit needs a named customer: a walk-in exchange pays any surplus back in cash")
	}
	var out *ExchangeResult
	err = s.inTx(ctx, func(ctx context.Context) error {
		// Section 11 step 1: the sale rows. The original sale first (a return
		// or a void of it serializes here), then the replacement, which this
		// act creates and no one else can hold.
		if in.OriginalSaleID != nil {
			if err := s.repo.LockSale(ctx, *in.OriginalSaleID); err != nil {
				return err
			}
		}
		// Section 11 step 1a: the customer's credit serialization, unconditionally
		// taken right after the sale row and before any stock or AR row.
		if saleCustomer != nil {
			if err := s.repo.LockCustomerCredit(ctx, *saleCustomer); err != nil {
				return err
			}
		}
		// The replacement sale, its lines priced as the counter prices them.
		sale, err := s.StartSale(ctx, &StartSale{RegisterID: in.RegisterID, CashierID: cashierID, CustomerID: saleCustomer}, actor)
		if err != nil {
			return err
		}
		for i := range in.Items {
			if _, err := s.AddLine(ctx, sale.ID, []salesdoc.ParsedLine{in.Items[i]}, actor); err != nil {
				return err
			}
		}
		lines, err := s.repo.GetLines(ctx, sale.ID)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			return invalid("sale_lines", "name at least one replacement line")
		}
		// Step 6: the inventory rows of every product the act touches, in
		// product order, before any AR row (step 7): the restock of the
		// returned goods and the issue of the replacement both find them held.
		if err := s.lockExchangeStock(ctx, st, lines, sale.BranchID); err != nil {
			return err
		}
		// The return half: the memo posted and left open, the goods restocked.
		ret, err := s.postReturn(ctx, st, nil)
		if err != nil {
			return err
		}
		if ret.CreditMemoID == nil {
			return fmt.Errorf("the exchange's return carries no credit memo")
		}
		returned := -int64(ret.TotalCents)
		// The sale half: the memo's credit is a leg up to the total, tender
		// covers the rest.
		leg := &exchangeLeg{memoID: *ret.CreditMemoID, creditCents: returned, preview: preview}
		pre, err := s.repo.GetSale(ctx, sale.ID)
		if err != nil {
			return err
		}
		pre.WalkInID = walkInID
		revision := pre.Revision
		done, err := s.completeSaleTx(ctx, sale.ID, "", &revision, in.Tenders, in.PickedUpBy, actor, pre, *st.customerID,
			make([]*chargeResult, len(in.Tenders)), nil, leg)
		if err != nil {
			return err
		}
		figures := ExchangeFigures{ReturnedCents: httpx.Cents(returned), RestockingFeeCents: httpx.Cents(st.feeCents),
			ReplacementCents: done.TotalCents, DifferenceCents: httpx.Cents(leg.totalCents - returned)}
		// The surplus when the return is worth more: cash out of the open
		// drawer, or the memo's remainder left open on the account.
		surplus := returned - leg.appliedCents
		method := in.RefundMethod
		var refunded int64
		if surplus > 0 {
			if method == RefundCash {
				fx, err := s.refundSurplus(ctx, st, *ret.CreditMemoID, surplus, in.Reason, actor)
				if err != nil {
					return err
				}
				for _, ev := range fx.Events() {
					if err := s.recordEvent(ctx, ev); err != nil {
						return err
					}
				}
				refunded = surplus
			}
		} else {
			// nothing left over: the credit was all used, none left on an account
			method = RefundAccount
		}
		if err := s.repo.LinkExchange(ctx, ret.ID, sale.ID, method); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.exchange.completed", EntityType: "pos_transaction", EntityID: sale.ID, UserID: actor,
				Changes: map[string]any{"return_id": ret.ID, "credit_memo_id": ret.CreditMemoID, "returned_cents": returned,
					"replacement_cents": int64(done.TotalCents), "difference_cents": int64(figures.DifferenceCents),
					"refunded_cents": refunded, "customer_id": saleCustomer}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if preview {
			return &exchangePreview{figures: figures}
		}
		retOut, err := s.repo.GetReturn(ctx, ret.ID)
		if err != nil {
			return err
		}
		saleOut, err := s.GetSale(ctx, sale.ID)
		if err != nil {
			return err
		}
		out = &ExchangeResult{Return: retOut, Sale: saleOut, ExchangeFigures: figures, RefundedCents: httpx.Cents(refunded)}
		return nil
	})
	var pv *exchangePreview
	if errors.As(err, &pv) {
		return &ExchangeResult{ExchangeFigures: pv.figures}, nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// lockExchangeStock takes the inventory rows of every product the exchange
// restocks or issues, in (product id, inventory id) order (section 11,
// step 6), so the two halves of the act and any other act agree on the order.
func (s *Service) lockExchangeStock(ctx context.Context, st *returnState, lines []salesdoc.Line, branch uuid.UUID) error {
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	add := func(id *uuid.UUID) {
		if id != nil && !seen[*id] {
			seen[*id] = true
			ids = append(ids, *id)
		}
	}
	for i := range st.priced {
		p := &st.priced[i]
		add(p.in.ProductID)
		// a returned kit restocks its components
		if p.saleLine != nil && p.saleLine.LineType == salesdoc.LineKit {
			for j := range st.saleLines {
				if st.saleLines[j].ParentLineID != nil && *st.saleLines[j].ParentLineID == p.saleLine.ID {
					add(st.saleLines[j].ProductID)
				}
			}
		}
	}
	for i := range lines {
		if lines[i].IsStocked() {
			add(lines[i].ProductID)
		}
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a].String() < ids[b].String() })
	return s.repo.LockStock(ctx, ids, branch)
}

// refundSurplus pays the memo's remainder back in cash out of the register's
// open drawer, through the AR core, the way a cash return pays out.
func (s *Service) refundSurplus(ctx context.Context, st *returnState, memoID uuid.UUID, cents int64, reason, actor string) (*account.Effects, error) {
	session, err := s.repo.GetOpenTillSession(ctx, st.in.RegisterID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, conflict("no_open_session",
			"a cash refund pays out of the drawer: open the till session before refunding cash")
	}
	if err := s.repo.LockTillSession(ctx, session.ID, false); err != nil {
		return nil, err
	}
	held, err := s.repo.GetTillSession(ctx, session.ID)
	if err != nil {
		return nil, err
	}
	if held.Status != TillOpen {
		return nil, conflict("session_closed",
			"the till session closed while the exchange was made: open the drawer and take it again")
	}
	date, err := s.repo.BranchLocalDate(ctx, *st.branchID, s.now())
	if err != nil {
		return nil, err
	}
	_, fx, err := s.ar.RefundCreditMemo(ctx, account.RefundCreditMemoIn{
		MemoID: memoID, AmountCents: cents, Reason: reason, Method: string(RefundCash), Actor: actor, On: date})
	if err != nil {
		return nil, err
	}
	branch := *st.branchID
	if err := s.recordEvent(ctx, outbox.Event{Type: "credit_memo.refunded", EntityType: "credit_memo", EntityID: memoID,
		BranchID: &branch, Data: eventJSON(map[string]any{
			"customer_id": st.customerID, "status": "applied", "currency": st.currency,
			"amount_cents": cents, "method": "cash"})}); err != nil {
		return nil, err
	}
	return fx, nil
}

// exchangeRequest is POST /pos/exchanges and POST /pos/exchanges/preview.
type exchangeRequest struct {
	RegisterID   *string                `json:"register_id"`
	OriginalSale *string                `json:"original_sale_id"`
	CustomerID   *string                `json:"customer_id"`
	RefundMethod string                 `json:"refund_method"`
	Reason       *string                `json:"reason"`
	Returned     []returnLineRequest    `json:"returned_lines"`
	SaleLines    []salesdoc.LineRequest `json:"sale_lines"`
	Tenders      []tenderRequest        `json:"tenders"`
	PickedUpBy   *string                `json:"picked_up_by"`
}

func (r *exchangeRequest) parse() (*ExchangeIn, error) {
	// the return half reads as a return does, its lines named returned_lines
	rr := &returnRequest{RegisterID: r.RegisterID, OriginalSale: r.OriginalSale, CustomerID: r.CustomerID,
		RefundMethod: r.RefundMethod, Reason: r.Reason, Lines: r.Returned}
	ri, rerr := rr.parseWith("returned_lines")
	v := &httpx.Validator{}
	out := &ExchangeIn{}
	if ri != nil {
		out.RegisterID, out.OriginalSaleID, out.CustomerID = ri.RegisterID, ri.OriginalSaleID, ri.CustomerID
		out.RefundMethod, out.Reason, out.Returned = ri.RefundMethod, ri.Reason, ri.Lines
	}
	v.Check(len(r.SaleLines) > 0, "sale_lines", "name at least one replacement line")
	out.Items = salesdoc.ParseLines(v, r.SaleLines)
	out.Tenders = parseTenders(v, "tenders", r.Tenders)
	if r.PickedUpBy != nil {
		out.PickedUpBy = *r.PickedUpBy
		v.Check(len(out.PickedUpBy) <= 200, "picked_up_by", "must be at most 200 characters")
	}
	verr := v.Err()
	switch {
	case rerr != nil && verr != nil:
		var a, b *httpx.Error
		if errors.As(rerr, &a) && errors.As(verr, &b) {
			a.Details = append(a.Details, b.Details...)
			return nil, a
		}
		return nil, rerr
	case rerr != nil:
		return nil, rerr
	case verr != nil:
		return nil, verr
	}
	return out, nil
}

// CreateExchange is POST /pos/exchanges: the return and the replacement sale
// in one act.
func (h *Handler) CreateExchange(w http.ResponseWriter, r *http.Request) {
	h.exchange(w, r, false)
}

// PreviewExchange is POST /pos/exchanges/preview: the figures of the same
// request, computed by running the act and rolling it back, so the counter
// states what is owed or what goes back before anything is rung.
func (h *Handler) PreviewExchange(w http.ResponseWriter, r *http.Request) {
	h.exchange(w, r, true)
}

func (h *Handler) exchange(w http.ResponseWriter, r *http.Request, preview bool) {
	cashierID, ok := cashierOf(w, r)
	if !ok {
		return
	}
	var req exchangeRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.service.Exchange(r.Context(), cashierID, in, cashierID.String(), preview)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if preview {
		writeJSON(w, http.StatusOK, res.ExchangeFigures)
		return
	}
	w.Header().Set("Location", "/api/v1/pos/returns/"+res.Return.ID.String())
	writeJSON(w, http.StatusCreated, res)
}
