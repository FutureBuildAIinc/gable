// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

func newStaffRow(t *testing.T, db *database.DB, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.Pool.QueryRow(context.Background(),
		`INSERT INTO staff (email, full_name) VALUES ($1, 'Test Person') RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(context.Background(), `DELETE FROM staff WHERE id = $1`, id) })
	return id
}

func linkSvc(t *testing.T) (*Links, *Service, *auditRec, *database.DB) {
	t.Helper()
	dir := okDir()
	svc, rec, _ := newSvc(t, dir)
	db := svc.store.db
	return NewLinks(db, dir, rec), svc, rec, db
}

func TestLinkSetClearAndList(t *testing.T) {
	links, svc, rec, db := linkSvc(t)
	ctx := context.Background()
	if _, _, err := svc.SignIn(ctx, "a@x.example", "pw", "web"); err != nil {
		t.Fatal(err)
	}
	staffID := newStaffRow(t, db, "link-"+uuid.NewString()+"@x.example")
	sub := Subject("p", "u1")

	people, err := links.People(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var seen *Person
	for i := range people {
		if people[i].Sub == sub {
			seen = &people[i]
		}
	}
	if seen == nil || seen.Email != "a@x.example" {
		t.Fatalf("people = %+v", people)
	}
	if seen.LinkedStaffID != nil {
		t.Fatal("unlinked person shows a staff row")
	}

	if err := links.Set(ctx, staffID, sub, "admin-1"); err != nil {
		t.Fatal(err)
	}
	people, _ = links.People(ctx)
	for _, p := range people {
		if p.Sub == sub && (p.LinkedStaffID == nil || *p.LinkedStaffID != staffID) {
			t.Fatalf("link not shown: %+v", p)
		}
	}
	if err := links.Clear(ctx, staffID, "admin-1"); err != nil {
		t.Fatal(err)
	}
	if got := rec.actions(); got[len(got)-2] != "staff.sign_in_linked" || got[len(got)-1] != "staff.sign_in_unlinked" {
		t.Fatalf("audit = %v", got)
	}
}

func TestLinkRefusals(t *testing.T) {
	links, svc, _, db := linkSvc(t)
	ctx := context.Background()
	_, _, _ = svc.SignIn(ctx, "a@x.example", "pw", "web")
	sub := Subject("p", "u1")
	a := newStaffRow(t, db, "a-"+uuid.NewString()+"@x.example")
	b := newStaffRow(t, db, "b-"+uuid.NewString()+"@x.example")
	if err := links.Set(ctx, a, uuid.New(), "admin-1"); !errors.Is(err, ErrUnknownPerson) {
		t.Fatalf("a person who never signed in: %v", err)
	}
	if err := links.Set(ctx, uuid.New(), sub, "admin-1"); !errors.Is(err, ErrNoStaffRow) {
		t.Fatalf("an unknown staff row: %v", err)
	}
	if err := links.Set(ctx, a, sub, "admin-1"); err != nil {
		t.Fatal(err)
	}
	if err := links.Set(ctx, b, sub, "admin-1"); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("one person on two roster rows: %v", err)
	}
}

// Three admins linking the same person to three rows at once, at pool size 4:
// exactly one wins.
func TestLinkConcurrentContendersOneWins(t *testing.T) {
	dir := okDir()
	svc, rec, _ := newSvc(t, dir)
	_ = rec
	db := testutil.RequireDBMaxConns(t, 4)
	svc.store = NewStore(db)
	links := NewLinks(db, dir, &auditRec{})
	ctx := context.Background()
	_, _, _ = svc.SignIn(ctx, "a@x.example", "pw", "web")
	sub := Subject("p", "u1")
	var rows []uuid.UUID
	for i := 0; i < 3; i++ {
		rows = append(rows, newStaffRow(t, db, uuid.NewString()+"@x.example"))
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for _, r := range rows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := links.Set(ctx, r, sub, "admin-1"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d contenders linked the same person, want 1", wins)
	}
	var n int
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM staff WHERE sub = $1`, sub).Scan(&n)
	if n != 1 {
		t.Fatalf("%d roster rows hold the sub", n)
	}
}

func TestLinkRoutes(t *testing.T) {
	links, svc, _, db := linkSvc(t)
	ctx := context.Background()
	_, _, _ = svc.SignIn(ctx, "a@x.example", "pw", "web")
	staffID := newStaffRow(t, db, "route-"+uuid.NewString()+"@x.example")
	mux := http.NewServeMux()
	links.RegisterRoutes(mux)
	sub := Subject("p", "u1").String()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := do(http.MethodGet, "/api/v1/admin/staff/sign-in-people", ""); w.Code != http.StatusOK {
		t.Fatalf("people = %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPut, "/api/v1/admin/staff/"+staffID.String()+"/sign-in-link", `{"sub":"`+sub+`"}`); w.Code != http.StatusNoContent {
		t.Fatalf("set = %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPut, "/api/v1/admin/staff/"+staffID.String()+"/sign-in-link", `{"sub":"not-a-uuid"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad sub = %d", w.Code)
	}
	if w := do(http.MethodDelete, "/api/v1/admin/staff/"+staffID.String()+"/sign-in-link", ""); w.Code != http.StatusNoContent {
		t.Fatalf("clear = %d", w.Code)
	}
}
