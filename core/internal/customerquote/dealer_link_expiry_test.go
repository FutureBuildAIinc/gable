// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote_test

// QE-01-a: the public dealer link's accept agrees with the quote module's one
// expiry refusal. A contractor accepting a quote past its Valid until is
// refused with the ruling's words, not the link's own open-check phrasing,
// because the decision runs through the quote lifecycle, where the one rule
// lives.

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/outbox"
)

var qe01WordsRe = regexp.MustCompile(
	`^This quote expired on [A-Z][a-z]{2} \d{1,2}, \d{4}: revise it with a new valid until date to send or convert it$`)

func TestQE01_ExpiredDealerLinkAcceptRefused(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	// The real quote lifecycle, as serve wires it behind the link: the one
	// expiry rule lives there, so the link's refusal is its refusal.
	quoteSvc := quote.NewService(quote.NewRepository(w.db)).
		WithOutbox(outbox.NewWriter(w.db, "")).WithTxRunner(w.db)
	stillGood := w.now.Add(24 * time.Hour)
	id, _, auditLog := w.dealerQuote("SENT", &stillGood)
	w.svc.WithDealerQuotes(customerquote.QuoteLifecycle{Quotes: quoteSvc}, auditLog)

	res, err := w.svc.ShareDealerQuote(ctx, desk, id, "")
	if err != nil {
		t.Fatalf("share the sent quote while its date is good: %v", err)
	}
	token := strings.TrimPrefix(res.Path, "/dq/")

	// The quote's Valid until passes while the link is out, as time alone
	// moves it: a sent quote takes no edits.
	if _, err := w.db.Pool.Exec(ctx,
		`UPDATE quotes SET expires_at = $1 WHERE id = $2`, w.now.Add(-time.Hour), id); err != nil {
		t.Fatalf("age the quote past its date: %v", err)
	}

	// The page the contractor opens tells them it has expired, and offers no
	// decision.
	v, err := w.svc.DealerQuoteView(ctx, token, anon)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "expired" || v.CanDecide {
		t.Fatalf("view status=%s canDecide=%v, want expired and no decision", v.Status, v.CanDecide)
	}

	// The acceptance is refused with the ruling's words, the same ones the
	// desk's transition answers.
	_, err = w.svc.DealerQuoteDecide(ctx, token, anon, true, "Corey Contractor", "")
	if err == nil {
		t.Fatal("an accept of a quote past its Valid until was allowed")
	}
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 409 {
		t.Fatalf("the refusal = %v, want the desk's own 409", err)
	}
	if !qe01WordsRe.MatchString(he.Message) {
		t.Errorf("the refusal's message = %q, want the ruling's words", he.Message)
	}
	found := false
	for _, d := range he.Details {
		if d.Code == "quote_expired" {
			found = true
		}
	}
	if !found {
		t.Errorf("the refusal's details = %+v, want the quote_expired blocker", he.Details)
	}

	// The quote stands as it was: still SENT, not accepted.
	d, err := quoteSvc.GetQuote(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != quote.QuoteStateSent {
		t.Errorf("the quote's state after the refused accept = %s, want SENT", d.Status)
	}
}
