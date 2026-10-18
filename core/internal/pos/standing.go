// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"fmt"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// The verdict words and reasons of "can this sale go on account?".
// The reasons are blocker codes: the completion refuses with
// the same code the read states.
const (
	VerdictAllowed = "allowed"
	VerdictBlocked = "blocked"

	// ReasonCreditHold is the hold the counter has today: a credit limit of
	// zero, no credit given. ReasonCreditLimit is a sale that would put the
	// account over a limit. ReasonCashOnlyTerms is an account whose terms
	// are due on receipt. ReasonManagerOverride marks an allowed verdict a
	// manager made.
	ReasonCreditHold      = "credit_hold"
	ReasonCreditLimit     = "credit_limit"
	ReasonCashOnlyTerms   = "cash_only_terms"
	ReasonManagerOverride = "manager_override"

	// The three ways forward from a blocked verdict.
	WayPayDown            = "pay_down"
	WayManagerOverride    = "manager_override"
	WaySwitchTender       = "switch_tender"
	termsKindDueOnReceipt = "DUE_ON_RECEIPT"
)

// WayForward is one thing the counter can do about a blocked verdict.
type WayForward struct {
	Kind      string `json:"kind"`
	Available bool   `json:"available"`
	// AmountCents, for a pay down, is what paying would clear.
	AmountCents *httpx.Cents `json:"amount_cents,omitempty"`
	Message     string       `json:"message"`
}

// OnAccountVerdict is the one answer to "can this go on account?". The read
// and the completion both ask onAccountVerdict, so the two cannot drift.
type OnAccountVerdict struct {
	Verdict string `json:"verdict"`
	// Reason is empty when plainly allowed; the blocker code when blocked;
	// manager_override when allowed by a live override.
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// OverCents is how far past the limit the sale would put the account
	// (a credit_limit refusal only).
	OverCents   *httpx.Cents `json:"over_cents"`
	WaysForward []WayForward `json:"ways_forward"`
}

// verdictIn is everything the verdict reads: the account as it stands and
// the amount the sale would put on it. A later hold record (M25) adds its
// own field here and its own reason in onAccountVerdict, nowhere else.
type verdictIn struct {
	currency    string
	creditLimit *int64 // null is unlimited
	open        int64  // the open receivable the credit check reads
	termsKind   string // the terms master's kind
	amount      int64  // the account portion the sale asks for
}

// overridable names the reasons a manager may waive for one sale: the terms
// of an account are the dealer's, never one sale's.
func overridable(reason string) bool {
	return reason == ReasonCreditHold || reason == ReasonCreditLimit
}

// onAccountVerdict decides whether in.amount may go on the account. It is a
// pure function of the account's facts.
func onAccountVerdict(in verdictIn) OnAccountVerdict {
	money := func(c int64) string { return moneyWords(in.currency, c) }
	switchTender := WayForward{Kind: WaySwitchTender, Available: true,
		Message: "Take the payment by cash, check or card instead of putting it on the account."}
	switch {
	case in.termsKind == termsKindDueOnReceipt:
		msg := "This account's terms are due on receipt: it is paid at the counter and nothing can be put on the account."
		return OnAccountVerdict{Verdict: VerdictBlocked, Reason: ReasonCashOnlyTerms, Message: msg,
			WaysForward: []WayForward{
				{Kind: WayPayDown, Available: false, Message: "Paying the balance does not change the account's terms."},
				{Kind: WayManagerOverride, Available: false, Message: "A manager cannot waive the terms of an account for one sale."},
				switchTender,
			}}
	case in.creditLimit != nil && *in.creditLimit <= 0:
		msg := "This account is on hold: it has no credit, so nothing can be put on it."
		return OnAccountVerdict{Verdict: VerdictBlocked, Reason: ReasonCreditHold, Message: msg,
			WaysForward: []WayForward{
				{Kind: WayPayDown, Available: false, Message: "Paying the balance does not lift a hold: the account has no credit limit."},
				{Kind: WayManagerOverride, Available: true, Message: "A manager can approve this sale on account."},
				switchTender,
			}}
	case in.creditLimit != nil && in.open+in.amount > *in.creditLimit:
		over := httpx.Cents(in.open + in.amount - *in.creditLimit)
		clear := over
		msg := fmt.Sprintf("This sale would put the account %s over its credit limit of %s.", money(int64(over)), money(*in.creditLimit))
		if in.amount == 0 {
			msg = fmt.Sprintf("This account is %s over its credit limit of %s.", money(int64(over)), money(*in.creditLimit))
		}
		return OnAccountVerdict{Verdict: VerdictBlocked, Reason: ReasonCreditLimit, Message: msg, OverCents: &over,
			WaysForward: []WayForward{
				{Kind: WayPayDown, Available: true, AmountCents: &clear,
					Message: fmt.Sprintf("Take %s on the account to clear it.", money(int64(clear)))},
				{Kind: WayManagerOverride, Available: true, Message: "A manager can approve this sale on account."},
				switchTender,
			}}
	}
	return OnAccountVerdict{Verdict: VerdictAllowed, WaysForward: []WayForward{}}
}

