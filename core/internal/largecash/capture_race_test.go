// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash_test

import (
	"context"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// TestCaptureSerializesOnThePayer: three transactions capture a cash tender
// each for one payer at once, on a pool of 4, through the real service. Each
// is under the threshold alone and the three are over it together, so only
// the payer's advisory lock lets the last one see the first two: without it
// every contender reads an empty window and no crossing is ever written.
// A second round of three after the crossing runs the same race on the reset
// running total: it crosses exactly once more on its own three tenders, so no
// tender of the first round is dropped from or counted again in the second.
// The route level race (pos) is serialized upstream by the customer row, so
// this is the test that holds the lock itself.
func TestCaptureSerializesOnThePayer(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	svc := keyedService(t, db)
	branch, sales := usBranch(t, db, 6)
	payer := &largecash.PayerIn{Name: "Race Payer", Address: "1 Test Road", Occupation: "builder",
		DateOfBirth: "1980-01-02", IDType: "passport", IDNumber: "RACE-ID-0001", TaxpayerID: "99-0000001"}
	const each = largecash.ThresholdCents * 45 / 100
	race := func(round []uuid.UUID) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make([]error, len(round))
		for i, id := range round {
			wg.Add(1)
			go func(i int, id uuid.UUID) {
				defer wg.Done()
				errs[i] = db.RunInTx(ctx, func(ctx context.Context) error {
					return svc.Capture(ctx, &largecash.CompletionFacts{BranchID: branch, TransactionID: id, Currency: "USD", Payer: payer,
						Tenders: []largecash.TenderFacts{{Method: "cash", AmountCents: each}}})
				})
			}(i, id)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("contender %d: %v", i, err)
			}
		}
	}
	race(sales[:3])
	if got := crossings(t, db, branch); len(got) != 1 || got[0] != 3*each {
		t.Fatalf("crossings = %v, want exactly one with the three tenders' %d", got, 3*each)
	}
	race(sales[3:])
	if got := crossings(t, db, branch); len(got) != 2 || got[1] != 3*each {
		t.Fatalf("crossings after the second round = %v, want a second one with its own three tenders' %d", got, 3*each)
	}
}
