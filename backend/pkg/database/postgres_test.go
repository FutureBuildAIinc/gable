// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package database_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
)

func TestInTx(t *testing.T) {
	db := testutil.RequireDB(t)

	if database.InTx(context.Background()) {
		t.Fatal("InTx(Background) = true, want false outside a transaction")
	}

	if err := db.RunInTx(context.Background(), func(txCtx context.Context) error {
		if !database.InTx(txCtx) {
			t.Fatal("InTx inside RunInTx = false, want true")
		}
		return nil
	}); err != nil {
		t.Fatalf("RunInTx: %v", err)
	}
}
