// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// Service tests over a fake repository: the order of the repository calls,
// the event written last, and refusals that write nothing. The store itself
// is proved against Postgres in wire_test.go and tx_test.go.

type callLog struct{ calls []string }

func (l *callLog) add(s string) { l.calls = append(l.calls, s) }

func (l *callLog) index(s string) int {
	for i, c := range l.calls {
		if c == s {
			return i
		}
	}
	return -1
}

func (l *callLog) has(s string) bool { return l.index(s) >= 0 }

type fakeRepo struct {
	Repository // anything the test does not script is a loud nil call
	log        *callLog

	cust      *Customer
	shipTos   map[uuid.UUID]*ShipTo
	shipCount int64
	terms     map[uuid.UUID]*PaymentTerms
	open      []string
	enabled   []string
	insertErr error
}

func (f *fakeRepo) InsertCustomer(_ context.Context, c *Customer) error {
	f.log.add("InsertCustomer")
	if f.insertErr != nil {
		return f.insertErr
	}
	cp := *c
	cp.Revision = 1
	cp.EffectiveCurrency = "USD"
	f.cust = &cp
	return nil
}
func (f *fakeRepo) GetCustomer(_ context.Context, id uuid.UUID) (*Customer, error) {
	f.log.add("GetCustomer")
	if f.cust == nil || f.cust.ID != id {
		return nil, ErrNotFound
	}
	cp := *f.cust
	return &cp, nil
}
func (f *fakeRepo) LockCustomer(_ context.Context, id uuid.UUID) error {
	f.log.add("LockCustomer")
	if f.cust == nil || f.cust.ID != id {
		return ErrNotFound
	}
	return nil
}
func (f *fakeRepo) UpdateCustomer(_ context.Context, c *Customer) error {
	f.log.add("UpdateCustomer")
	cp := *c
	cp.Revision = f.cust.Revision + 1
	f.cust = &cp
	return nil
}
func (f *fakeRepo) EnabledCurrencies(context.Context) ([]string, error) {
	f.log.add("EnabledCurrencies")
	if f.enabled == nil {
		return []string{"USD"}, nil
	}
	return f.enabled, nil
}
func (f *fakeRepo) OpenDocuments(context.Context, uuid.UUID) ([]string, error) {
	f.log.add("OpenDocuments")
	return f.open, nil
}
func (f *fakeRepo) GetTerms(_ context.Context, id uuid.UUID) (*PaymentTerms, error) {
	f.log.add("GetTerms")
	t, ok := f.terms[id]
	if !ok {
		return nil, ErrTermsNotFound
	}
	return t, nil
}
func (f *fakeRepo) DefaultTermsID(context.Context) (uuid.UUID, error) {
	f.log.add("DefaultTermsID")
	return uuid.MustParse("11111111-1111-1111-1111-111111111111"), nil
}
func (f *fakeRepo) CountShipTos(context.Context, uuid.UUID) (int64, error) {
	f.log.add("CountShipTos")
	return f.shipCount, nil
}
func (f *fakeRepo) ClearDefaultShipTo(context.Context, uuid.UUID, uuid.UUID) error {
	f.log.add("ClearDefaultShipTo")
	return nil
}
func (f *fakeRepo) InsertShipTo(_ context.Context, s *ShipTo) error {
	f.log.add("InsertShipTo")
	if f.shipTos == nil {
		f.shipTos = map[uuid.UUID]*ShipTo{}
	}
	cp := *s
	cp.Revision = 1
	f.shipTos[s.ID] = &cp
	return nil
}
func (f *fakeRepo) GetShipTo(_ context.Context, id uuid.UUID) (*ShipTo, error) {
	f.log.add("GetShipTo")
	s, ok := f.shipTos[id]
	if !ok {
		return nil, ErrShipToNotFound
	}
	cp := *s
	return &cp, nil
}

type fakeEvents struct {
	log    *callLog
	events []outbox.Event
	err    error
}

func (e *fakeEvents) Write(_ context.Context, ev outbox.Event) error {
	e.log.add("event")
	if e.err != nil {
		return e.err
	}
	e.events = append(e.events, ev)
	return nil
}

func newTestService(cust *Customer) (*Service, *fakeRepo, *fakeEvents, *callLog) {
	log := &callLog{}
	repo := &fakeRepo{log: log, cust: cust}
	ev := &fakeEvents{log: log}
	svc := NewService(repo).WithOutbox(ev)
	svc.now = func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }
	return svc, repo, ev, log
}

func existing() *Customer {
	return &Customer{
		ID: uuid.New(), AccountNumber: "A-1", Name: "Existing", Tier: TierRetail, IsActive: true,
		PaymentTermsID:    uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		EffectiveCurrency: "USD", Revision: 3, PrimaryBranchID: uuid.New(),
	}
}

func draftFor(c *Customer) *Draft {
	terms := c.PaymentTermsID
	return &Draft{AccountNumber: c.AccountNumber, Name: c.Name, Tier: c.Tier, IsActive: c.IsActive, PaymentTermsID: &terms,
		Email: c.Email, Phone: c.Phone, Address: c.Address, Currency: c.Currency, POrequired: c.POrequired,
		CreditLimitCents: c.CreditLimitCents}
}

