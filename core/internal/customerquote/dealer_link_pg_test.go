// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// fakeDealerQuotes is a dealer quote lifecycle that decides once, as the quote
// module does.
type fakeDealerQuotes struct {
	mu    sync.Mutex
	docs  map[uuid.UUID]*customerquote.DealerQuoteDoc
	calls int
}

func (f *fakeDealerQuotes) Read(_ context.Context, id uuid.UUID) (*customerquote.DealerQuoteDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.docs[id]
	if !ok {
		return nil, httpx.NotFound("quote not found")
	}
	cp := *d
	return &cp, nil
}

func (f *fakeDealerQuotes) ReadByLink(ctx context.Context, id uuid.UUID) (*customerquote.DealerQuoteDoc, error) {
	d, err := f.Read(ctx, id)
	if err != nil {
		return nil, nil
	}
	return d, nil
}

func (f *fakeDealerQuotes) Decide(_ context.Context, id uuid.UUID, accept bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	d := f.docs[id]
	if d.State != "SENT" {
		return httpx.InvalidStateTransition("the quote is not sent")
	}
	if accept {
		d.State = "ACCEPTED"
	} else {
		d.State = "REJECTED"
	}
	return nil
}

type recAudit struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (r *recAudit) Log(_ context.Context, e audit.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	return nil
}

// dealerQuoteWorld adds a dealer quote (a real quotes row for the foreign key,
// a fake lifecycle for the decision) to a world.
func (w *world) dealerQuote(state string, expires *time.Time) (uuid.UUID, *fakeDealerQuotes, *recAudit) {
	w.t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if _, err := w.db.Pool.Exec(ctx, `INSERT INTO quotes (id, customer_id, state, branch_id) VALUES ($1, $2, 'SENT', $3)`, id, w.a.CustomerID, w.branch); err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _, _ = w.db.Pool.Exec(ctx, `DELETE FROM quotes WHERE id = $1`, id) })
	f := &fakeDealerQuotes{docs: map[uuid.UUID]*customerquote.DealerQuoteDoc{id: {
		ID: id, Number: "Q-000777", State: state, CustomerName: "Quote Contractor A", ExpiresAt: expires,
		FreightCents: 2500, TotalCents: 77500,
		Lines: []customerquote.DealerQuoteLine{{Description: "2x4 stud", Quantity: qty(w.t, "100"), UOM: "EA", PriceUOM: "EA", UnitPrice: 7500, LineTotal: 75000}},
	}}}
	a := &recAudit{}
	w.svc.WithDealerQuotes(f, a)
	return id, f, a
}

var desk = customerquote.DealerActor{Ref: "desk-1", Name: "Desk User"}

