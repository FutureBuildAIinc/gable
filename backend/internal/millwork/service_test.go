// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package millwork_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/millwork"
	"github.com/gablelbm/gable/internal/testutil"
)

func TestMillworkService_Integration(t *testing.T) {
	// Skip if short mode (unit tests only)
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Skips (rather than fails) when Postgres is unreachable; runs unchanged
	// when DATABASE_URL points at a live database.
	db := testutil.RequireDB(t)

	// Setup Service
	repo := millwork.NewRepository(db)
	svc := millwork.NewService(repo)

	// Test Case 1: Create Option
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	category := "test_category"
	req := millwork.CreateOptionRequest{
		Category:        category,
		Name:            "Test Option 1",
		PriceAdjustment: 10.50,
		Attributes:      json.RawMessage(`{"width": 30}`),
	}

	opt, err := svc.CreateOption(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create option: %v", err)
	}

	if opt.ID.String() == "" {
		t.Error(" expected ID to be generated")
	}
	if opt.Name != req.Name {
		t.Errorf("expected name %s, got %s", req.Name, opt.Name)
	}

	// Test Case 2: Get Options
	options, err := svc.GetOptionsByCategory(ctx, category)
	if err != nil {
		t.Fatalf("Failed to get options: %v", err)
	}

	found := false
	for _, o := range options {
		if o.ID == opt.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created option not found in category list")
	}

	// Cleanup (Optional)
	_, _ = db.Pool.Exec(ctx, "DELETE FROM millwork_options WHERE id = $1", opt.ID)
}
