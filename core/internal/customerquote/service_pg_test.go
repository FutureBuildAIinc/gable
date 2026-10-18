// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/google/uuid"
)

func TestCreate_PricesTheQuoteFromTheDealersCost(t *testing.T) {
	w := newWorld(t)
	q, err := w.svc.Create(context.Background(), w.a, w.basicInput())
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != customerquote.StatusDraft || !strings.HasPrefix(q.Reference, "CQ-") {
		t.Fatalf("status %s reference %q", q.Status, q.Reference)
	}
	wantCost := map[uuid.UUID]int64{w.stud: 55000, w.ply: 41250, w.nail: 4800}
	wantSell := map[uuid.UUID]int64{w.stud: 68750, w.ply: 51563, w.nail: 6000}
	for _, l := range q.Lines {
		if l.CostCents != wantCost[l.ProductID] || l.SellCents != wantSell[l.ProductID] {
			t.Errorf("%s: cost %d sell %d, want %d and %d", l.Description, l.CostCents, l.SellCents, wantCost[l.ProductID], wantSell[l.ProductID])
		}
		if l.Applied.Tier != customerquote.TierQuote {
			t.Errorf("%s: tier %s, want quote", l.Description, l.Applied.Tier)
		}
	}
	tot, err := q.Totals()
	if err != nil {
		t.Fatal(err)
	}
	want := customerquote.Totals{
		MaterialCostCents: 101050, MaterialSellCents: 126313, LabourCents: 104000, OverheadCents: 15000,
		SubtotalCents: 245313, TaxCents: 10105, TotalCents: 255418, DepositCents: 63855,
		ContractorCostCents: 18909, MarginCents: 125354,
	}
	want.MarginBPS = tot.MarginBPS
	if tot != want {
		t.Errorf("totals\n got %+v\nwant %+v", tot, want)
	}
	if tot.MarginBPS < 5100 || tot.MarginBPS > 5115 {
		t.Errorf("margin bps %d, want about 5110", tot.MarginBPS)
	}
}

