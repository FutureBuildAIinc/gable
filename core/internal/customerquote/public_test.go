// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

func sampleQuote() *Quote {
	g := uuid.New()
	hours, _ := httpx.ParseQuantity("10")
	q := &Quote{
		ID: uuid.New(), Reference: "CQ-000001", Title: "Deck", Status: StatusSent, ValidUntil: time.Date(2031, 5, 1, 0, 0, 0, 0, time.UTC),
		EndCustomer: Contact{Name: "Pat Homeowner"}, JobSiteAddress: "12 Maple Street", Tax: Tax{Mode: TaxSiteRate, RateBPS: 800}, DepositBPS: 2500,
		Revision: 3, Markup: Markup{Kind: KindMarkup, BPS: 2500},
		Groups: []OptionGroup{{ID: g, Label: "Decking", Position: 0}},
		Lines: []Line{
			{ID: uuid.New(), Description: "Studs", Quantity: 10_000_000, UOM: "EA", CostCents: 55000, SellCents: 68750, Selection: SelectionPending},
			{ID: uuid.New(), GroupID: &g, Description: "Option A", Quantity: 10_000, UOM: "EA", CostCents: 20000, SellCents: 25000, Selection: SelectionPending},
			{ID: uuid.New(), GroupID: &g, Description: "Option B", Quantity: 10_000, UOM: "EA", CostCents: 30000, SellCents: 37500, Selection: SelectionPending},
		},
		Labour: []Labour{
			{ID: uuid.New(), Description: "Crew", RateType: RateHourly, RateCents: 5000, Hours: hours},
			{ID: uuid.New(), Description: "Own travel", RateType: RateFlat, RateCents: 7777, ContractorOnly: true},
		},
		Overhead: []Overhead{{ID: uuid.New(), Label: "Permit", AmountType: AmountFlat, Value: 3000}},
	}
	return q
}

