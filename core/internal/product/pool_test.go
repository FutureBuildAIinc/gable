// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product_test

// A read never holds a connection while it asks for a second one: the list
// reads each product's stock in the same statement as the product. Four
// readers at pool size 4, each paging through the whole catalogue, finish.
// A list that ran a statement per row while its rows were open would leave
// four holders each waiting for a fifth connection.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
)

func TestProductListHoldsOneConnection(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	svc := product.NewService(product.NewRepository(db))

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pass := 0; pass < 5; pass++ {
				rows, _, err := svc.ListProductsPage(ctx, nil, nil, 200)
				if err != nil {
					errs <- err
					return
				}
				for _, p := range rows[:min(len(rows), 3)] {
					if _, err := svc.GetProduct(ctx, p.ID); err != nil {
						errs <- err
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("reader: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("readers at pool size 4 did not finish: a read held a connection while asking for another")
	}
}
