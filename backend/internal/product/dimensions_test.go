// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// These tests exist for one reason: on the product-geometry path, NULL and 0
// are different facts, and every layer between the JSON body and the SQL
// argument list has to keep them apart.
//
//	null -> "the PIM has no geometry for this SKU"  -> AI_LM falls back
//	0    -> "this SKU measures zero inches"          -> AI_LM believes it
//
// Migration 080_ailm_integration_contract.sql deliberately left the five
// columns nullable to preserve that distinction. Anything that reintroduces a
// `NOT NULL DEFAULT`, a COALESCE, or a non-pointer struct field should make
// something in this file fail.

// --- test double ----------------------------------------------------------

// recordingRepo captures the Geometry the service hands the repository. Only
// UpdateDimensions and GetProduct are exercised; the rest of the interface is
// satisfied so the fake can stand in for a Repository.
type recordingRepo struct {
	Repository // nil embedded interface: any unexpected call panics loudly

	gotID     uuid.UUID
	got       Geometry
	calls     int
	updateErr error

	product *Product
	getErr  error
}

func (r *recordingRepo) UpdateDimensions(_ context.Context, id uuid.UUID, g Geometry) error {
	r.calls++
	r.gotID = id
	r.got = g
	if r.updateErr != nil {
		return r.updateErr
	}
	// Mirror what Postgres would then hold, so a later GetProduct read-back
	// reflects the write rather than a hand-written fixture.
	if r.product != nil {
		r.product.LengthIn = g.LengthIn
		r.product.WidthIn = g.WidthIn
		r.product.HeightIn = g.HeightIn
		r.product.Stackable = g.Stackable
		r.product.GeometrySource = g.GeometrySource
	}
	return nil
}

func (r *recordingRepo) GetProduct(_ context.Context, _ uuid.UUID) (*Product, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.product, nil
}

func f64(v float64) *float64 { return &v }
func boolp(v bool) *bool     { return &v }
func strp(v string) *string  { return &v }

func newTestHandler(repo *recordingRepo) (*Handler, *http.ServeMux) {
	h := NewHandler(NewService(repo))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux
}

func patchDimensions(t *testing.T, mux *http.ServeMux, id uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/products/"+id.String()+"/dimensions", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// --- the null-vs-zero contract -------------------------------------------

// CORRECTNESS, and the whole reason the columns are nullable: an operator who
// clears a dimension field must clear the column to NULL. Writing 0 would tell
// AI_LM the SKU is a real zero-size box, which its resolveGeometry() would
// happily pack instead of falling back to its own override table.
func TestUpdateDimensions_ClearedFieldsWriteNullNotZero(t *testing.T) {
	id := uuid.New()

	cases := []struct {
		name string
		body string
		want Geometry
	}{
		{
			// The editor's "clear everything" save: explicit JSON nulls.
			name: "explicit nulls clear every column",
			body: `{"length_in":null,"width_in":null,"height_in":null,"stackable":null,"geometry_source":null}`,
			want: Geometry{},
		},
		{
			// An empty body must behave identically to explicit nulls: an
			// absent field is "not recorded", not "zero".
			name: "omitted fields clear every column",
			body: `{}`,
			want: Geometry{},
		},
		{
			// The distinction that matters. A real 0 is preserved as a real 0
			// and must NOT be folded into the null case.
			name: "explicit zero is preserved as a real measurement",
			body: `{"length_in":0,"width_in":0,"height_in":0,"stackable":false}`,
			want: Geometry{
				LengthIn:       f64(0),
				WidthIn:        f64(0),
				HeightIn:       f64(0),
				Stackable:      boolp(false),
				GeometrySource: strp(GeometrySourceParametric),
			},
		},
		{
			// Partial geometry is legal: an operator may know the length of a
			// board before anyone has measured its width.
			name: "one recorded dimension leaves the others null",
			body: `{"length_in":96}`,
			want: Geometry{
				LengthIn:       f64(96),
				GeometrySource: strp(GeometrySourceParametric),
			},
		},
		{
			name: "a full triple round-trips",
			body: `{"length_in":96,"width_in":3.5,"height_in":1.5,"stackable":true}`,
			want: Geometry{
				LengthIn:       f64(96),
				WidthIn:        f64(3.5),
				HeightIn:       f64(1.5),
				Stackable:      boolp(true),
				GeometrySource: strp(GeometrySourceParametric),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recordingRepo{product: &Product{ID: id, SKU: "LUM-248-PREM"}}
			_, mux := newTestHandler(repo)

			if w := patchDimensions(t, mux, id, tc.body); w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
			}
			if repo.calls != 1 {
				t.Fatalf("repository called %d times, want 1", repo.calls)
			}
			if repo.gotID != id {
				t.Errorf("wrote geometry for %s, want %s", repo.gotID, id)
			}
			assertGeometry(t, "repository argument", repo.got, tc.want)
		})
	}
}