func TestCreate_ThreeTiersPickTheMostSpecific(t *testing.T) {
	w := newWorld(t)
	in := w.basicInput()
	in.CategoryRules = []customerquote.RuleInput{{CategoryID: w.catLum.String(), Markup: customerquote.Markup{Kind: customerquote.KindMargin, BPS: 3000}}}
	in.Lines[2].Markup = &customerquote.Markup{Kind: customerquote.KindMarkup, BPS: 5000}
	q, err := w.svc.Create(context.Background(), w.a, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range q.Lines {
		switch l.ProductID {
		case w.stud:
			if l.Applied.Tier != customerquote.TierCategory || l.Applied.Kind != customerquote.KindMargin || l.SellCents != 78571 {
				t.Errorf("stud: %+v sell %d", l.Applied, l.SellCents)
			}
		case w.ply:
			if l.Applied.Tier != customerquote.TierCategory || l.SellCents != 58929 {
				t.Errorf("ply: %+v sell %d", l.Applied, l.SellCents)
			}
		case w.nail:
			if l.Applied.Tier != customerquote.TierLine || l.SellCents != 7200 {
				t.Errorf("nail: %+v sell %d", l.Applied, l.SellCents)
			}
		}
	}
}

func TestCreate_RefusesA100PercentMarginAtEveryTier(t *testing.T) {
	w := newWorld(t)
	for name, mut := range map[string]func(*customerquote.QuoteInput){
		"quote default": func(in *customerquote.QuoteInput) {
			in.Markup = &customerquote.Markup{Kind: customerquote.KindMargin, BPS: 10000}
		},
		"category rule": func(in *customerquote.QuoteInput) {
			in.CategoryRules = []customerquote.RuleInput{{CategoryID: w.catLum.String(), Markup: customerquote.Markup{Kind: customerquote.KindMargin, BPS: 10000}}}
		},
		"line override": func(in *customerquote.QuoteInput) {
			in.Lines[0].Markup = &customerquote.Markup{Kind: customerquote.KindMargin, BPS: 15000}
		},
	} {
		in := w.basicInput()
		mut(in)
		if _, err := w.svc.Create(context.Background(), w.a, in); err == nil {
			t.Errorf("%s: a margin of 100 percent or more was accepted", name)
		}
	}
}

func TestCreate_TheProjectMustBeTheContractorsOwn(t *testing.T) {
	w := newWorld(t)
	in := w.basicInput()
	in.ProjectID = sp(w.projB.String())
	if _, err := w.svc.Create(context.Background(), w.a, in); err == nil {
		t.Fatal("a quote was filed on another contractor's project")
	}
}

func TestCreate_NeedsTheQuotingCapabilities(t *testing.T) {
	w := newWorld(t)
	viewOnly := w.a
	viewOnly.Role = "View-Only"
	if _, err := w.svc.Create(context.Background(), viewOnly, w.basicInput()); !errors.Is(err, customerquote.ErrForbidden) {
		t.Errorf("a View-Only role created a quote: %v", err)
	}
	if _, err := w.svc.Get(context.Background(), viewOnly, uuid.New()); !errors.Is(err, customerquote.ErrForbidden) {
		t.Errorf("a View-Only role read a quote: %v", err)
	}
}

func TestUpdate_KeepsCapturedCostAndNeedsTheRevision(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	q, err := w.svc.Create(ctx, w.a, w.basicInput())
	if err != nil {
		t.Fatal(err)
	}
	callsBefore := w.costs.calls
	// The dealer's price moves; a line that is kept with its id keeps the cost it captured.
	it := w.costs.items[w.stud]
	it.UnitCostTenThousandths = 99000
	w.costs.items[w.stud] = it
	in := w.basicInput()
	id := q.Lines[0].ID.String()
	in.Lines[0].ID = &id
	updated, err := w.svc.Update(ctx, w.a, q.ID, `"1"`, in)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 {
		t.Errorf("revision %d, want 2", updated.Revision)
	}
	for _, l := range updated.Lines {
		if l.ProductID == w.stud && l.CostCents != 55000 {
			t.Errorf("the kept line was repriced to %d", l.CostCents)
		}
		if l.ProductID == w.ply && l.CostCents != 41250 {
			t.Errorf("ply cost %d", l.CostCents)
		}
	}
	if w.costs.calls-callsBefore != 2 {
		t.Errorf("%d price lookups, want 2 (only the two lines without a kept id)", w.costs.calls-callsBefore)
	}
	if _, err := w.svc.Update(ctx, w.a, q.ID, `"1"`, w.basicInput()); err == nil {
		t.Error("an update with a stale revision was accepted")
	}
	if _, err := w.svc.Update(ctx, w.a, q.ID, "", w.basicInput()); err == nil {
		t.Error("an update with no revision was accepted")
	}
}

func TestValidUntil_NeverLaterThanTheDealersPriceProtection(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	// A catalog line: the configured horizon (3 days here) bounds a request for 90.
	if _, err := w.db.Pool.Exec(ctx, `INSERT INTO system_settings (key, value) VALUES ('customer_quote.price_protection_days', '3')
		ON CONFLICT (key) DO UPDATE SET value = '3'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key = 'customer_quote.price_protection_days'`)
	})
	in := w.basicInput()
	far := w.now.Add(90 * 24 * time.Hour)
	in.ValidUntil = tsOf(far)
	q, err := w.svc.Create(ctx, w.a, in)
	if err != nil {
		t.Fatal(err)
	}
	if !q.ValidUntil.Equal(w.now.Add(3 * 24 * time.Hour)) {
		t.Errorf("valid_until %v, want exactly three days on", q.ValidUntil.Sub(w.now))
	}
	// A request inside the horizon is kept.
	in.ValidUntil = tsOf(w.now.Add(24 * time.Hour))
	q, err = w.svc.Update(ctx, w.a, q.ID, `"1"`, in)
	if err != nil {
		t.Fatal(err)
	}
	if !q.ValidUntil.Equal(w.now.Add(24 * time.Hour)) {
		t.Errorf("an inside request moved to %v", q.ValidUntil)
	}
	// A request in the past is refused.
	in.ValidUntil = tsOf(w.now.Add(-time.Hour))
	if _, err := w.svc.Update(ctx, w.a, q.ID, `"2"`, in); err == nil {
		t.Error("a valid_until in the past was accepted")
	}
}

