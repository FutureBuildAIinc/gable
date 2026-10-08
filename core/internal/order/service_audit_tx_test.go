// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// requirePool4DB opens a pool capped at 4 connections, the concurrency-test
// size the floor rules mandate. Unlike testutil.RequireDB it reads
// DATABASE_URL directly: config.Load would fall back to a default database on
// another port, and a concurrency probe must address the container the
// command named, nothing else.
func requirePool4DB(t *testing.T) *database.DB {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		if os.Getenv(testutil.RequireDBEnv) != "" {
			t.Fatalf("%s is set but DATABASE_URL is not; refusing to fall back to any default database", testutil.RequireDBEnv)
		}
		t.Skip(testutil.SkipReason)
	}
	db, err := database.Connect(url, database.PoolConfig{
		MaxConns:          4,
		MinConns:          1,
		MaxConnLifetime:   time.Hour,
		MaxConnIdleTime:   30 * time.Minute,
		HealthCheckPeriod: time.Minute,
	})
	if err != nil {
		t.Skipf("%s (%v)", testutil.SkipReason, err)
	}
	t.Cleanup(db.Close)
	return db
}

// TestCancelOrderAuditInTxConcurrencyPool4 runs three concurrent
// cancellations on a pool of 4 connections, with the fourth held and the
// three cancellations deliberately overlapped: the held connection row-locks
// the three order rows until every contender is inside its transaction (each
// holding a connection), then lets go while keeping the connection. That is
// the steady state of a running server — cron, health checks, another
// request — and the exact shape in which a pool write inside a transaction
// starves: three transaction connections plus the held fourth leave nothing
// to acquire. CancelOrder writes its audit row through its transaction, so
// the contenders complete; a pool use inside the transaction would block on
// the exhausted pool until the bounded context fails the probe.
func TestCancelOrderAuditInTxConcurrencyPool4(t *testing.T) {
	db := requirePool4DB(t)
	ctx := context.Background()

	orderRepo := order.NewRepository(db)
	custRepo := customer.NewRepository(db)
	custSvc := customer.NewService(custRepo)
	orderSvc := order.NewService(orderRepo, nil, nil, custSvc, nil, db).WithAuditLog(audit.NewLogger(db))

	const contenders = 3
	orderIDs := make([]uuid.UUID, contenders)
	for i := 0; i < contenders; i++ {
		custID := uuid.New()
		productID := uuid.New()
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO customers (id, name, account_number, primary_branch_id)
			 VALUES ($1, $2, $3,
			         (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
			custID, "Concurrency Customer", "CC-"+custID.String()[:8]); err != nil {
			t.Fatalf("insert customer: %v", err)
		}
		if _, err := db.Pool.Exec(ctx,
			"INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'Concurrency Item', 'EA', 100)",
			productID, "CC-"+productID.String()[:8]); err != nil {
			t.Fatalf("insert product: %v", err)
		}
		o, err := orderSvc.CreateOrder(ctx, order.CreateOrderRequest{
			CustomerID: custID,
			Lines: []order.OrderLineRequest{
				{ProductID: productID, Quantity: 1, PriceEach: 5000},
			},
		})
		if err != nil {
			t.Fatalf("create order: %v", err)
		}
		orderIDs[i] = o.ID
	}

	// Hold the fourth connection for the whole probe.
	held, err := db.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire held connection: %v", err)
	}
	defer held.Release()

	// Overlap the contenders: row-lock their order rows from the held
	// connection so each cancellation begins its transaction and then blocks
	// on the lock, holding its transaction's connection.
	if _, err := held.Conn().Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	if _, err := held.Conn().Exec(ctx,
		`SELECT id FROM orders WHERE id = ANY($1) FOR UPDATE`, orderIDs); err != nil {
		t.Fatalf("row-lock orders: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Bounded so a connection-starved contender fails instead of
			// hanging the suite.
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			errs[i] = orderSvc.CancelOrder(cctx, orderIDs[i], "concurrency probe")
		}(i)
	}

	// Let every contender reach its blocked UpdateStatus inside its
	// transaction, then release the row locks — the held connection stays
	// held, so the pool is exactly three transaction connections plus one.
	time.Sleep(500 * time.Millisecond)
	if _, err := held.Conn().Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatalf("rollback lock tx: %v", err)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("contender %d cancel failed (pool exhausted by a pool use inside the transaction?): %v", i, err)
		}
	}

	// Every cancellation committed with exactly its one audit row, written
	// through the transaction.
	for _, id := range orderIDs {
		var status string
		if err := db.Pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, id).Scan(&status); err != nil {
			t.Fatalf("read order %s: %v", id, err)
		}
		if status != string(order.StatusCancelled) {
			t.Errorf("order %s status = %s, want CANCELLED", id, status)
		}
		var auditCount int
		if err := db.Pool.QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE entity_type = 'order' AND entity_id = $1 AND action = 'order.cancelled'`,
			id).Scan(&auditCount); err != nil {
			t.Fatalf("count audit rows for %s: %v", id, err)
		}
		if auditCount != 1 {
			t.Errorf("order %s has %d order.cancelled audit row(s), want exactly 1", id, auditCount)
		}
	}
}
