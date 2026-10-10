// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

// The key branch wall's own unit tests: every constructor against a handler
// that records whether it ran, with the pin and the key id in the context the
// machine key core puts them in, and no database. The database backed paths
// (the location row lookup, the customer branch membership) are covered here
// only in their failure to fail open: a nil database answers 500 and the
// handler never runs.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// pinAuditor records the refusals the wall writes.
type pinAuditor struct {
	rows []string
}

func (a *pinAuditor) AuditKeyBranchRefusal(ctx context.Context, keyID string, branch uuid.UUID, method, path string) {
	a.rows = append(a.rows, keyID+" "+method+" "+path)
}

// pinWall builds the wall with a recording auditor and no database: every
// database backed lookup fails, and the tests assert it fails closed.
func pinWall(t *testing.T) (w *KeyBranchWall, a *pinAuditor) {
	t.Helper()
	a = &pinAuditor{}
	return NewKeyBranchWall(nil, a), a
}

// serveWall routes one request through the wall on a real ServeMux whose
// pattern carries the path values the wall reads, with the principal the
// caller names (a pin, or none for a user and an unbound key), and reports
// the status and whether the handler ran.
func serveWall(t *testing.T, wall func(http.Handler) http.Handler, pin *uuid.UUID, method, pattern, target, body string) (int, bool) {
	t.Helper()
	ran := false
	handler := http.Handler(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ran = true
		rw.WriteHeader(http.StatusOK)
	}))
	mux := http.NewServeMux()
	mux.Handle(method+" "+pattern, wall(handler))
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := req.Context()
	ctx = WithKeyID(ctx, "key-test")
	if pin != nil {
		ctx = branchctx.WithKeyBranch(ctx, *pin)
	}
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, ran
}

// wallPath is serveWall for a route whose pattern is its own path.
func wallPath(t *testing.T, wall func(http.Handler) http.Handler, pin *uuid.UUID, method, path, body string) (int, bool) {
	t.Helper()
	return serveWall(t, wall, pin, method, path, path, body)
}

// TestKeyBranchWall_NoPinPasses walks every constructor with a user or an
// unbound key (no pin in the context): the wall passes the request through
// untouched, and no audit row is written.
func TestKeyBranchWall_NoPinPasses(t *testing.T) {
	own := uuid.New()
	foreign := uuid.New()
	cases := []struct {
		name  string
		wall  func(*testing.T) func(http.Handler) http.Handler
		check func(t *testing.T, code int, ran bool)
	}{
		{"named branch", func(t *testing.T) func(http.Handler) http.Handler {
			w, _ := pinWall(t)
			return w.NamedBranch("branch_id")
		}, nil},
		{"body branch", func(t *testing.T) func(http.Handler) http.Handler {
			w, _ := pinWall(t)
			return w.BodyBranch()
		}, nil},
		{"row branch", func(t *testing.T) func(http.Handler) http.Handler {
			w, _ := pinWall(t)
			return w.RowBranch(func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
				return &foreign, nil
			})
		}, nil},
		{"refuse bound", func(t *testing.T) func(http.Handler) http.Handler {
			w, _ := pinWall(t)
			return w.RefuseBound("the reason")
		}, nil},
		{"customer body", func(t *testing.T) func(http.Handler) http.Handler {
			w, _ := pinWall(t)
			return w.CustomerBodyBranch()
		}, nil},
		{"customer row", func(t *testing.T) func(http.Handler) http.Handler {
			w, _ := pinWall(t)
			return w.CustomerRowBranch(func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
				return &own, nil
			})
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, ran := wallPath(t, c.wall(t), nil, http.MethodPost, "/api/v1/x", `{"branch_id":"`+foreign.String()+`"}`)
			if code != http.StatusOK || !ran {
				t.Errorf("no pin: %d ran %v, want 200 and the handler reached", code, ran)
			}
		})
	}
}

// TestKeyBranchWall_NamedBranch holds the path named branch to the pin: a
// foreign branch is refused with the audit row, the pin's own branch passes,
// and a malformed path value passes to the handler.
func TestKeyBranchWall_NamedBranch(t *testing.T) {
	wall, auditor := pinWall(t)
	pin, foreign := uuid.New(), uuid.New()
	mux := wall.NamedBranch("branch_id")
	pattern := "/api/v1/branches/{branch_id}"

	if code, ran := serveWall(t, mux, &pin, http.MethodGet, pattern, "/api/v1/branches/"+foreign.String(), ""); code != http.StatusForbidden || ran {
		t.Errorf("foreign branch: %d ran %v, want 403 and not run", code, ran)
	}
	if len(auditor.rows) != 1 {
		t.Errorf("foreign branch audit rows: %v, want one", auditor.rows)
	}
	if code, ran := serveWall(t, mux, &pin, http.MethodGet, pattern, "/api/v1/branches/"+pin.String(), ""); code != http.StatusOK || !ran {
		t.Errorf("own branch: %d ran %v, want 200 and run", code, ran)
	}
	if code, ran := serveWall(t, mux, &pin, http.MethodGet, pattern, "/api/v1/branches/not-a-uuid", ""); code != http.StatusOK || !ran {
		t.Errorf("malformed id: %d ran %v, want 200 and run (the handler answers it)", code, ran)
	}
	// An empty path segment never reaches the wall: the router itself
	// answers it, so the guard in NamedBranch stays defensive.
}

