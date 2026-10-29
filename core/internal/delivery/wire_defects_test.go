// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery_test

// Defect 1 of the PR 80 reviews, pinned on the wire end to end. PR 80
// review round 2 P2-3 found that the lumber-index pre-ship gate
// (pricing.ErrUnresolvedExposure) was returned as is from
// AssignOrderToRoute and the handler passed the error to
// httpx.WriteError, which renders a non-*httpx.Error as a 500
// internal_error. The order module's HandleExposureGate maps the same
// error to a 409 with the exposure payload as blockers; the delivery
// handler now does the same on the assign path.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// defectFixture extends the wire fixture with the pricing exposure gate
// wired in (the real PostgresExposureChecker, the same checker the
// server wires through wireExposure). It carries the same fields and
// outbox so the existing wire tests' assign helper and seed helpers
// apply.
type defectFixture struct {
	*fixture
}

// newDefectFixture builds a delivery server with the production exposure
// gate wired in, so an assign against an order whose source quote is in
// ACK_REQUIRED exposure state answers the production 409.
func newDefectFixture(t *testing.T) *defectFixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db, prefix: "DEFECT-" + uuid.NewString()[:8] + "-"}
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&f.branch); err != nil {
		t.Fatal(err)
	}

	svc := delivery.NewService(delivery.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db))
	svc.WithFulfilment(fakeQueue{f}, fakeOrders{})
	// The real pricing checker against the test database is the
	// production wiring (core/internal/app/serve/wire_exposure.go).
	svc.WithExposureGate(pricing.NewExposureChecker(db))

	mux := http.NewServeMux()
	delivery.NewHandler(svc).RegisterRoutes(mux)
	wallMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bc := &branchctx.Context{UserSub: r.Header.Get("X-Test-Sub"), IsAdmin: r.Header.Get("X-Test-Admin") == "1"}
		if b := r.Header.Get("X-Test-Branch"); b != "" {
			id := uuid.MustParse(b)
			bc.BranchID = &id
		}
		mux.ServeHTTP(w, r.WithContext(branchctx.With(r.Context(), bc)))
	})
	f.srv = httptest.NewServer(middleware.Idempotency(db)(wallMux))
	t.Cleanup(f.srv.Close)
	return &defectFixture{f}
}

