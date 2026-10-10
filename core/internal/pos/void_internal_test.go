// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// tendersInIDOrder, white box (second review P3-4): every tender that became
// a payment comes back in payment id order, and a tender with no payment (an
// ACCOUNT tender) is dropped rather than aborting the sort.

import (
	"testing"

	"github.com/google/uuid"
)

func TestTendersInIDOrderSortsPaymentBearers(t *testing.T) {
	low := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	high := uuid.MustParse("00000000-0000-0000-0000-00000000000f")
	mid := uuid.MustParse("00000000-0000-0000-0000-000000000008")
	tenders := []Tender{
		{ID: uuid.New(), Method: TenderAccount}, // no payment: dropped
		{ID: uuid.New(), Method: TenderCard, PaymentID: &high},
		{ID: uuid.New(), Method: TenderAccount}, // no payment: dropped
		{ID: uuid.New(), Method: TenderCheck, PaymentID: &low},
		{ID: uuid.New(), Method: TenderCash, PaymentID: &mid},
	}
	got := tendersInIDOrder(tenders)
	if len(got) != 3 {
		t.Fatalf("%d tenders out, want the 3 that carry payments", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].PaymentID.String() >= got[i].PaymentID.String() {
			t.Errorf("order = %s then %s, want ascending payment ids",
				got[i-1].PaymentID, got[i].PaymentID)
		}
	}
}
