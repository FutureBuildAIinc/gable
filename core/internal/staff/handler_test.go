// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staff

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// --- fakes ------------------------------------------------------------------

// fakeStore is an in-memory store so the whole admin surface can be driven over
// httptest with no Postgres. It mirrors the three facts the real tables hold —
// roster rows, (staff_id, module_id) grants, and modules.<id>.enabled — because
// those are exactly the facts POST /api/integration/validate-staff reads back.
type fakeStore struct {
	mu       sync.Mutex
	staff    map[uuid.UUID]*Staff
	grants   map[uuid.UUID]map[string]string // staffID -> moduleID -> granted_by
	enabled  map[string]bool                 // moduleID -> enabled
	failWith error                           // when non-nil every method fails

	lastUpdate  *UpdateStaffInput // captured payload of the last Update
	createCalls int
	grantCalls  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		staff:   map[uuid.UUID]*Staff{},
		grants:  map[uuid.UUID]map[string]string{},
		enabled: map[string]bool{},
	}
}

// seed inserts a roster row directly (bypassing the API) and returns its id.
func (f *fakeStore) seed(email, name, role string, active bool, modules ...string) uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.New()
	f.staff[id] = &Staff{
		ID: id, Email: email, FullName: name, Role: role, Active: active,
		CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC(),
	}
	f.grants[id] = map[string]string{}
	for _, m := range modules {
		f.grants[id][m] = "seed"
	}
	return id
}

// snapshot returns a copy with the current grant set attached, the way the real
// repository's LEFT JOIN ... ARRAY_AGG does.
func (f *fakeStore) snapshot(id uuid.UUID) *Staff {
	src := f.staff[id]
	if src == nil {
		return nil
	}
	out := *src
	mods := []string{}
	for m := range f.grants[id] {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	out.Modules = mods
	return &out
}

func (f *fakeStore) List(context.Context) ([]Staff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	out := []Staff{}
	for id := range f.staff {
		out = append(out, *f.snapshot(id))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FullName < out[j].FullName })
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, id uuid.UUID) (*Staff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	s := f.snapshot(id)
	if s == nil {
		return nil, ErrNotFound
	}
	return s, nil
}

func (f *fakeStore) Create(_ context.Context, in CreateStaffInput) (*Staff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.failWith != nil {
		return nil, f.failWith
	}
	role := in.Role
	if role == "" {
		role = "staff"
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	id := uuid.New()
	f.staff[id] = &Staff{
		ID: id, Email: in.Email, FullName: in.FullName, StaffNo: in.StaffNo,
		Role: role, Active: active,
		CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC(),
	}
	f.grants[id] = map[string]string{}
	return f.snapshot(id), nil
}

func (f *fakeStore) Update(_ context.Context, id uuid.UUID, in UpdateStaffInput) (*Staff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	captured := in
	f.lastUpdate = &captured
	if f.failWith != nil {
		return nil, f.failWith
	}
	s := f.staff[id]
	if s == nil {
		return nil, ErrNotFound
	}
	if in.Email != nil {
		s.Email = *in.Email
	}
	if in.FullName != nil {
		s.FullName = *in.FullName
	}
	if in.StaffNo != nil {
		s.StaffNo = in.StaffNo
	}
	if in.Role != nil {
		s.Role = *in.Role
	}
	if in.Active != nil {
		s.Active = *in.Active
	}
	return f.snapshot(id), nil
}

func (f *fakeStore) GrantModule(_ context.Context, staffID uuid.UUID, moduleID, grantedBy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grantCalls++
	if f.failWith != nil {
		return f.failWith
	}
	if f.grants[staffID] == nil {
		f.grants[staffID] = map[string]string{}
	}
	// ON CONFLICT DO NOTHING: an existing grant keeps its original attribution.
	if _, exists := f.grants[staffID][moduleID]; !exists {
		f.grants[staffID][moduleID] = grantedBy
	}
	return nil
}

func (f *fakeStore) RevokeModule(_ context.Context, staffID uuid.UUID, moduleID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	delete(f.grants[staffID], moduleID)
	return nil
}

func (f *fakeStore) EnabledModules(context.Context) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	out := map[string]bool{}
	for k, v := range f.enabled {
		if v {
			out[k] = true
		}
	}
	return out, nil
}

func (f *fakeStore) SetModuleEnabled(_ context.Context, moduleID string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	f.enabled[moduleID] = enabled
	return nil
}

func (f *fakeStore) grantedBy(staffID uuid.UUID, moduleID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.grants[staffID][moduleID]
	return v, ok
}

