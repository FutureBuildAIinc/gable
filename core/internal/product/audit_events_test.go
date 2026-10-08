// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product_test

// Every product write carries its audit row and its outbox event in the
// same transaction (review r2, N-4): a failing event write, or a failing
// audit write, rolls the write back; a good write records exactly one of
// each, the event last.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type failingEvents struct{}

func (failingEvents) Write(context.Context, outbox.Event) error {
	return errors.New("outbox insert failed")
}

type failingAudit struct{}

func (failingAudit) Log(context.Context, audit.Entry) error {
	return errors.New("audit insert failed")
}

func productDraft() *product.Product {
	return &product.Product{
		SKU:         "AUD-" + uuid.NewString()[:10],
		Description: "audit probe",
		UOMPrimary:  product.UOM("PCS"),
	}
}

// TestProductWrites_FailedEventWriteRollsThemBack: with the outbox failing,
// the create and the margins update refuse and leave nothing behind.
func TestProductWrites_FailedEventWriteRollsThemBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()

	bad := product.NewService(product.NewRepository(db)).
		WithOutbox(failingEvents{}).WithTxRunner(db).WithAudit(kitAuditLogger(t, db))
	draft := productDraft()
	if err := bad.CreateProduct(ctx, draft); err == nil {
		t.Fatal("the create succeeded though its event could not be written")
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM products WHERE sku = $1`, draft.SKU).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d products survived a rolled back create (%v)", n, err)
	}

	good := product.NewService(product.NewRepository(db)).
		WithOutbox(kitOutboxWriter(t, db)).WithTxRunner(db).WithAudit(kitAuditLogger(t, db))
	created := productDraft()
	if err := good.CreateProduct(ctx, created); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, created.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, created.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, created.ID)
	}()
	if _, err := bad.UpdateMarginRules(ctx, created.ID, 30, 5, created.Revision); err == nil {
		t.Fatal("the margins update succeeded though its event could not be written")
	}
	if err := db.Pool.QueryRow(ctx, `SELECT revision FROM products WHERE id = $1`, created.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("revision = %d (%v) after a rolled back margins update, want 1", n, err)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'product' AND entity_id = $1`, created.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d audit rows survived the rolled back update, want the 1 of the create (%v)", n, err)
	}
}

// TestProductWrites_FailedAuditWriteRollsTheWriteBack: the audit row is in
// the same transaction; its failure refuses the write too.
func TestProductWrites_FailedAuditWriteRollsTheWriteBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()

	bad := product.NewService(product.NewRepository(db)).
		WithOutbox(kitOutboxWriter(t, db)).WithTxRunner(db).WithAudit(failingAudit{})
	draft := productDraft()
	if err := bad.CreateProduct(ctx, draft); err == nil {
		t.Fatal("the create succeeded though its audit row could not be written")
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM products WHERE sku = $1`, draft.SKU).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d products survived a rolled back create (%v)", n, err)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM events_outbox WHERE entity_type = 'product' AND entity_id = $1`, draft.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d events survived a rolled back create, want 0 (%v)", n, err)
	}
}

// TestProductWrites_RecordAuditAndEvent: a good create and a good margins
// update record one audit row and one event each, the event carrying the
// part the write moved.
func TestProductWrites_RecordAuditAndEvent(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := product.NewService(product.NewRepository(db)).
		WithOutbox(kitOutboxWriter(t, db)).WithTxRunner(db).WithAudit(kitAuditLogger(t, db))

	created := productDraft()
	if err := svc.CreateProduct(ctx, created); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, created.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, created.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, created.ID)
	}()
	if _, err := svc.UpdateMarginRules(ctx, created.ID, 30, 5, created.Revision); err != nil {
		t.Fatal(err)
	}

	assertValues := func(query string, want map[string]bool, what string) {
		t.Helper()
		rows, err := db.Pool.Query(ctx, query, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]bool{}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			got[v] = true
		}
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
		for k := range want {
			if !got[k] {
				t.Fatalf("%s: got %v, want %v (missing %q)", what, got, want, k)
			}
		}
	}
	assertValues(`SELECT action FROM audit_log WHERE entity_type = 'product' AND entity_id = $1`,
		map[string]bool{"product.created": true, "product.updated": true}, "audit actions")
	assertValues(`SELECT type FROM events_outbox WHERE entity_type = 'product' AND entity_id = $1`,
		map[string]bool{"product.created": true, "product.updated": true}, "event types")

	var data string
	if err := db.Pool.QueryRow(ctx,
		`SELECT data::text FROM events_outbox WHERE entity_type = 'product' AND entity_id = $1 AND type = 'product.updated'`,
		created.ID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, `"margins"`) {
		t.Errorf("the update event %s does not name the margins part", data)
	}
}
