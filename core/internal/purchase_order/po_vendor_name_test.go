// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// RULE: the by id read names the vendor, so the printed purchase order's
// Vendor line is never blank (the print route passes this record to the form).
func TestGetPO_NamesItsVendor(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := branchctx.WithSystem(context.Background())

	vendor := uuid.New()
	name := "po-vendor-name-" + vendor.String()[:6]
	if _, err := db.Pool.Exec(ctx, `INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, name); err != nil {
		t.Fatal(err)
	}
	var poID uuid.UUID
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, poID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
	})

	svc := purchase_order.NewService(purchase_order.NewRepository(db), db, nil, nil, nil, nil)
	po, err := svc.CreateManualPOFromHandler(ctx, vendor, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	poID = po.ID
	got, err := svc.GetPO(ctx, po.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.VendorName != name {
		t.Errorf("vendor name = %q, want %q", got.VendorName, name)
	}
}