func TestValidUntil_BoundedByTheSourceDealerQuote(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	srcID := uuid.New()
	if _, err := w.db.Pool.Exec(ctx, `INSERT INTO quotes (id, customer_id, state, branch_id) VALUES ($1, $2, 'SENT', $3)`, srcID, w.a.CustomerID, w.branch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.db.Pool.Exec(ctx, `DELETE FROM pro_customer_quotes WHERE source_quote_id = $1`, srcID)
		_, _ = w.db.Pool.Exec(ctx, `DELETE FROM quotes WHERE id = $1`, srcID)
	})
	exp := w.now.Add(36 * time.Hour)
	w.costs.source[srcID] = &customerquote.SourceQuote{ID: srcID, ExpiresAt: &exp, Lines: []customerquote.SourceQuoteLine{
		{ID: uuid.New(), ProductID: w.stud, SKU: "S", Description: "2x4 stud", UOM: "EA", Quantity: qty(t, "100"), UnitCostTenThousandths: 50000},
	}}
	in := w.basicInput()
	in.SourceQuoteID = sp(srcID.String())
	srcLine := w.costs.source[srcID].Lines[0].ID.String()
	in.Lines = []customerquote.LineInput{{ProductID: w.stud.String(), Quantity: qty(t, "100"), SourceLineID: &srcLine}}
	in.ValidUntil = tsOf(w.now.Add(20 * 24 * time.Hour))
	q, err := w.svc.Create(ctx, w.a, in)
	if err != nil {
		t.Fatal(err)
	}
	if !q.ValidUntil.Equal(exp) {
		t.Errorf("valid_until %v, want the source quote's expiry %v", q.ValidUntil, exp)
	}
	if q.Lines[0].PriceBasis != "dealer_quote" || q.Lines[0].UnitCostTenThousandths != 50000 || q.Lines[0].CostCents != 50000 {
		t.Errorf("the source line was not taken at the dealer quote's price: %+v", q.Lines[0])
	}
	// A source quote the account does not own is refused.
	in.SourceQuoteID = sp(uuid.New().String())
	if _, err := w.svc.Create(ctx, w.a, in); err == nil {
		t.Error("an unknown source quote was accepted")
	}
}

