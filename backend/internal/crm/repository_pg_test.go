// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// The persistence rules the testability gap named live inside the Postgres
// implementation and need a real database:
//
//   - Create defaults a missing activity_date to now (activity.go:74-76), which
//     is what stops an un-dated call log sorting to the epoch.
//   - Update and Delete map zero rows affected onto "activity not found".
//   - ListByCustomer is scoped by customer_id and ordered activity_date DESC.
//
// Skips cleanly when Postgres is unreachable (testutil.RequireDB).

// seedCustomer creates a customer to hang activities off, since crm_activities
// has a NOT NULL FK to customers.
func seedCustomer(t *testing.T, repo *PostgresRepository) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()

	_, err := repo.db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ($1, $2, $3, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		id, "CRM PGTest "+id.String()[:8], "CRM-"+id.String()[:8])
	if err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.db.Pool.Exec(context.Background(), `DELETE FROM customers WHERE id = $1`, id)
	})
	return id
}

// CORRECTNESS: an activity submitted without an activity_date is dated now, not
// left at the zero time. The feed is ordered activity_date DESC, so a zero
// timestamp would bury a freshly logged call at the bottom of the customer's
// history forever.
func TestPostgresRepository_CreateDefaultsActivityDateToNow(t *testing.T) {
	repo := NewRepository(testutil.RequireDB(t))
	ctx := context.Background()
	customerID := seedCustomer(t, repo)

	before := time.Now().Add(-time.Minute)
	a := &Activity{CustomerID: customerID, ActivityType: ActivityCall, Description: "no date supplied"}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}
	after := time.Now().Add(time.Minute)

	if a.ID == uuid.Nil {
		t.Error("Create left the id unset")
	}
	if a.ActivityDate.IsZero() {
		t.Fatal("activity_date was left at the zero time")
	}
	if a.ActivityDate.Before(before) || a.ActivityDate.After(after) {
		t.Errorf("activity_date = %s, want roughly now (%s..%s)", a.ActivityDate, before, after)
	}

	stored, err := repo.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.ActivityDate.IsZero() {
		t.Error("the stored row has a zero activity_date")
	}
	if stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Errorf("created_at/updated_at were not stamped: %+v", stored)
	}
}

// CORRECTNESS: a supplied activity_date is kept. Back-dating a call log is the
// whole reason the field is writable.
func TestPostgresRepository_CreateKeepsASuppliedActivityDate(t *testing.T) {
	repo := NewRepository(testutil.RequireDB(t))
	ctx := context.Background()
	customerID := seedCustomer(t, repo)

	backdated := time.Date(2025, 11, 4, 15, 30, 0, 0, time.UTC)
	a := &Activity{
		CustomerID: customerID, ActivityType: ActivityMeeting,
		Description: "logged late", ActivityDate: backdated,
	}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	stored, err := repo.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !stored.ActivityDate.UTC().Equal(backdated) {
		t.Errorf("activity_date = %s, want the supplied %s", stored.ActivityDate.UTC(), backdated)
	}
}

// CORRECTNESS: the feed is scoped to one customer and ordered newest first.
func TestPostgresRepository_ListByCustomerIsScopedAndNewestFirst(t *testing.T) {
	repo := NewRepository(testutil.RequireDB(t))
	ctx := context.Background()
	mine, theirs := seedCustomer(t, repo), seedCustomer(t, repo)

	older := &Activity{
		CustomerID: mine, ActivityType: ActivityCall, Description: "older",
		ActivityDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	newer := &Activity{
		CustomerID: mine, ActivityType: ActivityNote, Description: "newer",
		ActivityDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	other := &Activity{
		CustomerID: theirs, ActivityType: ActivityEmail, Description: "another customer's note",
		ActivityDate: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}
	for _, a := range []*Activity{older, newer, other} {
		if err := repo.Create(ctx, a); err != nil {
			t.Fatalf("Create %s: %v", a.Description, err)
		}
	}

	got, err := repo.ListByCustomer(ctx, mine)
	if err != nil {
		t.Fatalf("ListByCustomer: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d activities, want 2 (the other customer's must not appear): %+v", len(got), got)
	}
	if got[0].ID != newer.ID || got[1].ID != older.ID {
		t.Errorf("order = [%s %s], want newest first [%s %s]", got[0].Description, got[1].Description, "newer", "older")
	}
	for _, a := range got {
		if a.CustomerID != mine {
			t.Errorf("activity %s belongs to customer %s, not %s", a.ID, a.CustomerID, mine)
		}
	}
}

// CORRECTNESS: updating or deleting a row that is not there is reported as
// "activity not found" via the zero-rows-affected check, not as a silent
// success — and it is reported as the ErrNotFound sentinel, which is what lets
// the handler answer 404 rather than 500 (repository_test.go).
func TestPostgresRepository_UpdateAndDeleteReportMissingRows(t *testing.T) {
	repo := NewRepository(testutil.RequireDB(t))
	ctx := context.Background()

	ghost := &Activity{ID: uuid.New(), CustomerID: uuid.New(), ActivityType: ActivityCall, Description: "x"}

	err := repo.Update(ctx, ghost)
	if err == nil {
		t.Error("Update on a nonexistent activity succeeded, want a not-found error")
	} else {
		if !strings.Contains(err.Error(), "activity not found") {
			t.Errorf("Update error = %q, want it to say the activity was not found", err)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Update error = %v, want ErrNotFound", err)
		}
	}

	err = repo.Delete(ctx, ghost.ID)
	if err == nil {
		t.Error("Delete on a nonexistent activity succeeded, want a not-found error")
	} else {
		if !strings.Contains(err.Error(), "activity not found") {
			t.Errorf("Delete error = %q, want it to say the activity was not found", err)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete error = %v, want ErrNotFound", err)
		}
	}
}

// CORRECTNESS: the UPDATE statement does not carry customer_id, so a caller who
// puts someone else's customer_id in the body cannot move an activity between
// customers. HandleUpdateActivity decodes the body's customer_id into the
// struct it hands the repository (handler.go:108-113 never overwrites it, only
// the id), so this SQL omission is the thing standing in the way.
func TestPostgresRepository_UpdateCannotMoveAnActivityToAnotherCustomer(t *testing.T) {
	repo := NewRepository(testutil.RequireDB(t))
	ctx := context.Background()
	mine, theirs := seedCustomer(t, repo), seedCustomer(t, repo)

	a := &Activity{CustomerID: mine, ActivityType: ActivityCall, Description: "mine"}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	hijacked := *a
	hijacked.CustomerID = theirs
	hijacked.Description = "reassigned"
	if err := repo.Update(ctx, &hijacked); err != nil {
		t.Fatalf("Update: %v", err)
	}

	stored, err := repo.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.CustomerID != mine {
		t.Errorf("the activity moved to customer %s; it must stay on %s", stored.CustomerID, mine)
	}
	if stored.Description != "reassigned" {
		t.Errorf("description = %q, want the update to have landed", stored.Description)
	}
}

// CORRECTNESS: Get maps pgx.ErrNoRows onto "activity not found" rather than
// leaking the driver error, which is what handler.go:93 turns into a 404.
func TestPostgresRepository_GetMapsNoRowsToNotFound(t *testing.T) {
	repo := NewRepository(testutil.RequireDB(t))

	_, err := repo.Get(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("Get on a random id succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "activity not found") {
		t.Errorf("error = %q, want it to say the activity was not found", err)
	}
	if strings.Contains(err.Error(), "no rows in result set") {
		t.Errorf("the raw pgx error leaked through the mapping: %q", err)
	}
}