func rev(n int64) Precondition { return Precondition{Revision: &n} }

func httpErr(t *testing.T, err error) *httpx.Error {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) {
		t.Fatalf("error %v is not an httpx.Error", err)
	}
	return he
}

// The event is the last statement of an update; the row is locked before it
// is read, and read before it is written.
func TestUpdate_LocksReadsWritesThenRecords(t *testing.T) {
	c := existing()
	svc, _, ev, log := newTestService(c)
	d := draftFor(c)
	d.Name = "Renamed"
	if _, err := svc.Update(context.Background(), c.ID, d, rev(3)); err != nil {
		t.Fatal(err)
	}
	if log.index("LockCustomer") != 0 || log.index("GetCustomer") != 1 || log.index("UpdateCustomer") <= log.index("GetCustomer") {
		t.Errorf("calls = %v, want lock, read, then write", log.calls)
	}
	if log.calls[len(log.calls)-1] != "event" {
		t.Errorf("calls = %v, the event must be the last statement", log.calls)
	}
	if len(ev.events) != 1 || ev.events[0].Type != EventUpdated || ev.events[0].EntityType != "customer" || ev.events[0].EntityID != c.ID {
		t.Fatalf("events = %+v", ev.events)
	}
}

// Refusals write nothing: no write, no event.
func TestUpdate_RefusalsWriteNothing(t *testing.T) {
	cases := []struct {
		name   string
		pre    Precondition
		mutate func(c *Customer, d *Draft, r *fakeRepo)
		status int
		code   string
	}{
		{"no revision", Precondition{}, nil, 428, httpx.CodePreconditionRequired},
		{"a stale revision", rev(2), nil, 409, httpx.CodeStaleRevision},
		{"a currency change with open documents", rev(3), func(c *Customer, d *Draft, r *fakeRepo) {
			usd := "USD"
			d.Currency = &usd
			r.open = []string{"orders"}
		}, 409, httpx.CodeConflict},
		{"a currency that is not enabled", rev(3), func(c *Customer, d *Draft, r *fakeRepo) {
			eur := "EUR"
			d.Currency = &eur
		}, 400, httpx.CodeValidationFailed},
		{"a currency with three decimal places", rev(3), func(c *Customer, d *Draft, r *fakeRepo) {
			kwd := "KWD"
			d.Currency = &kwd
			r.enabled = []string{"KWD"}
		}, 400, httpx.CodeValidationFailed},
		{"terms that do not exist", rev(3), func(c *Customer, d *Draft, r *fakeRepo) {
			id := uuid.New()
			d.PaymentTermsID = &id
		}, 400, httpx.CodeValidationFailed},
		{"terms that are inactive", rev(3), func(c *Customer, d *Draft, r *fakeRepo) {
			id := uuid.New()
			d.PaymentTermsID = &id
			r.terms = map[uuid.UUID]*PaymentTerms{id: {ID: id, IsActive: false}}
		}, 400, httpx.CodeValidationFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := existing()
			svc, repo, ev, log := newTestService(c)
			d := draftFor(c)
			if tc.mutate != nil {
				tc.mutate(c, d, repo)
			}
			_, err := svc.Update(context.Background(), c.ID, d, tc.pre)
			if he := httpErr(t, err); he.Status != tc.status || he.Code != tc.code {
				t.Errorf("error = %d %s, want %d %s", he.Status, he.Code, tc.status, tc.code)
			}
			if log.has("UpdateCustomer") || log.has("event") || len(ev.events) != 0 {
				t.Errorf("a refused update wrote: calls = %v", log.calls)
			}
		})
	}
}

// An unchanged currency never asks for the open documents, and a changed one
// is checked for them before the write.
func TestUpdate_CurrencyChecksOnlyWhenItChanges(t *testing.T) {
	c := existing()
	svc, _, _, log := newTestService(c)
	if _, err := svc.Update(context.Background(), c.ID, draftFor(c), rev(3)); err != nil {
		t.Fatal(err)
	}
	if log.has("OpenDocuments") || log.has("EnabledCurrencies") {
		t.Errorf("calls = %v, an unchanged currency must not be checked", log.calls)
	}

	c = existing()
	svc, _, _, log = newTestService(c)
	usd := "USD"
	d := draftFor(c)
	d.Currency = &usd
	if _, err := svc.Update(context.Background(), c.ID, d, rev(3)); err != nil {
		t.Fatal(err)
	}
	if log.index("OpenDocuments") < 0 || log.index("OpenDocuments") > log.index("UpdateCustomer") {
		t.Errorf("calls = %v, the open documents must be checked before the write", log.calls)
	}
}