// recordingAudit captures audit entries instead of writing audit_log rows.
type recordingAudit struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (a *recordingAudit) Log(_ context.Context, e audit.Entry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
}

func (a *recordingAudit) all() []audit.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Entry(nil), a.entries...)
}

// --- harness ----------------------------------------------------------------

// adminClaims is a caller the RequireRole("admin","owner") guard admits.
func adminClaims() *middleware.UserClaims {
	return &middleware.UserClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "admin-sub-1"},
		Email:            "avery@gable.com",
		Role:             "admin",
	}
}

type harness struct {
	mux   *http.ServeMux
	store *fakeStore
	audit *recordingAudit
	// claims is attached to every request the harness issues; nil = dev mode.
	claims *middleware.UserClaims
}

// newHarness mounts the real routes behind the real RequireRole guard, so the
// tests exercise the same wiring cmd/server/wire_staff.go installs.
func newHarness(t *testing.T) *harness {
	t.Helper()
	st := newFakeStore()
	rec := &recordingAudit{}
	svc := NewService(st)
	svc.auditLog = rec
	h := NewHandler(svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	return &harness{mux: mux, store: st, audit: rec, claims: adminClaims()}
}

func (h *harness) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if h.claims != nil {
		r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, h.claims))
	}
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %T from %q: %v", out, w.Body.String(), err)
	}
	return out
}

func hasModule(mods []string, id string) bool {
	for _, m := range mods {
		if m == id {
			return true
		}
	}
	return false
}

// --- staff CRUD -------------------------------------------------------------

func TestListStaff_ReturnsRosterWithGrants(t *testing.T) {
	h := newHarness(t)
	h.store.seed("dispatcher@gable.com", "Dana Ramirez", "dispatcher", true, "ai_lm")
	h.store.seed("yard@gable.com", "Yuki Tan", "yard", true)

	w := h.do(t, http.MethodGet, "/api/v1/admin/staff", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := decode[[]Staff](t, w)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// Ordered by full name: Dana before Yuki.
	if got[0].FullName != "Dana Ramirez" || got[1].FullName != "Yuki Tan" {
		t.Fatalf("order = %q, %q", got[0].FullName, got[1].FullName)
	}
	if !hasModule(got[0].Modules, "ai_lm") {
		t.Errorf("Dana should carry her ai_lm grant, got %v", got[0].Modules)
	}
	if len(got[1].Modules) != 0 {
		t.Errorf("Yuki holds no grants, got %v", got[1].Modules)
	}
}

func TestListStaff_EmptyRosterIsArrayNotNull(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodGet, "/api/v1/admin/staff", "")
	if got := strings.TrimSpace(w.Body.String()); got != "[]" {
		t.Errorf("body = %q, want []; a null would render as a broken table", got)
	}
}

func TestGetStaff_NotFoundAndBadID(t *testing.T) {
	h := newHarness(t)

	w := h.do(t, http.MethodGet, "/api/v1/admin/staff/"+uuid.New().String(), "")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown id: status = %d, want 404", w.Code)
	}

	w = h.do(t, http.MethodGet, "/api/v1/admin/staff/not-a-uuid", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("malformed id: status = %d, want 400", w.Code)
	}
}

