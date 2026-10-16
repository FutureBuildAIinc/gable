// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// A purchase order opened from a recommendation row names its yard; a user
// who holds two yards and sends no header gets the order at the named one
// (never the other yard or the default).
func TestCreateManualPOAt_LandsAtTheNamedYard(t *testing.T) {
	s := seedCreateBranch(t)
	svc := newCreateBranchService(s)
	sub := "po-yard-" + uuid.NewString()
	s.grant(t, sub, s.branchA, s.branchB)
	ctx := branchctx.With(context.Background(), &branchctx.Context{UserSub: sub})
	for _, want := range []uuid.UUID{s.branchB, s.branchA} {
		clearReorderDrafts(t, s)
		w := want
		po, err := svc.CreateManualPOAt(ctx, s.vendor, []purchase_order.CreatePOLineInput{
			{ProductID: s.product.String(), Description: "from a recommendation", Quantity: 4, Cost: 1},
		}, purchase_order.SourceManual, &w)
		if err != nil {
			t.Fatalf("create at %s: %v", want, err)
		}
		if po.BranchID != want {
			t.Fatalf("order at %s, want the named yard %s", po.BranchID, want)
		}
		got, ok := s.poBranch(t)
		if !ok || got != want {
			t.Fatalf("stored at %s (found %t), want %s", got, ok, want)
		}
	}
}