// seedUnresolvedExposureOrder plants a customer, a quote in
// ACK_REQUIRED exposure state, and an order pointing at that quote.
// The pricing exposure gate rejects the order at assign time. Returns
// a fresh route id ready for the assign call (DRAFT, with
// vehicle/driver seeded).
func (f *defectFixture) seedUnresolvedExposureOrder(t *testing.T) (route, order uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var customer uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		uuid.New(), "Defect exposure "+f.prefix, f.prefix+uuid.NewString()[:8], f.branch).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, customer, f.branch); err != nil {
		t.Fatal(err)
	}
	var quote uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO quotes (id, customer_id, branch_id, state, total_amount, exposure_state, exposure_dollars)
		VALUES ($1, $2, $3, 'ACCEPTED', 10, 'ACK_REQUIRED', 1500) RETURNING id`,
		uuid.New(), customer, f.branch).Scan(&quote); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO orders (id, customer_id, branch_id, quote_id, status, total_amount, delivery_type, currency, number)
		VALUES ($1, $2, $3, $4, 'CONFIRMED', 10, 'DELIVERY', 'USD', $5) RETURNING id`,
		uuid.New(), customer, f.branch, quote, "SO-"+strings.ToUpper(uuid.NewString()[:8])).Scan(&order); err != nil {
		t.Fatal(err)
	}

	vehicle, driver := f.seedFleet(t)
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO delivery_routes (id, vehicle_id, driver_id, scheduled_date, status)
		VALUES ($1, $2, $3, CURRENT_DATE, 'DRAFT') RETURNING id`,
		uuid.New(), vehicle, driver).Scan(&route); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_id IN ($1, $2)`, route, order)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_id IN ($1, $2)`, route, order)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM deliveries WHERE route_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_routes WHERE id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM orders WHERE id = $1`, order)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM quotes WHERE id = $1`, quote)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, customer)
	})
	return route, order
}

// countStops reads the deliveries row count for one route.
func (f *fixture) countStops(t *testing.T, route uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM deliveries WHERE route_id = $1`, route).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// countEvents reads the events_outbox row count for one (entity, type).
func (f *fixture) countEvents(t *testing.T, entity uuid.UUID, typ string) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = $2`, entity, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// countEventsForEntities reads the events_outbox row count for two
// entities combined, regardless of type. Used by the exposure
// refusal test to assert the refused attempt writes no event of any
// type (PR 80 review round 1 P3-2: the previous check asserted
// delivery.assigned only and a non-named event would slip past).
func (f *fixture) countEventsForEntities(t *testing.T, a, b uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events_outbox WHERE entity_id IN ($1, $2)`, a, b).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// countAudit reads the audit_log row count for one entity.
func (f *fixture) countAudit(t *testing.T, entity uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE entity_id = $1`, entity).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// detailsByFieldAndCode collects (field, code, message) pairs from the
// envelope so a test can assert on the named blocker.
func (f *fixture) detailsByFieldAndCode(t *testing.T, r resp) []map[string]string {
	t.Helper()
	env, _ := r.body["error"].(map[string]any)
	details, _ := env["details"].([]any)
	out := []map[string]string{}
	for _, d := range details {
		if m, ok := d.(map[string]any); ok {
			entry := map[string]string{}
			if code, ok := m["code"].(string); ok {
				entry["code"] = code
			}
			if msg, ok := m["message"].(string); ok {
				entry["message"] = msg
			}
			out = append(out, entry)
		}
	}
	return out
}

// Defect 1 (PR 80 review round 2 P2-3): an order whose source quote
// has unresolved lumber-index exposure is refused on the assign path
// with a 409 and the exposure payload as blockers. The base answer was
// a 500 internal because httpx.WriteError turns any non-*httpx.Error
// into a 500. The fix mirrors the order module's HandleExposureGate
// helper.
func TestAssign_ExposureGateRefusalAnswers409(t *testing.T) {
	f := newDefectFixture(t)
	route, order := f.seedUnresolvedExposureOrder(t)

	// PR 80 review round 1 P3-2: take a baseline of every event row
	// for these entities, then assert the post-call delta is zero.
	// The previous check only watched one named type
	// (delivery.assigned) and would have missed a stray event of
	// any other type that the refused attempt wrote.
	beforeEvents := f.countEventsForEntities(t, route, order)

	res := f.assignOrder(t, route, order, nil)
	if res.status != http.StatusConflict {
		t.Fatalf("assign with unresolved exposure = %d %s, want 409", res.status, res.raw)
	}
	if got := f.errCode(t, res); got != httpx.CodeConflict {
		t.Errorf("code = %s, want %s", got, httpx.CodeConflict)
	}
	// The envelope's details carry the exposure payload, prefixed
	// with `exposure_` so a client can match on the field without
	// parsing JSON in the message (the same shape the order module
	// writes). The pricing payload's top-level keys are `error`,
	// `code`, and `exposure` (a nested map that holds the per-quote
	// reading); the test asserts that the nested exposure block
	// carries `state = ACK_REQUIRED`.
	details := f.detailsByFieldAndCode(t, res)
	if len(details) == 0 {
		t.Fatalf("no details on the 409 envelope: %s", res.raw)
	}
	var sawState bool
	var sawCode bool
	for _, d := range details {
		if d["code"] == "exposure_exposure" {
			// The message is the string form of the nested map;
			// it contains "state:ACK_REQUIRED".
			if strings.Contains(d["message"], "state:ACK_REQUIRED") {
				sawState = true
			}
		}
		if d["code"] == "exposure_code" && d["message"] == "UNRESOLVED_EXPOSURE" {
			sawCode = true
		}
	}
	if !sawState {
		t.Errorf("details = %v, want an exposure_exposure entry with state=ACK_REQUIRED", details)
	}
	if !sawCode {
		t.Errorf("details = %v, want an exposure_code=UNRESOLVED_EXPOSURE entry", details)
	}

	// The refused assign writes no stop, no event and no audit row.
	if got := f.countStops(t, route); got != 0 {
		t.Errorf("%d stops written despite a 409 refusal", got)
	}
	// The total events_outbox delta over (route, order) is zero.
	if got := f.countEventsForEntities(t, route, order) - beforeEvents; got != 0 {
		t.Errorf("events_outbox delta = %d, want 0 (a refused assign must not write any event of any type)", got)
	}
	if got := f.countAudit(t, route) + f.countAudit(t, order); got != 0 {
		t.Errorf("audit rows = %d, want 0", got)
	}
}

// CreateDelivery populates DeliveryInstructions, ScheduledStart, and
// ScheduledEnd on the in-memory Stop struct and computes local window bounds
// by wall-clock hour and minute in the yard's time zone so a DST transition day
// does not shift the scheduled window by an hour.
func TestCreateDelivery_PopulatesScheduleAndInstructionsAndWallClockDST(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	yardID := uuid.New()
	custID := uuid.New()
	orderID := uuid.New()
	winID := uuid.New()
	holdID := uuid.New()
	stopID := uuid.Nil

	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO locations (id, name, code, type, timezone) VALUES ($1, 'DST Yard', $2, 'BRANCH', 'America/New_York')`,
		yardID, "DST-"+yardID.String()[:6]); err != nil {
		t.Fatalf("insert yard: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'DST Customer', $2, $3)`,
		custID, "DST-"+custID.String()[:8], yardID); err != nil {
		t.Fatalf("insert customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency, site_delivery_instructions)
		 VALUES ($1, $2, $3, 'CONFIRMED', 100, 'DELIVERY', 'USD', 'Drop by north bay')`,
		orderID, custID, yardID); err != nil {
		t.Fatalf("insert order: %v", err)
	}

	// Find the second Sunday of March next year (the America/New_York spring-forward transition day).
	marchFirst := time.Date(time.Now().UTC().Year()+1, time.March, 1, 0, 0, 0, 0, time.UTC)
	offsetToFirstSunday := (7 - int(marchFirst.Weekday())) % 7
	springForwardSunday := marchFirst.AddDate(0, 0, offsetToFirstSunday+7)
	holdDate := springForwardSunday.Format("2006-01-02")

	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO yard_schedule_windows (id, yard_id, kind, weekday, start_time, end_time, capacity, cutoff_minutes)
		 VALUES ($1, $2, 'delivery', 0, '08:00', '12:00', 2, 0)`,
		winID, yardID); err != nil {
		t.Fatalf("insert window: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO portal_schedule_holds (id, window_id, hold_date, customer_id, order_id, status)
		 VALUES ($1, $2, $3, $4, $5, 'held')`,
		holdID, winID, holdDate, custID, orderID); err != nil {
		t.Fatalf("insert hold: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		if stopID != uuid.Nil {
			_, _ = db.Pool.Exec(c, `DELETE FROM deliveries WHERE id = $1`, stopID)
		}
		_, _ = db.Pool.Exec(c, `DELETE FROM portal_schedule_holds WHERE id = $1`, holdID)
		_, _ = db.Pool.Exec(c, `DELETE FROM yard_schedule_windows WHERE id = $1`, winID)
		_, _ = db.Pool.Exec(c, `DELETE FROM orders WHERE id = $1`, orderID)
		_, _ = db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, custID)
		_, _ = db.Pool.Exec(c, `DELETE FROM locations WHERE id = $1`, yardID)
	})

	repo := delivery.NewRepository(db)
	stop := &delivery.Stop{
		OrderID:      orderID,
		StopSequence: 1,
		Status:       delivery.StopStatusPending,
	}
	if err := repo.CreateDelivery(ctx, stop); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}
	stopID = stop.ID

	if stop.DeliveryInstructions == nil || *stop.DeliveryInstructions != "Drop by north bay" {
		t.Errorf("in-memory stop.DeliveryInstructions = %v, want Drop by north bay", stop.DeliveryInstructions)
	}
	if stop.ScheduledStart == nil || stop.ScheduledEnd == nil {
		t.Errorf("in-memory stop.ScheduledStart=%v ScheduledEnd=%v, want non-nil", stop.ScheduledStart, stop.ScheduledEnd)
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load America/New_York: %v", err)
	}
	var dbStart, dbEnd time.Time
	if err := db.Pool.QueryRow(ctx,
		`SELECT scheduled_start, scheduled_end FROM deliveries WHERE id = $1`, stopID).Scan(&dbStart, &dbEnd); err != nil {
		t.Fatalf("read back scheduled bounds: %v", err)
	}
	if gotStart := dbStart.In(loc).Format("15:04"); gotStart != "08:00" {
		t.Errorf("ScheduledStart in yard zone = %s, want 08:00 (not shifted by DST)", gotStart)
	}
	if gotEnd := dbEnd.In(loc).Format("15:04"); gotEnd != "12:00" {
		t.Errorf("ScheduledEnd in yard zone = %s, want 12:00 (not shifted by DST)", gotEnd)
	}
}