func TestSend_NeedsAProjectAContactAndASite(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for name, mut := range map[string]func(*customerquote.QuoteInput){
		"no project": func(in *customerquote.QuoteInput) { in.ProjectID = nil },
		"no contact": func(in *customerquote.QuoteInput) { in.EndCustomer.Email, in.EndCustomer.Phone = "", "" },
		"no site":    func(in *customerquote.QuoteInput) { in.JobSiteAddress = "" },
	} {
		in := w.basicInput()
		mut(in)
		q, err := w.svc.Create(ctx, w.a, in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := w.svc.Send(ctx, w.a, q.ID, ""); err == nil {
			t.Errorf("%s: the quote was sent", name)
		}
	}
}

func TestSend_FreezesTheDraftAndIssuesALinkOnce(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	q, err := w.svc.Create(ctx, w.a, w.basicInput())
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.svc.Send(ctx, w.a, q.ID, `"1"`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Quote.Status != customerquote.StatusSent || res.Quote.SentAt == nil {
		t.Fatalf("status %s", res.Quote.Status)
	}
	if !strings.HasPrefix(res.Path, "/q/") || !strings.HasPrefix(res.URL, "https://quotes.example/q/") {
		t.Fatalf("link %q %q", res.Path, res.URL)
	}
	if !res.Delivered || len(w.mail.links) != 1 || w.mail.links[0] != res.URL {
		t.Errorf("the link was not handed to the mailer: delivered=%v links=%v", res.Delivered, w.mail.links)
	}
	if _, err := w.svc.Update(ctx, w.a, q.ID, `"2"`, w.basicInput()); err == nil {
		t.Error("a sent quote was edited")
	}
	if _, err := w.svc.Send(ctx, w.a, q.ID, ""); err == nil {
		t.Error("a sent quote was sent again")
	}
	// The link lives as long as the quote, no longer.
	link, err := w.svc.LiveLink(ctx, res.Quote)
	if err != nil || link == nil || !link.ExpiresAt.Equal(res.Quote.ValidUntil) {
		t.Errorf("link expiry %v vs valid_until %v (%v)", link, res.Quote.ValidUntil, err)
	}
}

func TestLink_OnlyTheHashIsStoredAndTheTokenIsNeverLogged(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	q, token := w.createSent(w.basicInput())

	var n int
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM pro_portal_share_tokens WHERE quote_id = $1 AND token_hash = $2`,
		q.ID, customerquote.HashToken(token)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the token's hash is not stored: %d, %v", n, err)
	}
	// Nothing in any table holds the raw token.
	for _, probe := range []string{
		`SELECT count(*) FROM pro_portal_share_tokens WHERE token_hash = $1`,
		`SELECT count(*) FROM pro_customer_quote_approval_log WHERE token_hash = $1 OR note = $1`,
		`SELECT count(*) FROM pro_customer_quotes WHERE customer_note = $1 OR title = $1`,
	} {
		if err := w.db.Pool.QueryRow(ctx, probe, token).Scan(&n); err != nil || n != 0 {
			t.Errorf("the raw token is stored (%s): %d, %v", probe, n, err)
		}
	}

	// Exercise every path that could log: a view, a bad token, a wrong code, an expired link.
	if _, err := w.svc.View(ctx, token, anon); err != nil {
		t.Fatal(err)
	}
	_, _ = w.svc.View(ctx, strings.Repeat("A", 43), anon)
	_, _ = w.svc.View(ctx, "short", anon)
	w.advance(400 * 24 * time.Hour)
	_, _ = w.svc.View(ctx, token, anon)
	if strings.Contains(w.logs.String(), token) {
		t.Fatalf("the token reached the log:\n%s", w.logs.String())
	}
}

func TestLink_ExpiredRevokedAndRotated(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	q, token := w.createSent(w.basicInput())

	if _, err := w.svc.View(ctx, token, anon); err != nil {
		t.Fatalf("a fresh link: %v", err)
	}
	if _, err := w.svc.View(ctx, strings.Repeat("Z", 43), anon); !errors.Is(err, customerquote.ErrLinkUnknown) {
		t.Errorf("an unknown token: %v", err)
	}

	// Rotation kills the old link and the new one works.
	res, err := w.svc.Rotate(ctx, w.a, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	newToken := strings.TrimPrefix(res.Path, "/q/")
	if newToken == token {
		t.Fatal("rotation reissued the same token")
	}
	if _, err := w.svc.View(ctx, token, anon); !errors.Is(err, customerquote.ErrLinkUnknown) {
		t.Errorf("the rotated-out link answered: %v", err)
	}
	if _, err := w.svc.View(ctx, newToken, anon); err != nil {
		t.Errorf("the new link: %v", err)
	}
	var live int
	_ = w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM pro_portal_share_tokens WHERE quote_id = $1 AND revoked_at IS NULL`, q.ID).Scan(&live)
	if live != 1 {
		t.Errorf("%d live links, want 1", live)
	}

	// Revocation.
	if err := w.svc.Revoke(ctx, w.a, q.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.View(ctx, newToken, anon); !errors.Is(err, customerquote.ErrLinkUnknown) {
		t.Errorf("the revoked link answered: %v", err)
	}
	if err := w.svc.Revoke(ctx, w.a, q.ID); err == nil {
		t.Error("revoking with no live link succeeded")
	}
	// Rotation after a revoke issues a working link.
	res, err = w.svc.Rotate(ctx, w.a, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	third := strings.TrimPrefix(res.Path, "/q/")
	if _, err := w.svc.View(ctx, third, anon); err != nil {
		t.Errorf("the link after a revoke: %v", err)
	}

	// Expiry: at valid_until the link is gone.
	w.advance(q.ValidUntil.Sub(w.now) + time.Second)
	if _, err := w.svc.View(ctx, third, anon); !errors.Is(err, customerquote.ErrLinkUnavailable) {
		t.Errorf("the expired link answered: %v", err)
	}
}

func TestLink_LifetimeIsBoundedByTheConfiguredMaximum(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.db.Pool.Exec(ctx, `INSERT INTO system_settings (key, value) VALUES ('customer_quote.link_max_days', '2'), ('customer_quote.price_protection_days', '30')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key IN ('customer_quote.link_max_days', 'customer_quote.price_protection_days')`)
	})
	in := w.basicInput()
	in.ValidUntil = tsOf(w.now.Add(20 * 24 * time.Hour))
	q, token := w.createSent(in)
	link, err := w.svc.LiveLink(ctx, q)
	if err != nil || link == nil {
		t.Fatal(err)
	}
	if !link.ExpiresAt.Equal(w.now.Add(48 * time.Hour)) {
		t.Errorf("link expires %v after now, want 48h (the maximum), valid_until is %v", link.ExpiresAt.Sub(w.now), q.ValidUntil.Sub(w.now))
	}
	w.advance(49 * time.Hour)
	if _, err := w.svc.View(ctx, token, anon); !errors.Is(err, customerquote.ErrLinkUnavailable) {
		t.Errorf("a link past the maximum answered: %v", err)
	}
}

func TestLink_RateLimitedPerLinkAndPerAddress(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	_, token := w.createSent(w.basicInput())
	w.svc.WithLimits(3, 1000, 1000)
	for i := 0; i < 3; i++ {
		if _, err := w.svc.View(ctx, token, anon); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	if _, err := w.svc.View(ctx, token, anon); !errors.Is(err, customerquote.ErrRateLimited) {
		t.Errorf("the fourth request in the minute: %v", err)
	}
	// Another address draws on its own budget but the link's budget is shared.
	if _, err := w.svc.View(ctx, token, customerquote.Caller{Address: "198.51.100.1"}); !errors.Is(err, customerquote.ErrRateLimited) {
		t.Errorf("another address on a spent link: %v", err)
	}

	// Per address: unknown tokens from one address run out its miss budget.
	w.svc.WithLimits(1000, 1000, 5)
	var limited bool
	for i := 0; i < 8; i++ {
		_, err := w.svc.View(ctx, strings.Repeat(string(rune('A'+i)), 43), customerquote.Caller{Address: "192.0.2.77"})
		if errors.Is(err, customerquote.ErrRateLimited) {
			limited = true
		}
	}
	if !limited {
		t.Error("an address presenting unknown tokens was never limited")
	}
	w.svc.WithLimits(1000, 4, 1000)
	hits := 0
	for i := 0; i < 8; i++ {
		if _, err := w.svc.View(ctx, token, customerquote.Caller{Address: "192.0.2.88"}); errors.Is(err, customerquote.ErrRateLimited) {
			hits++
		}
	}
	if hits != 4 {
		t.Errorf("%d of 8 requests from one address were limited, want 4", hits)
	}
}

func TestTenancy_AnotherContractorCannotReachAQuote(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	q, _ := w.createSent(w.basicInput())
	draft, err := w.svc.Create(ctx, w.a, w.basicInput())
	if err != nil {
		t.Fatal(err)
	}
	b := w.b
	checks := map[string]error{}
	_, checks["get"] = w.svc.Get(ctx, b, q.ID)
	_, checks["preview"] = w.svc.Preview(ctx, b, q.ID)
	_, checks["pdf"] = w.svc.ContractorPDF(ctx, b, q.ID)
	_, checks["update"] = w.svc.Update(ctx, b, draft.ID, `"1"`, w.basicInput())
	_, checks["send"] = w.svc.Send(ctx, b, draft.ID, "")
	_, checks["rotate"] = w.svc.Rotate(ctx, b, q.ID)
	checks["revoke"] = w.svc.Revoke(ctx, b, q.ID)
	_, checks["withdraw"] = w.svc.Withdraw(ctx, b, q.ID)
	_, checks["revise"] = w.svc.Revise(ctx, b, q.ID)
	_, checks["confirm"] = w.svc.Confirm(ctx, b, q.ID)
	_, checks["approvals"] = w.svc.Approvals(ctx, b, q.ID)
	for name, err := range checks {
		if !errors.Is(err, customerquote.ErrNotFound) && !errors.Is(err, customerquote.ErrForbidden) {
			t.Errorf("%s by another contractor: %v, want not found", name, err)
		}
	}
	list, err := w.svc.List(ctx, b, customerquote.ListFilter{Limit: 50})
	if err != nil || len(list) != 0 {
		t.Errorf("another contractor's list: %d rows, %v", len(list), err)
	}
	ex, err := w.svc.Export(ctx, b)
	if err != nil || len(ex.Quotes) != 0 || len(ex.Customers) != 0 {
		t.Errorf("another contractor's export: %+v, %v", ex, err)
	}
}

func tsOf(t time.Time) *httpxTimestamp { v := httpxTS(t); return &v }

var _ = sha256.Sum256
var _ = hex.EncodeToString
var _ = base64.StdEncoding

func TestLink_UnknownTokensCreateNoPerLinkKeys(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	before := w.svc.PerLinkKeys()
	for i := 0; i < 50; i++ {
		tok := strings.Repeat(string(rune('A'+i%26)), 42) + string(rune('a'+i%26))
		_, _ = w.svc.View(ctx, tok, anon)
		_, _ = w.svc.DealerQuoteView(ctx, tok, anon)
	}
	if after := w.svc.PerLinkKeys(); after != before {
		t.Errorf("unknown tokens created %d per link keys: a random token must cost the limiter nothing", after-before)
	}
	_, token := w.createSent(w.basicInput())
	_, _ = w.svc.View(ctx, token, anon)
	if w.svc.PerLinkKeys() != before+1 {
		t.Errorf("a real link did not get its key")
	}
}
