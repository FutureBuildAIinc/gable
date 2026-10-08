// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package notification

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/eventbus"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// sendRecorder is an EmailService that records the recipients of every
// delivery notification.
type sendRecorder struct {
	mu sync.Mutex
	to []string
}

func (r *sendRecorder) SendInvoice(context.Context, string, string, []byte) error { return nil }
func (r *sendRecorder) SendEmailWithAttachment(context.Context, []string, string, string, string, []byte) error {
	return nil
}
func (r *sendRecorder) SendDeliveryNotification(_ context.Context, to, _, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.to = append(r.to, to)
	return nil
}
func (r *sendRecorder) countTo(addr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, a := range r.to {
		if a == addr {
			n++
		}
	}
	return n
}

// staticChecker reports a fixed ACK_REQUIRED status for any quote.
type staticChecker struct{ status pricing.ExposureStatus }

func (c staticChecker) CheckQuoteExposure(_ context.Context, id uuid.UUID) (pricing.ExposureStatus, error) {
	s := c.status
	s.QuoteID = id
	return s, nil
}

func (staticChecker) RequireClearForOrder(context.Context, uuid.UUID) error { return nil }
func (staticChecker) QuoteIDForOrder(context.Context, uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}

// The whole path the exposure email takes: a service call commits its ledger
// row and outbox event, a drain pass delivers the committed row to the real
// ExposureNotifier, and exactly one email goes out. Resetting the cursor to
// 0 and draining again replays the row, and the notifier's dedup on the
// outbox event_id keeps the count at one (fix round 1, P2-4).
func TestExposureEmail_EndToEndThroughTheOutboxDrain(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %s: %v", sql, err)
		}
	}

	salesID, customerID, quoteID := uuid.New(), uuid.New(), uuid.New()
	salesEmail := "e2e-" + salesID.String()[:8] + "@example.test"
	durable := "e2e-notifier-" + salesID.String()[:8]
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	exec(`INSERT INTO sales_team (id, name, email) VALUES ($1, 'E2E Rep', $2)`, salesID, salesEmail)
	exec(`INSERT INTO customers (id, name, account_number, primary_branch_id)
	      VALUES ($1, 'E2E Email Co', $2, `+branch+`)`, customerID, "E2E-"+uuid.NewString()[:8])
	exec(`INSERT INTO quotes (id, customer_id, project_id, state, total_amount, source,
	                          customer_notes, branch_id, exposure_state, created_at, updated_at)
	      VALUES ($1, $2, NULL, 'SENT', 500.00, 'manual', 'e2e fixture', `+branch+`,
	              'ACK_REQUIRED', NOW(), NOW())`, quoteID, customerID)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM event_subscriber_cursors WHERE subscriber = $1`, durable)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, quoteID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quote_exposure_events WHERE quote_id = $1`, quoteID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE id = $1`, quoteID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM sales_team WHERE id = $1`, salesID)
	})

	emails := &sendRecorder{}
	notifier := NewExposureNotifier(emails, db, nil)

	startDrain := func() *outbox.DrainRunner {
		d := outbox.NewDrainRunner(db, nil)
		d.Subscribe(eventbus.SubjectExposureAll, durable, notifier.Handle)
		if err := d.Start(ctx); err != nil {
			t.Fatalf("drain start: %v", err)
		}
		return d
	}
	// caughtUp waits until the subscriber's cursor has reached the feed head.
	caughtUp := func() {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var pos, head int64
			if err := db.Pool.QueryRow(ctx,
				`SELECT position FROM event_subscriber_cursors WHERE subscriber = $1`, durable).Scan(&pos); err != nil {
				t.Fatal(err)
			}
			if err := db.Pool.QueryRow(ctx,
				`SELECT COALESCE(max(position), 0) FROM events_outbox`).Scan(&head); err != nil {
				t.Fatal(err)
			}
			if pos >= head {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("drain did not reach the head of the outbox")
	}

	drain := startDrain()
	svc := pricing.NewExposureService(
		pricing.NewExposureRepository(db), nil, nil, nil,
		staticChecker{status: pricing.ExposureStatus{
			State: pricing.ExposureStateAckRequired, ExposureDollars: 500,
			QuoteShortID: "E2E1", SalespersonID: &salesID, Indexes: []string{"SYP"},
		}}, nil,
	).WithOutbox(outbox.NewWriter(db, "default")).WithTxRunner(db)

	if _, err := svc.RequestAck(ctx, quoteID, "user-1", "sales"); err != nil {
		t.Fatalf("RequestAck: %v", err)
	}
	caughtUp()
	drain.Stop()
	if got := emails.countTo(salesEmail); got != 1 {
		t.Fatalf("emails to the salesperson after one commit and one drain pass = %d, want 1", got)
	}

	// Replay: cursor back to 0, a fresh drain pass over the whole outbox.
	if _, err := db.Pool.Exec(ctx,
		`UPDATE event_subscriber_cursors SET position = 0 WHERE subscriber = $1`, durable); err != nil {
		t.Fatal(err)
	}
	drain = startDrain()
	caughtUp()
	drain.Stop()
	if got := emails.countTo(salesEmail); got != 1 {
		t.Fatalf("emails to the salesperson after replaying the outbox = %d, want still 1", got)
	}
}