func TestCreateStaff_RequiresEmailAndFullName(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no email", `{"full_name":"Nobody"}`},
		{"no full_name", `{"email":"nobody@gable.com"}`},
		{"both blank", `{"email":"","full_name":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			w := h.do(t, http.MethodPost, "/api/v1/admin/staff", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			if h.store.createCalls != 0 {
				t.Errorf("store.Create called %d times; validation must reject before the write", h.store.createCalls)
			}
		})
	}
}

func TestCreateStaff_DefaultsRoleAndActive(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodPost, "/api/v1/admin/staff", `{"email":"new@gable.com","full_name":"New Hire"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := decode[Staff](t, w)
	if got.Email != "new@gable.com" || got.FullName != "New Hire" {
		t.Errorf("echoed %q/%q", got.Email, got.FullName)
	}
	if got.Role != "staff" {
		t.Errorf("role = %q, want the 'staff' default", got.Role)
	}
	if !got.Active {
		t.Error("a new hire must default to active")
	}
	if got.Modules == nil || len(got.Modules) != 0 {
		t.Errorf("modules = %v, want an empty set: creating staff must not grant anything", got.Modules)
	}
}

// A PUT that sends one field must leave the others alone. If UpdateStaffInput
// used values instead of pointers, deactivating a staff member would blank their
// email — and validate-staff looks people up BY email.
func TestUpdateStaff_PartialUpdateLeavesUnsentFieldsNil(t *testing.T) {
	h := newHarness(t)
	id := h.store.seed("dana@gable.com", "Dana Ramirez", "dispatcher", true, "ai_lm")

	w := h.do(t, http.MethodPut, "/api/v1/admin/staff/"+id.String(), `{"active":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	in := h.store.lastUpdate
	if in == nil {
		t.Fatal("store.Update was never called")
	}
	if in.Active == nil || *in.Active {
		t.Errorf("active = %v, want a non-nil false", in.Active)
	}
	if in.Email != nil || in.FullName != nil || in.Role != nil || in.StaffNo != nil {
		t.Errorf("unsent fields must stay nil, got email=%v name=%v role=%v staff_no=%v",
			in.Email, in.FullName, in.Role, in.StaffNo)
	}

	got := decode[Staff](t, w)
	if got.Active {
		t.Error("response still reports active")
	}
	if got.Email != "dana@gable.com" {
		t.Errorf("email = %q, want it untouched", got.Email)
	}
	if !hasModule(got.Modules, "ai_lm") {
		t.Error("deactivating must not delete the grant — reactivating should restore access")
	}
}

func TestUpdateStaff_UnknownIDIs404(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodPut, "/api/v1/admin/staff/"+uuid.New().String(), `{"role":"yard"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- grant / revoke ---------------------------------------------------------

func TestGrantModule_WritesGrantAttributedToCaller(t *testing.T) {
	h := newHarness(t)
	id := h.store.seed("yuki@gable.com", "Yuki Tan", "yard", true)

	w := h.do(t, http.MethodPost, "/api/v1/admin/staff/"+id.String()+"/modules", `{"module_id":"ai_lm"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	by, ok := h.store.grantedBy(id, "ai_lm")
	if !ok {
		t.Fatal("no grant row was written")
	}
	if by != "admin-sub-1" {
		t.Errorf("granted_by = %q, want the caller's JWT subject", by)
	}
	got := decode[Staff](t, w)
	if !hasModule(got.Modules, "ai_lm") {
		t.Errorf("response modules = %v, want the fresh grant so the UI checkbox settles", got.Modules)
	}
}

func TestGrantModule_FallsBackToEmailWhenSubjectMissing(t *testing.T) {
	h := newHarness(t)
	h.claims = &middleware.UserClaims{Email: "avery@gable.com", Role: "owner"}
	id := h.store.seed("yuki@gable.com", "Yuki Tan", "yard", true)

	if w := h.do(t, http.MethodPost, "/api/v1/admin/staff/"+id.String()+"/modules", `{"module_id":"ai_lm"}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if by, _ := h.store.grantedBy(id, "ai_lm"); by != "avery@gable.com" {
		t.Errorf("granted_by = %q, want the caller's email fallback", by)
	}
}

func TestGrantModule_RejectsMissingModuleID(t *testing.T) {
	h := newHarness(t)
	id := h.store.seed("yuki@gable.com", "Yuki Tan", "yard", true)

	for _, body := range []string{`{}`, `{"module_id":""}`} {
		w := h.do(t, http.MethodPost, "/api/v1/admin/staff/"+id.String()+"/modules", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, w.Code)
		}
	}
	if h.store.grantCalls != 0 {
		t.Errorf("store.GrantModule called %d times for an empty module_id", h.store.grantCalls)
	}
}

func TestGrantModule_RejectsMalformedStaffID(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodPost, "/api/v1/admin/staff/nope/modules", `{"module_id":"ai_lm"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if h.store.grantCalls != 0 {
		t.Error("a malformed id must not reach the store")
	}
}

func TestGrantModule_IsIdempotent(t *testing.T) {
	h := newHarness(t)
	id := h.store.seed("yuki@gable.com", "Yuki Tan", "yard", true)
	path := "/api/v1/admin/staff/" + id.String() + "/modules"

	for i := 0; i < 3; i++ {
		if w := h.do(t, http.MethodPost, path, `{"module_id":"ai_lm"}`); w.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d", i, w.Code)
		}
	}
	w := h.do(t, http.MethodGet, "/api/v1/admin/staff/"+id.String(), "")
	got := decode[Staff](t, w)
	if len(got.Modules) != 1 || got.Modules[0] != "ai_lm" {
		t.Errorf("modules = %v, want exactly one ai_lm grant after three grants", got.Modules)
	}
}

func TestRevokeModule_RemovesGrant(t *testing.T) {
	h := newHarness(t)
	id := h.store.seed("dana@gable.com", "Dana Ramirez", "dispatcher", true, "ai_lm")

	w := h.do(t, http.MethodDelete, "/api/v1/admin/staff/"+id.String()+"/modules/ai_lm", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if _, ok := h.store.grantedBy(id, "ai_lm"); ok {
		t.Error("grant row survived the revoke — the user would still authenticate into AI_LM")
	}
	got := decode[Staff](t, w)
	if hasModule(got.Modules, "ai_lm") {
		t.Errorf("response modules = %v, want ai_lm gone", got.Modules)
	}
}

func TestRevokeModule_IsIdempotent(t *testing.T) {
	h := newHarness(t)
	id := h.store.seed("yuki@gable.com", "Yuki Tan", "yard", true)
	w := h.do(t, http.MethodDelete, "/api/v1/admin/staff/"+id.String()+"/modules/ai_lm", "")
	if w.Code != http.StatusOK {
		t.Errorf("revoking a grant that was never made: status = %d, want 200", w.Code)
	}
}

func TestGrantAndRevokeAreAudited(t *testing.T) {
	h := newHarness(t)
	id := h.store.seed("yuki@gable.com", "Yuki Tan", "yard", true)
	base := "/api/v1/admin/staff/" + id.String() + "/modules"

	h.do(t, http.MethodPost, base, `{"module_id":"ai_lm"}`)
	h.do(t, http.MethodDelete, base+"/ai_lm", "")

	entries := h.audit.all()
	if len(entries) != 2 {
		t.Fatalf("audit entries = %d, want 2 (grant + revoke); handing out module access must leave a trail", len(entries))
	}
	if entries[0].Action != "module.grant" || entries[1].Action != "module.revoke" {
		t.Fatalf("actions = %q, %q", entries[0].Action, entries[1].Action)
	}
	for i, e := range entries {
		if e.EntityType != "staff" {
			t.Errorf("entry %d: entity_type = %q, want staff", i, e.EntityType)
		}
		if e.EntityID != id {
			t.Errorf("entry %d: entity_id = %s, want %s", i, e.EntityID, id)
		}
		if e.Changes["module_id"] != "ai_lm" {
			t.Errorf("entry %d: changes = %v, want module_id ai_lm", i, e.Changes)
		}
	}
	if entries[0].Changes["granted_by"] != "admin-sub-1" {
		t.Errorf("grant entry must attribute the caller, got %v", entries[0].Changes["granted_by"])
	}
}

// A nil *audit.Logger must not be boxed into the auditSink interface: a typed
// nil is a non-nil interface and would nil-panic inside Log on the first grant.
func TestWithAuditLog_NilLoggerDoesNotPanic(t *testing.T) {
	svc := NewService(newFakeStore()).WithAuditLog(nil)
	if svc.auditLog != nil {
		t.Fatal("a nil *audit.Logger must not be stored as a non-nil sink")
	}
	if err := svc.GrantModule(context.Background(), uuid.New(), "ai_lm", "someone"); err != nil {
		t.Fatalf("grant with no audit logger: %v", err)
	}
}

// --- global module flag -----------------------------------------------------

func TestListModules_DefaultsToDisabledWhenSettingAbsent(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodGet, "/api/v1/admin/modules", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	mods := decode[[]Module](t, w)
	if len(mods) != 1 || mods[0].ID != "ai_lm" {
		t.Fatalf("modules = %+v, want the ai_lm catalog entry", mods)
	}
	if mods[0].Enabled {
		t.Error("a missing modules.ai_lm.enabled row must read as disabled, not enabled")
	}
	if mods[0].Name != "AI_LM" {
		t.Errorf("name = %q, want the display name", mods[0].Name)
	}
}

func TestSetModuleEnabled_RoundTrips(t *testing.T) {
	h := newHarness(t)

	w := h.do(t, http.MethodPut, "/api/v1/admin/modules/ai_lm", `{"enabled":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("enable: status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := decode[Module](t, w); got.ID != "ai_lm" || !got.Enabled {
		t.Errorf("enable response = %+v", got)
	}
	if mods := decode[[]Module](t, h.do(t, http.MethodGet, "/api/v1/admin/modules", "")); !mods[0].Enabled {
		t.Error("GET /modules did not reflect the enable")
	}

	w = h.do(t, http.MethodPut, "/api/v1/admin/modules/ai_lm", `{"enabled":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: status = %d", w.Code)
	}
	if mods := decode[[]Module](t, h.do(t, http.MethodGet, "/api/v1/admin/modules", "")); mods[0].Enabled {
		t.Error("GET /modules still reports enabled after the kill switch was thrown")
	}
}

// The kill switch must not destroy grants: an operator who disables AI_LM for an
// afternoon and re-enables it should get the same roster back, not an empty one.
func TestDisablingModuleGloballyPreservesGrants(t *testing.T) {
	h := newHarness(t)
	id := h.store.seed("dana@gable.com", "Dana Ramirez", "dispatcher", true, "ai_lm")

	h.do(t, http.MethodPut, "/api/v1/admin/modules/ai_lm", `{"enabled":false}`)

	got := decode[Staff](t, h.do(t, http.MethodGet, "/api/v1/admin/staff/"+id.String(), ""))
	if !hasModule(got.Modules, "ai_lm") {
		t.Fatalf("modules = %v; the admin surface reports GRANTS, which survive the global toggle", got.Modules)
	}

	h.do(t, http.MethodPut, "/api/v1/admin/modules/ai_lm", `{"enabled":true}`)
	got = decode[Staff](t, h.do(t, http.MethodGet, "/api/v1/admin/staff/"+id.String(), ""))
	if !hasModule(got.Modules, "ai_lm") {
		t.Error("grant lost across a disable/enable cycle")
	}
}

// --- the entitlement contract shared with validate-staff --------------------

// entitledPerIntegrationRule is the predicate internal/integrations applies in
// ValidateStaff: active AND the ai_lm grant AND the global flag. It is spelled
// out here so the table below asserts that state driven through THIS admin API
// lands in the three facts that rule reads.
func entitledPerIntegrationRule(active, granted, globallyEnabled bool) bool {
	return active && granted && globallyEnabled
}

func TestAdminSurfaceFeedsTheValidateStaffFacts(t *testing.T) {
	cases := []struct {
		name         string
		active       bool
		grant        bool
		enable       bool
		wantEntitled bool
	}{
		{"granted, enabled, active", true, true, true, true},
		{"granted and active but module off globally", true, true, false, false},
		{"enabled and active but no grant", true, false, true, false},
		{"granted and enabled but deactivated", false, true, true, false},
		{"nothing", false, false, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			// Everyone starts active with no grant and the module off.
			id := h.store.seed("subject@gable.com", "Sub Ject", "dispatcher", true)
			staffPath := "/api/v1/admin/staff/" + id.String()

			if tc.grant {
				if w := h.do(t, http.MethodPost, staffPath+"/modules", `{"module_id":"ai_lm"}`); w.Code != http.StatusOK {
					t.Fatalf("grant: status = %d", w.Code)
				}
			}
			if tc.enable {
				if w := h.do(t, http.MethodPut, "/api/v1/admin/modules/ai_lm", `{"enabled":true}`); w.Code != http.StatusOK {
					t.Fatalf("enable: status = %d", w.Code)
				}
			}
			if !tc.active {
				if w := h.do(t, http.MethodPut, staffPath, `{"active":false}`); w.Code != http.StatusOK {
					t.Fatalf("deactivate: status = %d", w.Code)
				}
			}

			// Read the three facts back the way an operator would.
			member := decode[Staff](t, h.do(t, http.MethodGet, staffPath, ""))
			mods := decode[[]Module](t, h.do(t, http.MethodGet, "/api/v1/admin/modules", ""))

			if member.Active != tc.active {
				t.Errorf("staff.active = %v, want %v", member.Active, tc.active)
			}
			if hasModule(member.Modules, "ai_lm") != tc.grant {
				t.Errorf("ai_lm grant = %v, want %v (modules=%v)", hasModule(member.Modules, "ai_lm"), tc.grant, member.Modules)
			}
			if mods[0].Enabled != tc.enable {
				t.Errorf("modules.ai_lm.enabled = %v, want %v", mods[0].Enabled, tc.enable)
			}

			got := entitledPerIntegrationRule(member.Active, hasModule(member.Modules, "ai_lm"), mods[0].Enabled)
			if got != tc.wantEntitled {
				t.Errorf("entitled = %v, want %v", got, tc.wantEntitled)
			}
		})
	}
}

// --- authorization ----------------------------------------------------------

// adminRoutes is every route RegisterRoutes mounts, with a body where the
// handler needs one. Kept in one place so a new route added without a guard
// shows up as a failing case rather than as an open door.
func adminRoutes(staffID uuid.UUID) []struct {
	method string
	path   string
	body   string
} {
	id := staffID.String()
	return []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/admin/staff", ""},
		{http.MethodPost, "/api/v1/admin/staff", `{"email":"x@gable.com","full_name":"X"}`},
		{http.MethodGet, "/api/v1/admin/staff/" + id, ""},
		{http.MethodPut, "/api/v1/admin/staff/" + id, `{"role":"yard"}`},
		{http.MethodPost, "/api/v1/admin/staff/" + id + "/modules", `{"module_id":"ai_lm"}`},
		{http.MethodDelete, "/api/v1/admin/staff/" + id + "/modules/ai_lm", ""},
		{http.MethodGet, "/api/v1/admin/modules", ""},
		{http.MethodPut, "/api/v1/admin/modules/ai_lm", `{"enabled":true}`},
	}
}

// Every route must be behind RequireRole. An unguarded endpoint here lets any
// authenticated user grant themselves AI_LM access.
func TestEveryAdminRouteRejectsNonAdminRole(t *testing.T) {
	h := newHarness(t)
	id := h.store.seed("dana@gable.com", "Dana Ramirez", "dispatcher", true, "ai_lm")
	h.claims = &middleware.UserClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "sales-sub"},
		Role:             "sales",
		Roles:            []string{"sales", "warehouse"},
	}

	for _, rt := range adminRoutes(id) {
		w := h.do(t, rt.method, rt.path, rt.body)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403 for role=sales", rt.method, rt.path, w.Code)
		}
	}
	// And no state moved.
	if h.store.createCalls != 0 || h.store.grantCalls != 0 || h.store.lastUpdate != nil {
		t.Error("a rejected caller reached the store")
	}
	if _, ok := h.store.grantedBy(id, "ai_lm"); !ok {
		t.Error("a rejected caller revoked a grant")
	}
	if len(h.store.enabled) != 0 {
		t.Error("a rejected caller flipped the global module flag")
	}
}

func TestEveryAdminRouteIsMountedAndReachableByAdmin(t *testing.T) {
	for _, role := range []string{"admin", "owner"} {
		t.Run(role, func(t *testing.T) {
			h := newHarness(t)
			id := h.store.seed("dana@gable.com", "Dana Ramirez", "dispatcher", true, "ai_lm")
			h.claims = &middleware.UserClaims{
				RegisteredClaims: jwt.RegisteredClaims{Subject: role + "-sub"},
				Role:             role,
			}
			for _, rt := range adminRoutes(id) {
				w := h.do(t, rt.method, rt.path, rt.body)
				if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
					t.Errorf("%s %s: status = %d — route not mounted for that method", rt.method, rt.path, w.Code)
				}
				if w.Code == http.StatusForbidden {
					t.Errorf("%s %s: %s must be allowed", rt.method, rt.path, role)
				}
			}
		})
	}
}

// --- failure propagation ----------------------------------------------------

func TestStoreFailuresSurfaceAs500(t *testing.T) {
	h := newHarness(t)
	id := h.store.seed("dana@gable.com", "Dana Ramirez", "dispatcher", true)
	h.store.failWith = errors.New("connection refused")

	for _, rt := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/admin/staff", ""},
		{http.MethodPost, "/api/v1/admin/staff", `{"email":"x@gable.com","full_name":"X"}`},
		{http.MethodGet, "/api/v1/admin/modules", ""},
		{http.MethodPut, "/api/v1/admin/modules/ai_lm", `{"enabled":true}`},
		{http.MethodPost, "/api/v1/admin/staff/" + id.String() + "/modules", `{"module_id":"ai_lm"}`},
	} {
		w := h.do(t, rt.method, rt.path, rt.body)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s %s: status = %d, want 500", rt.method, rt.path, w.Code)
		}
	}
}

func TestMalformedJSONIsRejected(t *testing.T) {
	h := newHarness(t)
	id := h.store.seed("dana@gable.com", "Dana Ramirez", "dispatcher", true)
	for _, rt := range []struct {
		method, path string
	}{
		{http.MethodPost, "/api/v1/admin/staff"},
		{http.MethodPut, "/api/v1/admin/staff/" + id.String()},
		{http.MethodPost, "/api/v1/admin/staff/" + id.String() + "/modules"},
		{http.MethodPut, "/api/v1/admin/modules/ai_lm"},
	} {
		w := h.do(t, rt.method, rt.path, `{"not json`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: status = %d, want 400", rt.method, rt.path, w.Code)
		}
	}
}
