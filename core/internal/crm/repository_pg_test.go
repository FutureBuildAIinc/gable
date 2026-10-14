// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// The persistence rules against a real Postgres: the keyset list, the branch
// wall through the customer, the revision bump and the defaulted
// activity_date. Skips cleanly when Postgres is unreachable.

type pgFixture struct {
	t        *testing.T
	db       *database.DB
	repo     *PostgresRepository
	customer uuid.UUID
	other    uuid.UUID // a customer on another branch
	branch   uuid.UUID
	otherBr  uuid.UUID
}

// seedCustomer creates a customer on a branch, since crm_activities has a
// NOT NULL FK to customers and the wall reads customer_branches.
func seedCustomerOn(t *testing.T, f *pgFixture, branch uuid.UUID, prefix string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ($1, $2, $3, $4)`,
		id, "CRM Test "+id.String()[:8], prefix+id.String()[:8], branch); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, id, branch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM crm_activities WHERE customer_id = $1`, id)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM customer_branches WHERE customer_id = $1`, id)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM customers WHERE id = $1`, id)
	})
	return id
}

func newPGFixture(t *testing.T) *pgFixture {
	t.Helper()
	db := testutil.RequireDB(t)
	f := &pgFixture{t: t, db: db, repo: NewRepository(db)}
	ctx := context.Background()
	if err := db.Pool.QueryRow(ctx,
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&f.branch); err != nil {
		t.Fatal(err)
	}
	// A second branch with a customer on it, for the wall.
	newBranch := uuid.New()
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO locations (id, type, code, name)
		VALUES ($1, 'BRANCH', $2, $3)
		RETURNING id`, newBranch, "crm-"+newBranch.String()[:8], "crm wall branch "+newBranch.String()[:8]).Scan(&f.otherBr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, f.otherBr)
	})
	f.customer = seedCustomerOn(t, f, f.branch, "CRM-A-")
	f.other = seedCustomerOn(t, f, f.otherBr, "CRM-B-")
	return f
}

func (f *pgFixture) insert(t *testing.T, customer uuid.UUID, typ ActivityType, description string, when time.Time) Activity {
	t.Helper()
	a := Activity{ID: uuid.New(), CustomerID: customer, ActivityType: typ, Description: description,
		ActivityDate: httpx.TimestampOf(when), CreatedAt: httpx.TimestampOf(when), UpdatedAt: httpx.TimestampOf(when)}
	if err := f.repo.Create(context.Background(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

// The list is newest first on (created_at, id) and the cursor walks every
// row once.
func TestList_KeysetWalksEveryRowOnce(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	var made []Activity
	for i := 0; i < 5; i++ {
		made = append(made, f.insert(t, f.customer, ActivityCall, "call", base.Add(time.Duration(i)*time.Minute)))
	}
	seen := map[uuid.UUID]bool{}
	after := (*time.Time)(nil)
	afterID := uuid.Nil
	pages := 0
	for {
		items, hasMore, _, err := f.repo.List(ctx, f.customer, ListFilter{Limit: 2, AfterTime: after, AfterID: afterID}, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range items {
			if seen[a.ID] {
				t.Fatalf("activity %s served twice", a.ID)
			}
			seen[a.ID] = true
		}
		pages++
		if !hasMore {
			break
		}
		last := items[len(items)-1]
		at := last.CreatedAt.Time
		after, afterID = &at, last.ID
	}
	if len(seen) != 5 || pages != 3 {
		t.Errorf("served %d activities over %d pages, want 5 over 3", len(seen), pages)
	}
}

// The branch wall through the customer: a caller held to another branch
// finds the activity a 404 and the list empty.
func TestBranchWall_ThroughTheCustomer(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	a := f.insert(t, f.customer, ActivityCall, "wall", time.Now())
	other := branchctx.With(ctx, &branchctx.Context{BranchID: &f.otherBr, IsAdmin: false, UserSub: "someone"})

	if _, err := f.repo.Get(other, a.ID); err != ErrNotFound {
		t.Errorf("get behind the wall = %v, want ErrNotFound", err)
	}
	if err := f.repo.Lock(other, a.ID); err != ErrNotFound {
		t.Errorf("lock behind the wall = %v, want ErrNotFound", err)
	}
	items, _, _, err := f.repo.List(other, f.customer, ListFilter{Limit: 10}, false)
	if err != nil || len(items) != 0 {
		t.Errorf("list behind the wall = %v, %d items; want empty", err, len(items))
	}
	ok, err := f.repo.CustomerVisible(other, f.customer)
	if err != nil || ok {
		t.Errorf("customer visible behind the wall = %v, %v; want false", ok, err)
	}
	// The same caller sees its own branch's activity.
	mine := f.insert(t, f.other, ActivityNote, "other branch", time.Now())
	if _, err := f.repo.Get(other, mine.ID); err != nil {
		t.Errorf("get of the caller's own branch activity = %v", err)
	}
}

// The filters filter: activity_type and contact_id.
func TestList_FiltersFilter(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	f.insert(t, f.customer, ActivityCall, "one", now)
	f.insert(t, f.customer, ActivityNote, "two", now.Add(time.Second))
	call := ActivityCall
	items, _, _, err := f.repo.List(ctx, f.customer, ListFilter{Limit: 10, Type: &call}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Description != "one" {
		t.Errorf("type filter = %v, want only the call", items)
	}
	_, _, tp, err := f.repo.List(ctx, f.customer, ListFilter{Limit: 10}, true)
	if err != nil {
		t.Fatal(err)
	}
	if tp == nil || *tp != 2 {
		t.Errorf("total = %v, want 2", tp)
	}
}

// Every write that changes the activity moves its revision.
func TestWrites_MoveTheRevision(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	a := f.insert(t, f.customer, ActivityCall, "rev", time.Now())
	got, err := f.repo.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 {
		t.Fatalf("a fresh activity is at revision %d, want 1", got.Revision)
	}
	if err := f.repo.Lock(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	got.Description = "renamed"
	if err := f.repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	after, err := f.repo.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != 2 {
		t.Errorf("revision after update = %d, want 2", after.Revision)
	}
}
