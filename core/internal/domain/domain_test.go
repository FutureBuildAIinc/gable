// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package domain

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Package domain holds the DTOs shared across the module boundary: the journal
// entries handed to an external GL adapter, and the purchase-order data handed
// to the X12 generator. It contains no behaviour, so there is nothing here to
// exercise — but it does carry two contracts that are invisible at the call
// site and expensive to get wrong:
//
//  1. UNITS. JournalEntryLine.Debit/Credit are int64 CENTS; POlineData.Cost is
//     float64 DOLLARS. Both are documented only in a comment. Flipping either
//     type compiles cleanly at every call site (Go converts silently through
//     an explicit cast, and a literal 0 fits both) while changing every posted
//     amount by a factor of 100.
//  2. QUANTITIES. Physical quantities are float64 because lumber is sold in
//     fractional units (board feet, linear feet); turning one into an int
//     would silently truncate a 2.5 MBF line to 2.
//
// These are CORRECTNESS tests against the type contract, asserted through
// reflection so they fail on the declaration rather than on some distant
// caller.

func fieldKind(t *testing.T, v any, field string) reflect.Kind {
	t.Helper()
	rt := reflect.TypeOf(v)
	f, ok := rt.FieldByName(field)
	if !ok {
		t.Fatalf("%s has no field %q", rt.Name(), field)
	}
	return f.Type.Kind()
}

// CORRECTNESS: a GL posting is in integer cents. A float64 debit would let a
// rounding error accumulate across a trial balance that must sum to exactly
// zero.
func TestJournalEntryLine_MoneyIsInt64Cents(t *testing.T) {
	for _, field := range []string{"Debit", "Credit"} {
		if got := fieldKind(t, JournalEntryLine{}, field); got != reflect.Int64 {
			t.Errorf("JournalEntryLine.%s is %s, want int64 (cents) — see the comment on the field", field, got)
		}
	}

	// A balanced entry sums to zero across its lines in the same unit. This is
	// the invariant the type exists to make expressible.
	entry := JournalEntry{
		ID:          uuid.New(),
		ReferenceID: "INV-1001",
		Date:        time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC),
		Lines: []JournalEntryLine{
			{AccountID: "1020", AccountName: "Accounts Receivable", Debit: 10825},
			{AccountID: "4010", AccountName: "Sales Revenue", Credit: 10000},
			{AccountID: "2100", AccountName: "Sales Tax Payable", Credit: 825},
		},
		Status: "Pending",
	}

	var debits, credits int64
	for _, l := range entry.Lines {
		debits += l.Debit
		credits += l.Credit
	}
	if debits != credits {
		t.Fatalf("the fixture is unbalanced: %d debits vs %d credits", debits, credits)
	}
	if debits != 10825 {
		t.Errorf("debits = %d cents, want 10825 ($108.25)", debits)
	}
}

// CORRECTNESS: PO data crossing into the X12 generator carries fractional
// quantities and dollar costs. An integer quantity would truncate every
// fractional-unit line on an outbound 850.
func TestPOData_QuantityAndCostAreFloat64(t *testing.T) {
	for _, field := range []string{"Quantity", "Cost"} {
		if got := fieldKind(t, POlineData{}, field); got != reflect.Float64 {
			t.Errorf("POlineData.%s is %s, want float64", field, got)
		}
	}

	line := POlineData{LineNumber: 1, Quantity: 2.5, Cost: 685.25, ItemCode: "2X4-8"}
	if line.Quantity != 2.5 {
		t.Errorf("Quantity = %v, want a fractional quantity to survive", line.Quantity)
	}
	if line.Cost != 685.25 {
		t.Errorf("Cost = %v, want the exact dollar cost", line.Cost)
	}
}

// CORRECTNESS: SentAt distinguishes "queued" from "sent at time T". A
// non-pointer time.Time would make an unsent document look as though it was
// sent at the zero time, which is what a downstream retry sweep would filter
// on.
func TestX12Document_SentAtIsOptional(t *testing.T) {
	rt := reflect.TypeOf(X12Document{})
	f, ok := rt.FieldByName("SentAt")
	if !ok {
		t.Fatal("X12Document has no SentAt field")
	}
	if f.Type.Kind() != reflect.Ptr {
		t.Fatalf("X12Document.SentAt is %s, want a *time.Time so 'never sent' is representable", f.Type)
	}

	queued := X12Document{ID: uuid.New(), Type: "850", Status: "Queued"}
	if queued.SentAt != nil {
		t.Error("a queued document must have a nil SentAt")
	}
	now := time.Now()
	sent := X12Document{ID: uuid.New(), Type: "850", Status: "Sent", SentAt: &now}
	if sent.SentAt == nil {
		t.Error("a sent document must carry a send time")
	}
}

// CORRECTNESS: ProductionMode is the switch between a test and a live trading
// partner. Its zero value must be the safe one — a profile loaded from an
// incomplete row must default to NON-production, not to sending live purchase
// orders.
func TestEDIProfile_ZeroValueIsNotProduction(t *testing.T) {
	var p EDIProfile
	if p.ProductionMode {
		t.Fatal("the zero-value EDI profile is in production mode; the safe default must be false")
	}
	if p.TransportMethod != "" || p.DestinationURL != "" {
		t.Error("the zero-value profile must not carry a transport or destination")
	}
}
