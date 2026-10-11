// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product_test

// A read never holds a connection while it asks for a second one: the list
// reads each product's stock in the same statement as the product. Four
// readers at pool size 4, each paging through the whole catalogue, finish.
// A list that ran a statement per row while its rows were open would leave
// four holders each waiting for a fifth connection.
//
// The catalogue is shared: parallel packages' tests create and delete their
// own products while this one runs, and the list orders by created_at DESC,
// so those rows sit at the top of the first page one moment and are gone the
// next. Each reader therefore walks the pages but reads back only the probe
// rows this test seeds and deletes itself; a probe that vanishes between the
// list and the get is a real failure, not another test's cleanup.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func TestProductListHoldsOneConnection(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const probes = 5
	marker := "PL4-" + uuid.NewString()[:8]
	probeIDs := make(map[uuid.UUID]bool, probes)
	for i := 0; i < probes; i++ {
		var id uuid.UUID
		if err := db.Pool.QueryRow(ctx,
			`INSERT INTO products (sku, description, uom_primary, base_price)
			 VALUES ($1, 'pool probe', 'PCS', 1) RETURNING id`,
			fmt.Sprintf("%s-%02d", marker, i)).Scan(&id); err != nil {
			t.Fatalf("seed probe product: %v", err)
		}
		probeIDs[id] = true
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM products WHERE sku LIKE $1`, marker+"%")
	})

	svc := product.NewService(product.NewRepository(db))

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pass := 0; pass < 5; pass++ {
				seen := make(map[uuid.UUID]bool, probes)
				var after *time.Time
				var afterID *uuid.UUID
				for {
					rows, more, err := svc.ListProductsPage(ctx, after, afterID, 200)
					if err != nil {
						errs <- err
						return
					}
					for _, p := range rows {
						if !probeIDs[p.ID] {
							continue
						}
						seen[p.ID] = true
						if _, err := svc.GetProduct(ctx, p.ID); err != nil {
							errs <- err
							return
						}
					}
					if !more || len(rows) == 0 {
						break
					}
					last := rows[len(rows)-1]
					after, afterID = &last.CreatedAt.Time, &last.ID
				}
				if len(seen) != probes {
					errs <- fmt.Errorf("pass walked the catalogue and found %d of the %d probe products", len(seen), probes)
					return
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