func TestDealerLink_OnlyASentUnexpiredQuoteGetsALinkAndAReissueKillsTheOld(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	exp := w.now.Add(10 * 24 * time.Hour)
	id, _, a := w.dealerQuote("SENT", &exp)

	res, err := w.svc.ShareDealerQuote(ctx, desk, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Path, "/dq/") || len(res.Path) != len("/dq/")+43 || !res.ExpiresAt.Equal(exp) {
		t.Fatalf("link %+v", res)
	}
	token := strings.TrimPrefix(res.Path, "/dq/")
	var n int
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM pro_portal_share_tokens WHERE dealer_quote_id = $1 AND quote_id IS NULL AND token_hash = $2`,
		id, customerquote.HashToken(token)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the token's hash is not stored against the dealer quote: %d, %v", n, err)
	}
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM pro_portal_share_tokens WHERE token_hash = $1`, token).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the raw token is stored: %d", n)
	}
	if len(a.entries) != 1 || a.entries[0].Action != "quote.share_link_issued" || strings.Contains(strings.Join(entryStrings(a.entries[0]), " "), token) {
		t.Errorf("audit %+v", a.entries)
	}
	v, err := w.svc.DealerQuoteView(ctx, token, anon)
	if err != nil || v.Status != "open" || !v.CanDecide || v.TotalCents != 77500 || len(v.Lines) != 1 {
		t.Fatalf("view %+v, %v", v, err)
	}

	// A second link kills the first.
	res2, err := w.svc.ShareDealerQuote(ctx, desk, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.DealerQuoteView(ctx, token, anon); !errors.Is(err, customerquote.ErrLinkUnknown) {
		t.Errorf("the replaced link: %v", err)
	}
	if _, err := w.svc.DealerQuoteView(ctx, strings.TrimPrefix(res2.Path, "/dq/"), anon); err != nil {
		t.Errorf("the new link: %v", err)
	}

	// Not sent, expired, unknown: no link.
	for name, st := range map[string]string{"draft": "DRAFT", "accepted": "ACCEPTED", "rejected": "REJECTED"} {
		other, _, _ := w.dealerQuote(st, &exp)
		if _, err := w.svc.ShareDealerQuote(ctx, desk, other, ""); !errors.Is(err, customerquote.ErrInvalidState) {
			t.Errorf("%s quote: %v", name, err)
		}
	}
	past := w.now.Add(-time.Hour)
	old, _, _ := w.dealerQuote("SENT", &past)
	if _, err := w.svc.ShareDealerQuote(ctx, desk, old, ""); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Errorf("an expired quote: %v", err)
	}
	if _, err := w.svc.ShareDealerQuote(ctx, desk, uuid.New(), ""); err == nil {
		t.Error("a quote that does not exist got a link")
	}
	if _, err := w.svc.ShareDealerQuote(ctx, desk, id, "not an email"); err == nil {
		t.Error("a bad address was accepted")
	}
}

func entryStrings(e audit.Entry) []string {
	b, _ := json.Marshal(e)
	return []string{string(b)}
}

func TestDealerLink_LifetimeIsBoundedByTheMaximum(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.db.Pool.Exec(ctx, `INSERT INTO system_settings (key, value) VALUES ('customer_quote.link_max_days', '2')
		ON CONFLICT (key) DO UPDATE SET value = '2'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key = 'customer_quote.link_max_days'`)
	})
	far := w.now.Add(30 * 24 * time.Hour)
	id, _, _ := w.dealerQuote("SENT", &far)
	res, err := w.svc.ShareDealerQuote(ctx, desk, id, "")
	if err != nil || !res.ExpiresAt.Equal(w.now.Add(48*time.Hour)) {
		t.Fatalf("expiry %v, %v", res, err)
	}
	// No expiry on the quote: the maximum alone.
	id2, _, _ := w.dealerQuote("SENT", nil)
	res2, err := w.svc.ShareDealerQuote(ctx, desk, id2, "")
	if err != nil || !res2.ExpiresAt.Equal(w.now.Add(48*time.Hour)) {
		t.Fatalf("expiry %v, %v", res2, err)
	}
	w.advance(49 * time.Hour)
	if _, err := w.svc.DealerQuoteView(ctx, strings.TrimPrefix(res.Path, "/dq/"), anon); !errors.Is(err, customerquote.ErrLinkUnavailable) {
		t.Errorf("a link past the maximum: %v", err)
	}
}