// customer.updated names the part and what changed; a terms only change is
// part terms.
func TestUpdate_EventNamesThePartAndTheChange(t *testing.T) {
	c := existing()
	svc, repo, ev, _ := newTestService(c)
	other := uuid.New()
	repo.terms = map[uuid.UUID]*PaymentTerms{other: {ID: other, IsActive: true}}
	d := draftFor(c)
	d.PaymentTermsID = &other
	if _, err := svc.Update(context.Background(), c.ID, d, rev(3)); err != nil {
		t.Fatal(err)
	}
	data := string(ev.events[0].Data)
	if !strings.Contains(data, `"part":"terms"`) || !strings.Contains(data, `"changed":["payment_terms_id"]`) {
		t.Errorf("event data = %s, want part terms naming payment_terms_id", data)
	}

	c = existing()
	svc, _, ev, _ = newTestService(c)
	limit := httpx.Cents(5000)
	d = draftFor(c)
	d.CreditLimitCents, d.POrequired = &limit, true
	if _, err := svc.Update(context.Background(), c.ID, d, rev(3)); err != nil {
		t.Fatal(err)
	}
	data = string(ev.events[0].Data)
	if !strings.Contains(data, `"part":"header"`) || !strings.Contains(data, `"credit_limit_cents":5000`) || !strings.Contains(data, `"changed":["credit_limit_cents","po_required"]`) {
		t.Errorf("event data = %s", data)
	}
}

// A create that fails to store writes no event; one that stores writes it last.
func TestCreate_EventIsLastAndAFailedInsertWritesNone(t *testing.T) {
	svc, repo, ev, log := newTestService(nil)
	d := &Draft{AccountNumber: "N-1", Name: "New", Tier: TierRetail, IsActive: true}
	out, err := svc.Create(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if log.calls[len(log.calls)-1] != "event" || len(ev.events) != 1 || ev.events[0].Type != EventCreated || ev.events[0].EntityID != out.ID {
		t.Errorf("calls = %v events = %+v", log.calls, ev.events)
	}

	svc, repo, ev, log = newTestService(nil)
	repo.insertErr = errors.New("boom")
	if _, err := svc.Create(context.Background(), d); err == nil {
		t.Fatal("Create succeeded though the insert failed")
	}
	if log.has("event") || len(ev.events) != 0 {
		t.Errorf("a failed create wrote an event: %v", log.calls)
	}
}

// A failed event write fails the act (the transaction rolls it back).
func TestUpdate_FailedEventFailsTheAct(t *testing.T) {
	c := existing()
	svc, _, ev, _ := newTestService(c)
	ev.err = errors.New("outbox down")
	if _, err := svc.Update(context.Background(), c.ID, draftFor(c), rev(3)); err == nil {
		t.Fatal("Update succeeded though its event could not be written")
	}
}

// The first active ship-to is the default; a later one is not unless named,
// and naming a default clears the old one before the insert.
func TestCreateShipTo_DefaultRules(t *testing.T) {
	c := existing()
	d := func() *ShipToDraft {
		return &ShipToDraft{Code: "A", Name: "a", Line1: "l", IsActive: true}
	}

	svc, _, _, _ := newTestService(c)
	first, err := svc.CreateShipTo(context.Background(), c.ID, d())
	if err != nil || !first.IsDefault {
		t.Fatalf("first ship-to = %+v (%v), want the default", first, err)
	}

	svc, repo, _, _ := newTestService(c)
	repo.shipCount = 1
	later, err := svc.CreateShipTo(context.Background(), c.ID, d())
	if err != nil || later.IsDefault {
		t.Fatalf("later ship-to = %+v (%v), want not the default", later, err)
	}

	svc, repo, _, log := newTestService(c)
	repo.shipCount = 1
	named := d()
	named.IsDefault, named.DefaultGiven = true, true
	got, err := svc.CreateShipTo(context.Background(), c.ID, named)
	if err != nil || !got.IsDefault {
		t.Fatalf("named default = %+v (%v)", got, err)
	}
	if log.index("ClearDefaultShipTo") < 0 || log.index("ClearDefaultShipTo") > log.index("InsertShipTo") {
		t.Errorf("calls = %v, the old default must be cleared before the insert", log.calls)
	}

	// An inactive first ship-to is not made the default.
	svc, _, _, _ = newTestService(c)
	inactive := d()
	inactive.IsActive = false
	if got, err := svc.CreateShipTo(context.Background(), c.ID, inactive); err != nil || got.IsDefault {
		t.Errorf("inactive first ship-to = %+v (%v), want not the default", got, err)
	}
}

// Salesperson and policy writes need the revision too.
func TestWritesWithoutARevisionAre428(t *testing.T) {
	c := existing()
	svc, _, _, log := newTestService(c)
	if _, err := svc.SetSalesperson(context.Background(), c.ID, &SalespersonDraft{}, Precondition{}); httpErr(t, err).Status != 428 {
		t.Error("salesperson without a revision must be 428")
	}
	if _, err := svc.SetEscalationPolicy(context.Background(), c.ID, &PolicyDraft{}, Precondition{}); httpErr(t, err).Status != 428 {
		t.Error("policy without a revision must be 428")
	}
	if err := svc.DeleteContact(context.Background(), c.ID, Precondition{}); httpErr(t, err).Status != 428 {
		t.Error("contact delete without a revision must be 428")
	}
	if len(log.calls) != 0 {
		t.Errorf("a refused write reached the repository: %v", log.calls)
	}
}