// withOverride turns a blocked verdict the override covers into an allowed
// one that says a manager made it. A verdict that is not blocked, a reason a
// manager may not waive, or an amount over the cap stays as it was.
func withOverride(v OnAccountVerdict, amount int64, o *AccountOverride, currency string) OnAccountVerdict {
	if v.Verdict != VerdictBlocked || !overridable(v.Reason) || o == nil || amount <= 0 || amount > o.CapCents {
		return v
	}
	return OnAccountVerdict{Verdict: VerdictAllowed, Reason: ReasonManagerOverride, WaysForward: []WayForward{},
		Message: fmt.Sprintf("A manager approved this sale on account up to %s.", moneyWords(currency, o.CapCents))}
}

// verdictError is the refusal the completion answers for a blocked verdict:
// the verdict's reason as the blocker code, its words as the message.
func verdictError(v OnAccountVerdict) error {
	return conflict(v.Reason, v.Message)
}

// moneyWords writes cents as the amount in plain words: grouped, with two
// decimals and the currency code after it.
func moneyWords(currency string, cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	whole := fmt.Sprintf("%d", cents/100)
	var grouped []string
	for len(whole) > 3 {
		grouped = append([]string{whole[len(whole)-3:]}, grouped...)
		whole = whole[:len(whole)-3]
	}
	grouped = append([]string{whole}, grouped...)
	out := fmt.Sprintf("%s%s.%02d", sign, strings.Join(grouped, ","), cents%100)
	if currency != "" {
		out += " " + currency
	}
	return out
}

// Buyer is a contact the account's roster names as a buyer.
type Buyer struct {
	ID       uuid.UUID    `json:"id"`
	Name     string       `json:"name"`
	CapCents *httpx.Cents `json:"cap_cents"`
}

// AccountStanding is the account as the counter reads it before a sale goes
// on it (GET /pos/customers/{id}/standing).
type AccountStanding struct {
	CustomerID uuid.UUID `json:"customer_id"`
	Name       string    `json:"name"`
	Currency   string    `json:"currency"`
	// CreditLimitCents is null for no limit (unlimited), zero for no credit.
	CreditLimitCents *httpx.Cents `json:"credit_limit_cents"`
	// OpenReceivableCents is the figure the credit check reads: open
	// invoices, less open credit memos and unapplied cash.
	OpenReceivableCents  httpx.Cents  `json:"open_receivable_cents"`
	OpenCreditMemoCents  httpx.Cents  `json:"open_credit_memo_cents"`
	AvailableCreditCents *httpx.Cents `json:"available_credit_cents"`
	// OnHold is the hold the counter has today: a credit limit of zero.
	OnHold             bool         `json:"on_hold"`
	TermsKind          string       `json:"terms_kind"`
	NamedBuyerRequired bool         `json:"named_buyer_required"`
	Buyers             []Buyer      `json:"buyers"`
	CheckLimitCents    *httpx.Cents `json:"check_limit_cents"`
	// PaymentOptions are the payment shapes the server will take from this
	// customer now, in the order the counter shows them.
	PaymentOptions []string         `json:"payment_options"`
	OnAccount      OnAccountVerdict `json:"on_account"`
}