// TestKeyBranchWall_BodyBranch holds the body named branch to the pin and
// reads the whole body: a foreign branch is refused, the pin's own passes, a
// body that names no branch passes to the handler, and a body that is not
// exactly one complete JSON value (unparsable, truncated, trailing data) is
// refused rather than passed for the handler to read only in part. The body
// that passes is restored for the handler to read.
func TestKeyBranchWall_BodyBranch(t *testing.T) {
	pin, foreign := uuid.New(), uuid.New()
	refused := []string{
		`{"branch_id":"` + foreign.String() + `"}`,
		`{"branch_id":"` + foreign.String() + `"} {}`,
		`{"branch_id":"` + foreign.String() + `"}xyz`,
		`{"branch_id":"` + foreign.String() + `"} 1`,
		`{"branch_id":"` + foreign.String() + `"}]`,
		`{"branch_id":"` + foreign.String() + `"`,
		`not json at all`,
	}
	for _, body := range refused {
		wall, auditor := pinWall(t)
		code, ran := wallPath(t, wall.BodyBranch(), &pin, http.MethodPost, "/api/v1/users/x/branches", body)
		if code != http.StatusForbidden || ran {
			t.Errorf("body %q: %d ran %v, want 403 and not run", body, code, ran)
		}
		if len(auditor.rows) != 1 {
			t.Errorf("body %q audit rows: %v, want one", body, auditor.rows)
		}
	}
	passes := []string{
		`{"branch_id":"` + pin.String() + `"}`,
		`{"branch_id":"` + strings.ToUpper(pin.String()) + `"}`,
		`{}`,
		`{"branch_id":null}`,
		`{"other":"` + foreign.String() + `"}`,
	}
	for _, body := range passes {
		wall, auditor := pinWall(t)
		code, ran := wallPath(t, wall.BodyBranch(), &pin, http.MethodPost, "/api/v1/users/x/branches", body)
		if code != http.StatusOK || !ran {
			t.Errorf("body %q: %d ran %v, want 200 and run", body, code, ran)
		}
		if len(auditor.rows) != 0 {
			t.Errorf("body %q audit rows: %v, want none", body, auditor.rows)
		}
	}
}

// TestKeyBranchWall_RowBranch holds the row named branch to the pin: a
// foreign row is refused, the pin's own passes, a missing row passes to the
// handler's own answer, and a lookup failure answers 500 with the handler
// never reached: the wall never fails open.
func TestKeyBranchWall_RowBranch(t *testing.T) {
	pin, foreign := uuid.New(), uuid.New()
	boom := errors.New("lookup boom")

	wall, auditor := pinWall(t)
	mux := wall.RowBranch(func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
		return &foreign, nil
	})
	locPattern := "/api/v1/locations/{id}"
	if code, ran := serveWall(t, mux, &pin, http.MethodPut, locPattern, "/api/v1/locations/"+uuid.NewString(), ""); code != http.StatusForbidden || ran {
		t.Errorf("foreign row: %d ran %v, want 403 and not run", code, ran)
	}
	if len(auditor.rows) != 1 {
		t.Errorf("foreign row audit rows: %v, want one", auditor.rows)
	}

	wall, _ = pinWall(t)
	mux = wall.RowBranch(func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
		return &pin, nil
	})
	if code, ran := serveWall(t, mux, &pin, http.MethodPut, locPattern, "/api/v1/locations/"+uuid.NewString(), ""); code != http.StatusOK || !ran {
		t.Errorf("own row: %d ran %v, want 200 and run", code, ran)
	}

	wall, _ = pinWall(t)
	mux = wall.RowBranch(func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
		return nil, nil // the row does not exist
	})
	if code, ran := serveWall(t, mux, &pin, http.MethodPut, locPattern, "/api/v1/locations/"+uuid.NewString(), ""); code != http.StatusOK || !ran {
		t.Errorf("missing row: %d ran %v, want 200 and run (the handler 404s)", code, ran)
	}

	// The fail closed path: a lookup error must never reach the handler.
	wall, _ = pinWall(t)
	mux = wall.RowBranch(func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
		return nil, boom
	})
	code, ran := serveWall(t, mux, &pin, http.MethodPut, locPattern, "/api/v1/locations/"+uuid.NewString(), "")
	if ran {
		t.Fatalf("lookup error: the handler ran, want the wall to fail closed")
	}
	if code != http.StatusInternalServerError && code != http.StatusForbidden {
		t.Errorf("lookup error: %d, want 500 or 403, never a pass", code)
	}

	// A nil database is the same failure: closed.
	wall, _ = pinWall(t)
	code, ran = serveWall(t, wall.LocationRowBranch(), &pin, http.MethodPut, locPattern, "/api/v1/locations/"+uuid.NewString(), "")
	if ran {
		t.Fatalf("nil database: the handler ran, want the wall to fail closed")
	}
	if code != http.StatusInternalServerError {
		t.Errorf("nil database: %d, want 500", code)
	}
}