// CORRECTNESS: nil must reach the wire as JSON `null`, not be omitted and not
// be rendered as 0. AI_LM decodes these into *float64 / *bool and branches on
// nil, so an `omitempty` or a zero-value default on any of these fields is a
// silent behaviour change for the load planner.
func TestGeometry_NilFieldsSerializeAsJSONNull(t *testing.T) {
	b, err := json.Marshal(Geometry{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	want := `{"length_in":null,"width_in":null,"height_in":null,"stackable":null,"geometry_source":null}`
	if got != want {
		t.Fatalf("empty Geometry marshalled as\n  %s\nwant\n  %s", got, want)
	}

	// Same requirement for the Product the ERP read path returns, which is
	// what the geometry editor prefills from.
	pb, err := json.Marshal(Product{ID: uuid.New(), SKU: "HW-NAIL-16D"})
	if err != nil {
		t.Fatalf("marshal product: %v", err)
	}
	for _, field := range []string{`"length_in":null`, `"width_in":null`, `"height_in":null`, `"stackable":null`, `"geometry_source":null`} {
		if !strings.Contains(string(pb), field) {
			t.Errorf("Product JSON is missing %s; got %s", field, pb)
		}
	}
	for _, forbidden := range []string{`"length_in":0`, `"stackable":false`, `"geometry_source":""`} {
		if strings.Contains(string(pb), forbidden) {
			t.Errorf("Product JSON reports %s for an unrecorded field; got %s", forbidden, pb)
		}
	}
}

// CORRECTNESS: the response body of the PATCH is what the editor re-renders
// from, so it has to carry nulls too. A handler that echoed a zeroed struct
// would make a cleared field look like a 0 in the UI immediately after saving.
func TestUpdateDimensions_ResponseReportsNullForClearedFields(t *testing.T) {
	id := uuid.New()
	repo := &recordingRepo{product: &Product{
		ID:             id,
		SKU:            "LUM-248-PREM",
		LengthIn:       f64(96),
		WidthIn:        f64(3.5),
		HeightIn:       f64(1.5),
		Stackable:      boolp(true),
		GeometrySource: strp(GeometrySourceParametric),
	}}
	_, mux := newTestHandler(repo)

	w := patchDimensions(t, mux, id, `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body)
	}
	for _, field := range []string{"length_in", "width_in", "height_in", "stackable", "geometry_source"} {
		v, ok := raw[field]
		if !ok {
			t.Errorf("response omits %q; a cleared field must be reported as null, not dropped", field)
			continue
		}
		if string(v) != "null" {
			t.Errorf("response %q = %s, want null", field, v)
		}
	}
}

// --- geometry_source provenance ------------------------------------------

// CORRECTNESS: geometry_source must never claim a provenance for geometry that
// does not exist. The integration layer's resolveGeometrySource() reports "" —
// not 'parametric' — for a row with no dimensions, precisely because
// "parametric with no dimensions" is a claim AI_LM cannot check. Clearing the
// dimensions has to clear the source with them.
func TestUpdateDimensions_GeometrySourceProvenance(t *testing.T) {
	id := uuid.New()

	cases := []struct {
		name string
		body string
		want *string
	}{
		{"operator-entered geometry is parametric", `{"length_in":96}`, strp(GeometrySourceParametric)},
		{"an explicit source wins", `{"length_in":96,"geometry_source":"MANUAL"}`, strp("MANUAL")},
		{"a future mesh source is passed through", `{"height_in":12,"geometry_source":"mesh"}`, strp("mesh")},
		{"clearing the geometry clears the source", `{"length_in":null}`, nil},
		{"clearing wins even over an explicit source", `{"geometry_source":"MANUAL"}`, nil},
		{"a blank source is null, not an empty string", `{"length_in":96,"geometry_source":"   "}`, strp(GeometrySourceParametric)},
		{"stackable alone is not geometry", `{"stackable":true}`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recordingRepo{product: &Product{ID: id}}
			_, mux := newTestHandler(repo)

			if w := patchDimensions(t, mux, id, tc.body); w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
			}
			assertStrPtr(t, "geometry_source", repo.got.GeometrySource, tc.want)
		})
	}
}

// --- request handling -----------------------------------------------------

func TestUpdateDimensions_RejectsBadInput(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		body     string
		wantCode int
	}{
		{"non-uuid id", "/api/v1/products/not-a-uuid/dimensions", `{}`, http.StatusBadRequest},
		{"malformed json", "/api/v1/products/" + uuid.New().String() + "/dimensions", `{"length_in":`, http.StatusBadRequest},
		{"wrong type for a dimension", "/api/v1/products/" + uuid.New().String() + "/dimensions", `{"length_in":"96 inches"}`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recordingRepo{product: &Product{}}
			_, mux := newTestHandler(repo)

			req := httptest.NewRequest(http.MethodPatch, tc.path, strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body)
			}
			if repo.calls != 0 {
				t.Errorf("a rejected request still wrote geometry (%d calls)", repo.calls)
			}
		})
	}
}

// CORRECTNESS: a failed write must not be reported as success. The handler
// optimistically reads the row back after writing, and an operator who sees
// 200 will believe the dimensions are in the PIM.
func TestUpdateDimensions_WriteFailureIs500(t *testing.T) {
	id := uuid.New()
	repo := &recordingRepo{product: &Product{ID: id}, updateErr: errors.New("db down")}
	_, mux := newTestHandler(repo)

	if w := patchDimensions(t, mux, id, `{"length_in":96}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", w.Code, w.Body)
	}
}

// A read-back failure is NOT a write failure: the geometry is already
// persisted, so reporting 500 would send the operator back to re-enter data
// that is safely stored.
func TestUpdateDimensions_ReadBackFailureStillReportsSuccess(t *testing.T) {
	id := uuid.New()
	repo := &recordingRepo{product: &Product{ID: id}, getErr: errors.New("replica lagging")}
	_, mux := newTestHandler(repo)

	w := patchDimensions(t, mux, id, `{"length_in":96}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
	}
	if repo.got.LengthIn == nil || *repo.got.LengthIn != 96 {
		t.Errorf("length_in was not persisted before the read-back failed: %v", repo.got.LengthIn)
	}
}

// --- the SQL that carries the null all the way down -----------------------

// CORRECTNESS: the write must hand Postgres the raw pointer values. A
// COALESCE(..., 0) or NULLIF here is the single most likely way to "fix" a null
// into a zero, and it would be invisible from the handler tests above because
// they stop at the repository boundary.
//
// The read side is already null-preserving and stays that way: the query in
// internal/integrations/ailm_store_pg.go selects `p.length_in, p.width_in,
// p.height_in, p.stackable` with no COALESCE, and
// TestListProductsWireShape in that package pins
// `"length_in":null,...,"stackable":null` on the GET /api/integration/products
// response body. Together those two facts are the end-to-end guarantee:
// clearing a dimension here writes NULL, and NULL is what AI_LM reads back.
func TestUpdateDimensionsQuery_WritesRawNulls(t *testing.T) {
	q := updateDimensionsQuery

	for _, banned := range []string{"COALESCE", "NULLIF", "coalesce", "nullif"} {
		if strings.Contains(q, banned) {
			t.Errorf("updateDimensionsQuery contains %s — geometry columns must be written raw so an unset dimension lands as SQL NULL, not a default:\n%s", banned, q)
		}
	}

	// Each geometry column must be assigned from its own placeholder.
	for _, assign := range []string{
		"length_in = $1", "width_in = $2", "height_in = $3",
		"stackable = $4", "geometry_source = $5",
	} {
		if !strings.Contains(q, assign) {
			t.Errorf("updateDimensionsQuery is missing %q:\n%s", assign, q)
		}
	}
	if !strings.Contains(q, "WHERE id = $6") {
		t.Errorf("updateDimensionsQuery must be scoped to one product:\n%s", q)
	}
}

// --- helpers --------------------------------------------------------------

func assertGeometry(t *testing.T, what string, got, want Geometry) {
	t.Helper()
	assertF64Ptr(t, what+" length_in", got.LengthIn, want.LengthIn)
	assertF64Ptr(t, what+" width_in", got.WidthIn, want.WidthIn)
	assertF64Ptr(t, what+" height_in", got.HeightIn, want.HeightIn)
	assertBoolPtr(t, what+" stackable", got.Stackable, want.Stackable)
	assertStrPtr(t, what+" geometry_source", got.GeometrySource, want.GeometrySource)
}

func assertF64Ptr(t *testing.T, what string, got, want *float64) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil:
		t.Errorf("%s = nil, want %v — a recorded measurement was dropped", what, *want)
	case want == nil:
		t.Errorf("%s = %v, want nil (SQL NULL) — an unrecorded dimension became a real number", what, *got)
	case *got != *want:
		t.Errorf("%s = %v, want %v", what, *got, *want)
	}
}

func assertBoolPtr(t *testing.T, what string, got, want *bool) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil:
		t.Errorf("%s = nil, want %v", what, *want)
	case want == nil:
		t.Errorf("%s = %v, want nil (SQL NULL) — 'unknown' became a definite answer", what, *got)
	case *got != *want:
		t.Errorf("%s = %v, want %v", what, *got, *want)
	}
}

func assertStrPtr(t *testing.T, what string, got, want *string) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil:
		t.Errorf("%s = nil, want %q", what, *want)
	case want == nil:
		t.Errorf("%s = %q, want nil (SQL NULL)", what, *got)
	case *got != *want:
		t.Errorf("%s = %q, want %q", what, *got, *want)
	}
}