// Standing reads whether a sale of amount cents can go on the account of the
// customer. saleID, when named, makes the read count the live manager
// override of that sale. The customer goes through the counter's one rule
// (namedCustomer): a customer the caller may not name is the module's 404,
// and the walk-in customer is no account.
func (s *Service) Standing(ctx context.Context, customerID uuid.UUID, amount int64, saleID *uuid.UUID) (*AccountStanding, error) {
	if saleID != nil {
		if err := s.holdSaleBranch(ctx, *saleID); err != nil {
			return nil, err
		}
	}
	_, facts, err := s.namedCustomer(ctx, customerID)
	if err != nil {
		return nil, err
	}
	walkInID, _, err := s.repo.WalkInCustomer(ctx)
	if err != nil {
		return nil, err
	}
	if walkInID == customerID {
		return nil, httpx.NotFound("customer not found")
	}
	open, err := s.repo.OpenReceivableCents(ctx, customerID)
	if err != nil {
		return nil, err
	}
	memoCents, err := s.repo.OpenCreditMemoCents(ctx, customerID)
	if err != nil {
		return nil, err
	}
	buyers, err := s.repo.Buyers(ctx, customerID)
	if err != nil {
		return nil, err
	}
	checkLimit, err := s.checkLimitOf(ctx, facts)
	if err != nil {
		return nil, err
	}
	out := &AccountStanding{CustomerID: customerID, Name: facts.Name, Currency: facts.Currency,
		OpenReceivableCents: httpx.Cents(open), OpenCreditMemoCents: httpx.Cents(memoCents),
		TermsKind: strings.ToLower(facts.TermsKind), NamedBuyerRequired: facts.NamedBuyerRequired, Buyers: buyers}
	if facts.CreditLimitCents != nil {
		limit := httpx.Cents(*facts.CreditLimitCents)
		out.CreditLimitCents = &limit
		avail := httpx.Cents(*facts.CreditLimitCents - open)
		if *facts.CreditLimitCents <= 0 {
			avail = 0
			out.OnHold = true
		}
		out.AvailableCreditCents = &avail
	}
	if checkLimit != nil {
		c := httpx.Cents(*checkLimit)
		out.CheckLimitCents = &c
	}
	v := onAccountVerdict(verdictIn{currency: facts.Currency, creditLimit: facts.CreditLimitCents, open: open,
		termsKind: facts.TermsKind, amount: amount})
	if saleID != nil {
		sale, err := s.repo.GetSale(ctx, *saleID)
		if err != nil {
			return nil, err
		}
		if sale.CustomerID == nil || *sale.CustomerID != customerID {
			return nil, invalid("sale_id", "is not a sale of this customer")
		}
		o, err := s.repo.LiveOverride(ctx, *saleID, OverrideAccount)
		if err != nil {
			return nil, err
		}
		v = withOverride(v, amount, o, facts.Currency)
	}
	out.OnAccount = v
	// the shapes of payment the server takes: cash and card always, a check
	// unless the customer's check limit is zero, the account when the verdict
	// at no amount allows it (an override is the sale's, not the customer's),
	// and store credit while any is open
	out.PaymentOptions = []string{"cash"}
	if checkLimit == nil || *checkLimit > 0 {
		out.PaymentOptions = append(out.PaymentOptions, "check")
	}
	out.PaymentOptions = append(out.PaymentOptions, "card")
	if onAccountVerdict(verdictIn{currency: facts.Currency, creditLimit: facts.CreditLimitCents, open: open,
		termsKind: facts.TermsKind}).Verdict == VerdictAllowed {
		out.PaymentOptions = append(out.PaymentOptions, "account")
	}
	if memoCents > 0 {
		out.PaymentOptions = append(out.PaymentOptions, "store_credit")
	}
	return out, nil
}

// checkLimitOf is the check limit that governs the customer: its own, else
// the database wide default; nil is no limit.
func (s *Service) checkLimitOf(ctx context.Context, facts CustomerFacts) (*int64, error) {
	if facts.CheckLimitCents != nil {
		return facts.CheckLimitCents, nil
	}
	return s.repo.DefaultCheckLimitCents(ctx)
}