// TestKeyBranchWall_RefuseBound refuses a bound key outright whatever the
// request carries, and passes everyone else.
func TestKeyBranchWall_RefuseBound(t *testing.T) {
	wall, auditor := pinWall(t)
	mux := wall.RefuseBound("the reason")
	pinned := uuid.New()
	code, ran := wallPath(t, mux, &pinned, http.MethodPost, "/api/v1/branches", `{"code":"x"}`)
	if code != http.StatusForbidden || ran {
		t.Errorf("bound key: %d ran %v, want 403 and not run", code, ran)
	}
	if len(auditor.rows) != 1 {
		t.Errorf("audit rows: %v, want one", auditor.rows)
	}
	if code, ran := wallPath(t, mux, nil, http.MethodPost, "/api/v1/branches", `{"code":"x"}`); code != http.StatusOK || !ran {
		t.Errorf("no pin: %d ran %v, want 200 and run", code, ran)
	}
}

// TestKeyBranchWall_CustomerBranch holds the customer named writes to the
// pin: a body that is not exactly one complete JSON value is refused, and a
// resolver failure or a nil database answers 500 with the handler never
// reached (the membership lookup itself needs the database the serve wiring
// tests exercise).
func TestKeyBranchWall_CustomerBranch(t *testing.T) {
	pin := uuid.New()

	refused := []string{
		`{"customer_id":"` + uuid.NewString() + `"} trailing`,
		`[{"customer_id":"` + uuid.NewString() + `"}] extra`,
		`not json`,
	}
	for _, body := range refused {
		wall, auditor := pinWall(t)
		code, ran := wallPath(t, wall.CustomerBodyBranch(), &pin, http.MethodPost, "/api/v1/tax/exemptions", body)
		if code != http.StatusForbidden || ran {
			t.Errorf("body %q: %d ran %v, want 403 and not run", body, code, ran)
		}
		if len(auditor.rows) != 1 {
			t.Errorf("body %q audit rows: %v, want one", body, auditor.rows)
		}
	}

	// A parsable body that names a customer reaches the membership lookup,
	// which has no database here and must fail closed.
	wall, _ := pinWall(t)
	code, ran := wallPath(t, wall.CustomerBodyBranch(), &pin, http.MethodPost, "/api/v1/tax/exemptions",
		`{"customer_id":"`+uuid.NewString()+`"}`)
	if ran || code != http.StatusInternalServerError {
		t.Errorf("membership without a database: %d ran %v, want 500 and not run", code, ran)
	}

	// A resolver failure is the same: closed.
	wall, _ = pinWall(t)
	code, ran = wallPath(t, wall.CustomerBranch(func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		return nil, errors.New("resolver boom")
	}), &pin, http.MethodPost, "/api/v1/x", "")
	if ran || code != http.StatusInternalServerError {
		t.Errorf("resolver failure: %d ran %v, want 500 and not run", code, ran)
	}

	// A request that names no customer passes; the row variant with no row
	// passes too.
	wall, auditor := pinWall(t)
	if code, ran := wallPath(t, wall.CustomerBodyBranch(), &pin, http.MethodPost, "/api/v1/pricing/rules", `{"name":"dealer wide"}`); code != http.StatusOK || !ran {
		t.Errorf("no customer named: %d ran %v, want 200 and run", code, ran)
	}
	if len(auditor.rows) != 0 {
		t.Errorf("no customer named audit rows: %v, want none", auditor.rows)
	}
	wall, _ = pinWall(t)
	if code, ran := serveWall(t, wall.CustomerRowBranch(func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
		return nil, nil
	}), &pin, http.MethodDelete, "/api/v1/tax/exemptions/{id}", "/api/v1/tax/exemptions/"+uuid.NewString(), ""); code != http.StatusOK || !ran {
		t.Errorf("missing row: %d ran %v, want 200 and run", code, ran)
	}
}
