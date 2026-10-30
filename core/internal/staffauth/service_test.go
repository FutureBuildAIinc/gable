// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/appwrite"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
)

type fakeDir struct {
	mu           sync.Mutex
	user         appwrite.User
	member       appwrite.Membership
	found        bool
	signInErr    error
	userErr      error
	memberErr    error
	ended        []string
	sessionsMade int
}

func (f *fakeDir) CreateEmailPasswordSession(ctx context.Context, email, pw string) (appwrite.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.signInErr != nil {
		return appwrite.Session{}, f.signInErr
	}
	f.sessionsMade++
	return appwrite.Session{ID: "aws-1", UserID: f.user.ID, Secret: "aws-secret"}, nil
}
func (f *fakeDir) GetUser(ctx context.Context, id string) (appwrite.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.user, f.userErr
}
func (f *fakeDir) ListMemberships(ctx context.Context, team, user string) (appwrite.Membership, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.member, f.found, f.memberErr
}
func (f *fakeDir) DeleteSessionAsUser(ctx context.Context, id, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = append(f.ended, id)
	return nil
}

type auditRec struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (a *auditRec) Log(ctx context.Context, e audit.Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}
func (a *auditRec) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, e := range a.entries {
		out = append(out, e.Action)
	}
	return out
}

func okDir() *fakeDir {
	return &fakeDir{
		user:   appwrite.User{ID: "u1", Status: true, EmailVerification: true},
		member: appwrite.Membership{UserID: "u1", Confirmed: true, Roles: []string{"gable-sales", "gable-finance"}},
		found:  true,
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newSvc(t *testing.T, dir *fakeDir) (*Service, *auditRec, *clock) {
	t.Helper()
	db := testutil.RequireDB(t)
	rec := &auditRec{}
	clk := &clock{t: time.Now().UTC()}
	svc := NewService(dir, NewStore(db), rec, Settings{ProjectID: "p", TeamID: "t"}, nil)
	svc.now = clk.now
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM staff_sessions WHERE sub = $1`, Subject("p", "u1"))
	})
	return svc, rec, clk
}

func rowCount(t *testing.T, svc *Service) int {
	t.Helper()
	var n int
	if err := svc.store.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM staff_sessions WHERE sub = $1`, Subject("p", "u1")).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSignInIssuesAFreshSessionAndEndsAppwrites(t *testing.T) {
	dir := okDir()
	svc, rec, _ := newSvc(t, dir)
	ctx := context.Background()
	id1, who, err := svc.SignIn(ctx, "a@x.example", "pw", "web")
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := svc.SignIn(ctx, "a@x.example", "pw", "web")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 || id1 == "" {
		t.Fatal("a sign in must issue a new session id every time")
	}
	if who.Sub != Subject("p", "u1") {
		t.Fatal("sub is not the name based UUID")
	}
	if len(who.Roles) != 2 || who.Roles[0] != "sales" || who.Roles[1] != "finance" {
		t.Fatalf("roles = %v", who.Roles)
	}
	if len(dir.ended) != 2 {
		t.Fatalf("appwrite sessions ended = %d, want 2", len(dir.ended))
	}
	claims, err := svc.Resolve(ctx, id1)
	if err != nil || claims.Subject != who.Sub.String() || claims.Role != "" || claims.Email != "" || len(claims.Roles) != 2 {
		t.Fatalf("claims = %+v, %v", claims, err)
	}
	if got := rec.actions(); len(got) != 2 || got[0] != "staff.sign_in" {
		t.Fatalf("audit = %v", got)
	}
}

func TestSignInRefusals(t *testing.T) {
	cases := map[string]func(*fakeDir){
		"non member":       func(d *fakeDir) { d.found = false },
		"unconfirmed":      func(d *fakeDir) { d.member.Confirmed = false },
		"blocked":          func(d *fakeDir) { d.user.Status = false },
		"unverified email": func(d *fakeDir) { d.user.EmailVerification = false },
		"unknown roles":    func(d *fakeDir) { d.member.Roles = []string{"bogus"} },
		"appwrite owner":   func(d *fakeDir) { d.member.Roles = []string{"owner", "gable-owner"} },
		"mfa":              func(d *fakeDir) { d.user.MFA = true },
		"deleted user":     func(d *fakeDir) { d.userErr = &appwrite.Error{StatusCode: http.StatusNotFound} },
		"bad credentials":  func(d *fakeDir) { d.signInErr = &appwrite.Error{StatusCode: http.StatusUnauthorized} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir := okDir()
			mutate(dir)
			svc, rec, _ := newSvc(t, dir)
			id, _, err := svc.SignIn(context.Background(), "a@x.example", "pw", "web")
			if !errors.Is(err, ErrRefused) || id != "" {
				t.Fatalf("SignIn = %q, %v; want ErrRefused", id, err)
			}
			if rowCount(t, svc) != 0 {
				t.Fatal("a refused sign in left a session row")
			}
			if dir.sessionsMade > 0 && len(dir.ended) != dir.sessionsMade {
				t.Fatal("the appwrite session of a refused sign in was not ended")
			}
			if a := rec.actions(); len(a) != 1 || a[0] != "staff.sign_in_refused" {
				t.Fatalf("audit = %v", a)
			}
			if rec.entries[0].Changes["email"] != "a@x.example" || rec.entries[0].Changes["reason"] == "" {
				t.Fatalf("refusal row = %v", rec.entries[0].Changes)
			}
			if _, has := rec.entries[0].Changes["password"]; has {
				t.Fatal("the password reached the audit row")
			}
		})
	}
}

func TestSignInAppwriteOutageIsNotARefusal(t *testing.T) {
	dir := okDir()
	dir.signInErr = errors.New("connection refused")
	svc, _, _ := newSvc(t, dir)
	if _, _, err := svc.SignIn(context.Background(), "a@x.example", "pw", "web"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestResolveFailsClosed(t *testing.T) {
	dir := okDir()
	svc, _, clk := newSvc(t, dir)
	ctx := context.Background()
	if _, err := svc.Resolve(ctx, ""); !errors.Is(err, ErrNoSession) {
		t.Fatal("empty id resolved")
	}
	if _, err := svc.Resolve(ctx, "never-issued"); !errors.Is(err, ErrNoSession) {
		t.Fatal("unknown id resolved")
	}
	// revoked by sign out
	id, _, _ := svc.SignIn(ctx, "a@x.example", "pw", "web")
	svc.SignOut(ctx, id)
	if _, err := svc.Resolve(ctx, id); !errors.Is(err, ErrNoSession) {
		t.Fatal("a signed out id still resolves")
	}
	// idle expiry
	id, _, _ = svc.SignIn(ctx, "a@x.example", "pw", "web")
	clk.t = clk.t.Add(svc.set.IdleLimit + time.Minute)
	if _, err := svc.Resolve(ctx, id); !errors.Is(err, ErrNoSession) {
		t.Fatal("an idle expired id resolves")
	}
	// absolute expiry, even when kept busy
	clk.t = time.Now().UTC()
	id, _, _ = svc.SignIn(ctx, "a@x.example", "pw", "web")
	for i := 0; i < 13; i++ {
		clk.t = clk.t.Add(time.Hour)
		if _, err := svc.Resolve(ctx, id); err != nil {
			if i < 11 {
				t.Fatalf("hour %d: %v", i, err)
			}
			return
		}
	}
	t.Fatal("an absolutely expired id resolves")
}

func TestRefreshRevokesOnDefiniteRemoval(t *testing.T) {
	removals := map[string]func(*fakeDir){
		"membership gone": func(d *fakeDir) { d.found = false },
		"blocked":         func(d *fakeDir) { d.user.Status = false },
		"deleted":         func(d *fakeDir) { d.userErr = &appwrite.Error{StatusCode: http.StatusNotFound} },
		"roles stripped":  func(d *fakeDir) { d.member.Roles = []string{"bogus"} },
	}
	for name, mutate := range removals {
		t.Run(name, func(t *testing.T) {
			dir := okDir()
			svc, rec, clk := newSvc(t, dir)
			ctx := context.Background()
			id, _, _ := svc.SignIn(ctx, "a@x.example", "pw", "web")
			mutate(dir)
			clk.t = clk.t.Add(svc.set.RefreshAfter + time.Second)
			if _, err := svc.Resolve(ctx, id); !errors.Is(err, ErrNoSession) {
				t.Fatalf("err = %v, want ErrNoSession", err)
			}
			// the revocation sticks even if Appwrite recovers
			*dir = *okDir()
			if _, err := svc.Resolve(ctx, id); !errors.Is(err, ErrNoSession) {
				t.Fatal("a revoked session came back")
			}
			if a := rec.actions(); a[len(a)-1] != "staff.session_revoked" {
				t.Fatalf("audit = %v", a)
			}
		})
	}
}

func TestRefreshReplacesChangedRolesAtOnce(t *testing.T) {
	dir := okDir()
	svc, _, clk := newSvc(t, dir)
	ctx := context.Background()
	id, _, _ := svc.SignIn(ctx, "a@x.example", "pw", "web")
	dir.member.Roles = []string{"gable-admin"}
	clk.t = clk.t.Add(svc.set.RefreshAfter + time.Second)
	claims, err := svc.Resolve(ctx, id)
	if err != nil || len(claims.Roles) != 1 || claims.Roles[0] != "admin" {
		t.Fatalf("claims = %+v, %v", claims, err)
	}
	got, _ := svc.store.Get(ctx, hashID(id))
	if len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Fatalf("stored roles = %v", got.Roles)
	}
}

// An outage keeps the session on its snapshot until the snapshot reaches its
// maximum age, then refuses it until a definite read succeeds.
func TestRefreshOutageUsesSnapshotUntilMaxAge(t *testing.T) {
	dir := okDir()
	svc, _, clk := newSvc(t, dir)
	svc.set.IdleLimit = 48 * time.Hour
	svc.set.AbsoluteLimit = 72 * time.Hour
	ctx := context.Background()
	id, _, _ := svc.SignIn(ctx, "a@x.example", "pw", "web")
	dir.userErr = errors.New("connection refused")
	clk.t = clk.t.Add(svc.set.RefreshAfter + time.Second)
	if _, err := svc.Resolve(ctx, id); err != nil {
		t.Fatalf("outage inside the snapshot age closed the counter: %v", err)
	}
	clk.t = clk.t.Add(svc.set.SnapshotMaxAge)
	if _, err := svc.Resolve(ctx, id); !errors.Is(err, ErrNoSession) {
		t.Fatal("a snapshot past its maximum age was served")
	}
	dir.userErr = nil
	if _, err := svc.Resolve(ctx, id); err != nil {
		t.Fatalf("a definite read did not restore the session: %v", err)
	}
}