func TestDealerLink_AcceptOnceWithAName_AndTheRecord(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	exp := w.now.Add(10 * 24 * time.Hour)
	id, f, a := w.dealerQuote("SENT", &exp)
	res, _ := w.svc.ShareDealerQuote(ctx, desk, id, "")
	token := strings.TrimPrefix(res.Path, "/dq/")

	if _, err := w.svc.DealerQuoteDecide(ctx, token, anon, true, "", ""); err == nil {
		t.Error("accepted without a name")
	}
	if _, err := w.svc.DealerQuoteDecide(ctx, token, anon, true, "P", ""); err == nil {
		t.Error("accepted with a one letter name")
	}
	if f.calls != 0 {
		t.Fatalf("a refused acceptance reached the lifecycle %d times", f.calls)
	}
	v, err := w.svc.DealerQuoteDecide(ctx, token, anon, true, "Pat Contractor", "")
	if err != nil || v.Status != "accepted" || v.CanDecide {
		t.Fatalf("accept: %+v, %v", v, err)
	}
	last := a.entries[len(a.entries)-1]
	if last.Action != "quote.accepted_from_link" || last.EntityID != id || last.Changes["decided_by"] != "Pat Contractor" ||
		last.Changes["source_address"] != "203.0.113.9" || last.Changes["user_agent"] != "cq-test" || last.Changes["token_hash"] != customerquote.HashToken(token) {
		t.Errorf("audit %+v", last)
	}
	if _, err := w.svc.DealerQuoteDecide(ctx, token, anon, false, "Pat Contractor", "changed my mind"); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Errorf("deciding twice: %v", err)
	}
	// The link keeps showing the decision.
	v, err = w.svc.DealerQuoteView(ctx, token, anon)
	if err != nil || v.Status != "accepted" {
		t.Errorf("view after: %+v %v", v, err)
	}
}

func TestDealerLink_DeclineRecordsTheReason(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	exp := w.now.Add(24 * time.Hour)
	id, _, a := w.dealerQuote("SENT", &exp)
	res, _ := w.svc.ShareDealerQuote(ctx, desk, id, "")
	token := strings.TrimPrefix(res.Path, "/dq/")
	v, err := w.svc.DealerQuoteDecide(ctx, token, anon, false, "Pat Contractor", "found it cheaper")
	if err != nil || v.Status != "declined" {
		t.Fatalf("decline: %+v, %v", v, err)
	}
	last := a.entries[len(a.entries)-1]
	if last.Action != "quote.declined_from_link" || last.Changes["reason"] != "found it cheaper" {
		t.Errorf("audit %+v", last)
	}
}

func TestDealerLink_ALinkOpensOnlyItsOwnKindOfTarget(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	exp := w.now.Add(24 * time.Hour)
	id, _, _ := w.dealerQuote("SENT", &exp)
	res, _ := w.svc.ShareDealerQuote(ctx, desk, id, "")
	dealerToken := strings.TrimPrefix(res.Path, "/dq/")
	_, customerToken := w.createSent(w.basicInput())

	// A dealer quote token opens no customer quote route.
	if _, err := w.svc.View(ctx, dealerToken, anon); !errors.Is(err, customerquote.ErrLinkUnknown) {
		t.Errorf("a dealer quote token on the customer page: %v", err)
	}
	if _, err := w.svc.Accept(ctx, dealerToken, anon, customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner")}); !errors.Is(err, customerquote.ErrLinkUnknown) {
		t.Errorf("a dealer quote token accepting a customer quote: %v", err)
	}
	// A customer quote token opens no dealer quote route.
	if _, err := w.svc.DealerQuoteView(ctx, customerToken, anon); !errors.Is(err, customerquote.ErrLinkUnknown) {
		t.Errorf("a customer quote token on the dealer quote page: %v", err)
	}
	if _, err := w.svc.DealerQuoteDecide(ctx, customerToken, anon, true, "Pat Contractor", ""); !errors.Is(err, customerquote.ErrLinkUnknown) {
		t.Errorf("a customer quote token deciding a dealer quote: %v", err)
	}
}

func TestDealerLink_TwoAcceptancesOfOneLinkDecideOnce(t *testing.T) {
	w := newWorldPool(t, 4)
	ctx := context.Background()
	exp := w.now.Add(24 * time.Hour)
	id, f, _ := w.dealerQuote("SENT", &exp)
	res, _ := w.svc.ShareDealerQuote(ctx, desk, id, "")
	token := strings.TrimPrefix(res.Path, "/dq/")
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = w.svc.DealerQuoteDecide(ctx, token, customerquote.Caller{Address: "203.0.113.9"}, true, "Pat Contractor", "")
		}()
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 1 || f.docs[id].State != "ACCEPTED" {
		t.Fatalf("%d acceptances succeeded, state %s (%v)", ok, f.docs[id].State, errs)
	}
}