func TestBuildPublicView_HasNoContractorOnlyLineAndHidesPricesOnRequest(t *testing.T) {
	q := sampleQuote()
	now := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	v, err := BuildPublicView(q, PublicInputs{ContractorName: "Wire Builders", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	for _, bad := range []string{"Own travel", "7777", "55000", "20000", "30000", "cost", "markup", "margin", "contractor_only"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("the public view carries %q: %s", bad, b)
		}
	}
	if len(v.Extras) != 2 || v.Status != "open" || !v.CanDecide {
		t.Errorf("extras %d status %s can_decide %v", len(v.Extras), v.Status, v.CanDecide)
	}
	if v.Lines[0].PriceCents == nil || *v.Lines[0].PriceCents != 68750 {
		t.Errorf("the line price: %v", v.Lines[0].PriceCents)
	}

	q.HideLinePrices = true
	v, _ = BuildPublicView(q, PublicInputs{ContractorName: "Wire Builders", Now: now})
	b, _ = json.Marshal(v)
	if strings.Contains(string(b), "price_cents") {
		t.Errorf("a line price shows under hide_line_prices: %s", b)
	}
	if v.Totals.TotalCents == 0 {
		t.Error("the lump sum is missing")
	}
}

func TestBuildPublicView_Statuses(t *testing.T) {
	q := sampleQuote()
	at := func(d time.Duration) time.Time { return q.ValidUntil.Add(d) }
	cases := []struct {
		status Status
		now    time.Time
		want   string
		can    bool
	}{
		{StatusSent, at(-time.Hour), "open", true},
		{StatusSent, at(0), "expired", false},
		{StatusApproved, at(time.Hour), "approved", false},
		{StatusDeclined, at(-time.Hour), "declined", false},
		{StatusConfirmed, at(-time.Hour), "confirmed", false},
		{StatusWithdrawn, at(-time.Hour), "withdrawn", false},
	}
	for _, c := range cases {
		q.Status = c.status
		v, err := BuildPublicView(q, PublicInputs{Now: c.now})
		if err != nil || v.Status != c.want || v.CanDecide != c.can {
			t.Errorf("%s at %v: status %q can_decide %v (%v), want %q %v", c.status, c.now, v.Status, v.CanDecide, err, c.want, c.can)
		}
	}
}

func TestFulfilmentOf_ShowsTheCustomerOnlyCoarseStates(t *testing.T) {
	for in, want := range map[string]string{"DRAFT": "ordered", "CONFIRMED": "ordered", "ON_HOLD": "being_prepared", "BACKORDERED": "being_prepared", "FULFILLED": "fulfilled", "CANCELLED": "cancelled"} {
		if got := FulfilmentOf(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestDecided_NeedsEveryChoiceAndSomethingApproved(t *testing.T) {
	q := sampleQuote()
	if err := decided(q); !errors.Is(err, ErrIncomplete) {
		t.Errorf("nothing decided: %v", err)
	}
	q.Lines[0].Selection = SelectionApproved
	if err := decided(q); !errors.Is(err, ErrIncomplete) {
		t.Errorf("the group undecided: %v", err)
	}
	q.Lines[1].Selection = SelectionApproved
	q.Lines[2].Selection = SelectionDeclined
	if err := decided(q); err != nil {
		t.Errorf("complete: %v", err)
	}
	// Everything declined, labour present: the customer may still sign off on the labour.
	q.Lines[0].Selection, q.Lines[1].Selection = SelectionDeclined, SelectionDeclined
	if err := decided(q); err != nil {
		t.Errorf("all material declined but labour remains: %v", err)
	}
	q.Labour = nil
	if err := decided(q); !errors.Is(err, ErrNothingChosen) {
		t.Errorf("nothing approved at all: %v", err)
	}
}

func TestBuildSnapshot_IsStableAndItsHashIsTheHashOfItsBytes(t *testing.T) {
	q := sampleQuote()
	q.Lines[0].Selection = SelectionApproved
	q.Lines[1].Selection = SelectionApproved
	q.Lines[2].Selection = SelectionDeclined
	body1, sum1, total1, err := BuildSnapshot(q, "approved")
	if err != nil {
		t.Fatal(err)
	}
	body2, sum2, _, _ := BuildSnapshot(q, "approved")
	if body1 != body2 || sum1 != sum2 {
		t.Error("the snapshot is not deterministic")
	}
	h := sha256.Sum256([]byte(body1))
	if hex.EncodeToString(h[:]) != sum1 {
		t.Error("the hash is not the SHA-256 of the bytes")
	}
	// 68750 + 25000 materials, 50000 crew, 3000 permit; tax 8% of materials 7500.
	if total1 != 68750+25000+50000+3000+7500 {
		t.Errorf("total %d", total1)
	}
	q.Lines[2].Selection = SelectionApproved
	q.Lines[1].Selection = SelectionDeclined
	_, sum3, _, _ := BuildSnapshot(q, "approved")
	if sum3 == sum1 {
		t.Error("a different choice gave the same hash")
	}
	if strings.Contains(body1, "Own travel") {
		t.Error("the snapshot carries a contractor only line")
	}
}

func TestInputParse_RefusesWhatCannotBeQuoted(t *testing.T) {
	good := func() *QuoteInput {
		return &QuoteInput{Title: "Deck", EndCustomer: Contact{Name: "Pat", Email: "pat@homeowner.example"},
			Lines: []LineInput{{ProductID: uuid.NewString(), Quantity: 10_000}}}
	}
	if _, err := good().Parse(); err != nil {
		t.Fatalf("a good input: %v", err)
	}
	for name, mut := range map[string]func(*QuoteInput){
		"no title":        func(in *QuoteInput) { in.Title = " " },
		"no customer":     func(in *QuoteInput) { in.EndCustomer.Name = "" },
		"bad email":       func(in *QuoteInput) { in.EndCustomer.Email = "pat@" },
		"bad phone":       func(in *QuoteInput) { in.EndCustomer.Phone = "abc" },
		"zero quantity":   func(in *QuoteInput) { in.Lines[0].Quantity = 0 },
		"bad product":     func(in *QuoteInput) { in.Lines[0].ProductID = "nope" },
		"margin 100":      func(in *QuoteInput) { in.Markup = &Markup{Kind: KindMargin, BPS: 10000} },
		"unknown tax":     func(in *QuoteInput) { in.Tax = &Tax{Mode: "vat"} },
		"rate wrong mode": func(in *QuoteInput) { in.Tax = &Tax{Mode: TaxIncluded, RateBPS: 5} },
		"deposit 101":     func(in *QuoteInput) { d := int64(10001); in.DepositBPS = &d },
		"hourly no hours": func(in *QuoteInput) {
			in.Labour = []LabourInput{{Description: "x", RateType: RateHourly, RateCents: 100}}
		},
		"flat with hours": func(in *QuoteInput) {
			h, _ := httpx.ParseQuantity("2")
			in.Labour = []LabourInput{{Description: "x", RateType: RateFlat, RateCents: 100, Hours: h}}
		},
		"overhead type": func(in *QuoteInput) { in.Overhead = []OverheadInput{{Label: "x", AmountType: "weekly", Value: 1}} },
		"group key":     func(in *QuoteInput) { k := "nope"; in.Lines[0].GroupKey = &k },
		"source line without a source": func(in *QuoteInput) {
			s := uuid.NewString()
			in.Lines[0].SourceLineID = &s
		},
		"too many lines": func(in *QuoteInput) {
			for i := 0; i < 201; i++ {
				in.Lines = append(in.Lines, LineInput{ProductID: uuid.NewString(), Quantity: 10_000})
			}
		},
	} {
		in := good()
		mut(in)
		if _, err := in.Parse(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
