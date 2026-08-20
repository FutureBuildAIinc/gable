// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The scheduled-report CRUD surface. Schedules can be created, listed and
// deleted — but nothing in this repository executes them, and these tests
// exist mostly to pin that the API says so.
//
// The failure mode being guarded against is not a crash. It is an operator
// configuring "AR aging to the controller every Monday at 9", getting a 201,
// and finding out a quarter later that no email was ever sent. An API that
// stores a schedule it cannot run must say it cannot run it.

func newScheduleTestMux(repo *fakeRepo, executor ScheduleExecutor) (*Handler, *http.ServeMux) {
	h := NewHandler(NewService(repo))
	if executor != nil {
		h = h.WithScheduleExecutor(executor)
	}
	mux := http.NewServeMux()
	h.RegisterBuilderRoutes(mux)
	return h, mux
}

func do(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// recordingExecutor is a stand-in for a working scheduler.
type recordingExecutor struct {
	added []ReportSchedule
	err   error
}

func (e *recordingExecutor) AddSchedule(_ context.Context, s ReportSchedule) error {
	if e.err != nil {
		return e.err
	}
	e.added = append(e.added, s)
	return nil
}

// *Scheduler is the intended production implementation of the seam. If its
// signature drifts, this stops compiling rather than silently leaving the
// handler unwireable.
var _ ScheduleExecutor = (*Scheduler)(nil)

// --- the honesty contract -------------------------------------------------

// CORRECTNESS: with no executor attached — the state of every deployment in
// this repository — a created schedule must be reported as NOT running, and
// must be persisted with a status that says so.
func TestCreateReportSchedule_SaysItWillNotRun(t *testing.T) {
	repo := newFakeRepo()
	repo.saved["r1"] = &SavedReport{ID: "r1", Name: "AR aging", EntityType: "invoices"}
	_, mux := newScheduleTestMux(repo, nil)

	w := do(mux, http.MethodPost, "/api/v1/reporting/schedules",
		`{"report_id":"r1","cron_expression":"0 0 9 * * *","recipients":["controller@example.com"],"format":"CSV"}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", w.Code, w.Body)
	}

	var got ReportScheduleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body)
	}

	if got.Execution.Enabled {
		t.Error("the API claims schedules execute, but nothing in this repository runs them")
	}
	if got.Execution.Summary == "" {
		t.Error("execution.summary is empty; a client has nothing to show the operator")
	}
	if len(got.Execution.Blockers) == 0 {
		t.Error("execution.blockers is empty; a disabled feature must say what is missing")
	}
	if got.Schedule.Status != ScheduleStatusStored {
		t.Errorf("stored status = %q, want %q — 'ACTIVE' would be a lie and would make Scheduler.Start pick the row up", got.Schedule.Status, ScheduleStatusStored)
	}

	// It really was persisted; "not executing" is not "not saved".
	if len(repo.schedules) != 1 {
		t.Fatalf("persisted %d schedules, want 1", len(repo.schedules))
	}
	if repo.schedules[0].Status != ScheduleStatusStored {
		t.Errorf("row written with status %q, want %q", repo.schedules[0].Status, ScheduleStatusStored)
	}
}

// CORRECTNESS: the list endpoint carries the same disclosure. A UI that only
// ever calls GET must still be able to tell the operator these do not fire.
func TestListReportSchedules_CarriesExecutionStatus(t *testing.T) {
	repo := newFakeRepo()
	repo.schedules = []ReportSchedule{
		{ID: "s1", ReportID: "r1", CronExpression: "0 0 9 * * *", Status: ScheduleStatusStored, Format: "CSV"},
	}
	_, mux := newScheduleTestMux(repo, nil)

	w := do(mux, http.MethodGet, "/api/v1/reporting/schedules", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
	}

	var got ReportScheduleListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body)
	}
	if got.Execution.Enabled {
		t.Error("list endpoint claims schedules execute")
	}
	if len(got.Schedules) != 1 || got.Schedules[0].ID != "s1" {
		t.Errorf("schedules = %+v, want the one stored row", got.Schedules)
	}
}

// An empty table is an empty list, not JSON null — a client mapping over the
// result should not have to null-check.
func TestListReportSchedules_EmptyIsEmptyArray(t *testing.T) {
	_, mux := newScheduleTestMux(newFakeRepo(), nil)

	w := do(mux, http.MethodGet, "/api/v1/reporting/schedules", "")
	if !strings.Contains(w.Body.String(), `"schedules":[]`) {
		t.Errorf("empty list rendered as %s, want \"schedules\":[]", w.Body)
	}
}

// CORRECTNESS, the other direction: the disclosure is derived from whether an
// executor is actually attached, not from a hand-maintained constant. Attaching
// a working executor must flip both the disclosure and the persisted status, so
// the two can never drift apart.
func TestCreateReportSchedule_WithExecutorReportsRunning(t *testing.T) {
	repo := newFakeRepo()
	exec := &recordingExecutor{}
	_, mux := newScheduleTestMux(repo, exec)

	w := do(mux, http.MethodPost, "/api/v1/reporting/schedules",
		`{"report_id":"r1","cron_expression":"0 0 9 * * *","recipients":["a@example.com"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", w.Code, w.Body)
	}

	var got ReportScheduleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Execution.Enabled {
		t.Error("execution.enabled = false with an executor attached")
	}
	if len(got.Execution.Blockers) != 0 {
		t.Errorf("blockers = %v, want none when execution is enabled", got.Execution.Blockers)
	}
	if got.Schedule.Status != ScheduleStatusActive {
		t.Errorf("status = %q, want %q", got.Schedule.Status, ScheduleStatusActive)
	}
	if len(exec.added) != 1 {
		t.Errorf("registered %d schedules with the executor, want 1", len(exec.added))
	}
}

// CORRECTNESS: if registration fails, the caller must hear about it. A 201 for
// a schedule the engine refused is the same silent failure in a new costume.
func TestCreateReportSchedule_RegistrationFailureIsReported(t *testing.T) {
	_, mux := newScheduleTestMux(newFakeRepo(), &recordingExecutor{err: errors.New("cron engine rejected it")})

	w := do(mux, http.MethodPost, "/api/v1/reporting/schedules",
		`{"report_id":"r1","cron_expression":"0 0 9 * * *","recipients":["a@example.com"]}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", w.Code, w.Body)
	}
}

// --- validation -----------------------------------------------------------

// CORRECTNESS: this package's cron engine is built with cron.WithSeconds(), so
// it needs SIX fields. Every crontab example in the world is five. Storing a
// five-field expression would create a schedule that could never register even
// after the scheduler is fixed, so it is rejected at the API with an error
// that names the dialect.
func TestCreateReportSchedule_RejectsUnrunnableCron(t *testing.T) {
	cases := []struct {
		name     string
		expr     string
		wantCode int
	}{
		{"six-field daily at 09:00", "0 0 9 * * *", http.StatusCreated},
		{"six-field weekly", "0 0 9 * * 0", http.StatusCreated},
		{"descriptor", "@daily", http.StatusCreated},
		{"five-field crontab syntax", "0 9 * * *", http.StatusBadRequest},
		{"garbage", "every monday please", http.StatusBadRequest},
		{"out-of-range minute", "0 99 8 * * *", http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			_, mux := newScheduleTestMux(repo, nil)

			w := do(mux, http.MethodPost, "/api/v1/reporting/schedules",
				`{"report_id":"r1","cron_expression":"`+tc.expr+`","recipients":["a@example.com"]}`)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body)
			}
			if tc.wantCode == http.StatusBadRequest {
				if len(repo.schedules) != 0 {
					t.Error("an unrunnable expression was persisted anyway")
				}
				// The 400 body itself cannot explain why: httputil.RespondError
				// replaces every 4xx message with a generic "Bad Request" on
				// purpose. The explanation reaches the operator two other ways,
				// asserted below and in TestScheduleExecution_AdvertisesTheCronDialect.
				if !strings.Contains(ValidateCronExpression(tc.expr).Error(), "six fields") {
					t.Errorf("ValidateCronExpression(%q) does not explain the six-field dialect: %v", tc.expr, ValidateCronExpression(tc.expr))
				}
			}
		})
	}
}

// CORRECTNESS: because the shared 4xx envelope cannot carry a reason, the
// accepted grammar has to be discoverable BEFORE a POST is attempted. A client
// that can only learn the rule by failing will keep failing.
func TestScheduleExecution_AdvertisesTheCronDialect(t *testing.T) {
	_, mux := newScheduleTestMux(newFakeRepo(), nil)

	w := do(mux, http.MethodGet, "/api/v1/reporting/schedules", "")
	var got ReportScheduleListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(got.Execution.CronDialect, "Six fields") {
		t.Errorf("execution.cron_dialect = %q, want it to state the six-field requirement", got.Execution.CronDialect)
	}

	// The shared envelope really does swallow the reason — pinned so that if
	// httputil ever starts forwarding messages, this comment stops being true
	// and someone re-reads the advertisement above.
	bad := do(mux, http.MethodPost, "/api/v1/reporting/schedules",
		`{"report_id":"r1","cron_expression":"0 9 * * *","recipients":["a@example.com"]}`)
	if strings.Contains(bad.Body.String(), "six fields") {
		t.Log("httputil.RespondError now forwards 4xx messages; the cron_dialect advertisement could be simplified")
	}
}

// Whatever ValidateCronExpression accepts, AddSchedule must accept too —
// otherwise the API would happily store expressions the engine later refuses.
func TestValidateCronExpression_MatchesTheEngine(t *testing.T) {
	exprs := []string{"0 0 9 * * *", "*/30 * * * * *", "@daily", "@every 1h", "0 9 * * *", "", "nonsense"}

	for _, expr := range exprs {
		validateErr := ValidateCronExpression(expr)
		sched := NewScheduler(NewService(newFakeRepo()), &fakeSender{})
		addErr := sched.AddSchedule(context.Background(), ReportSchedule{ID: "s1", CronExpression: expr})

		if (validateErr == nil) != (addErr == nil) {
			t.Errorf("ValidateCronExpression(%q) err=%v but AddSchedule err=%v — the API and the engine disagree", expr, validateErr, addErr)
		}
	}
}

func TestCreateReportSchedule_RequiresTheEssentials(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no report_id", `{"cron_expression":"0 0 9 * * *","recipients":["a@example.com"]}`},
		{"no cron_expression", `{"report_id":"r1","recipients":["a@example.com"]}`},
		{"no recipients", `{"report_id":"r1","cron_expression":"0 0 9 * * *","recipients":[]}`},
		{"malformed json", `{"report_id":`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			_, mux := newScheduleTestMux(repo, nil)

			w := do(mux, http.MethodPost, "/api/v1/reporting/schedules", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
			}
			if len(repo.schedules) != 0 {
				t.Error("an invalid schedule was persisted")
			}
		})
	}
}

func TestCreateReportSchedule_FormatHandling(t *testing.T) {
	cases := []struct {
		name     string
		format   string
		wantCode int
		want     string
	}{
		{"omitted defaults to CSV", "", http.StatusCreated, "CSV"},
		{"xlsx", "XLSX", http.StatusCreated, "XLSX"},
		{"pdf", "PDF", http.StatusCreated, "PDF"},
		{"case-insensitive", "csv", http.StatusCreated, "CSV"},
		{"unsupported", "PARQUET", http.StatusBadRequest, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			_, mux := newScheduleTestMux(repo, nil)

			w := do(mux, http.MethodPost, "/api/v1/reporting/schedules",
				`{"report_id":"r1","cron_expression":"0 0 9 * * *","recipients":["a@example.com"],"format":"`+tc.format+`"}`)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body)
			}
			if tc.wantCode != http.StatusCreated {
				return
			}
			if repo.schedules[0].Format != tc.want {
				t.Errorf("persisted format = %q, want %q", repo.schedules[0].Format, tc.want)
			}
		})
	}
}

// --- delete ---------------------------------------------------------------

func TestDeleteReportSchedule(t *testing.T) {
	repo := newFakeRepo()
	repo.schedules = []ReportSchedule{
		{ID: "s1", ReportID: "r1", Status: ScheduleStatusStored},
		{ID: "s2", ReportID: "r2", Status: ScheduleStatusStored},
	}
	_, mux := newScheduleTestMux(repo, nil)

	w := do(mux, http.MethodDelete, "/api/v1/reporting/schedules/s1", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", w.Code, w.Body)
	}
	if len(repo.schedules) != 1 || repo.schedules[0].ID != "s2" {
		t.Errorf("after deleting s1 the table holds %+v, want only s2", repo.schedules)
	}
}

func TestDeleteReportSchedule_RepositoryFailureIs500(t *testing.T) {
	repo := newFakeRepo()
	repo.savedErr = errors.New("db down")
	_, mux := newScheduleTestMux(repo, nil)

	if w := do(mux, http.MethodDelete, "/api/v1/reporting/schedules/s1", ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

// --- running a saved report ----------------------------------------------

// CORRECTNESS, and the direct contrast with the scheduler's known bug:
// definitionFromSaved is the decode step that turns a saved report's untyped
// definition_json into the typed ReportDefinition the query builder needs.
// Scheduler.ExecuteAndSendReport omits exactly this step, which is why every
// scheduled run fails with "no columns selected". The run endpoint performs it.
func TestDefinitionFromSaved_DecodesTheStoredDefinition(t *testing.T) {
	report := &SavedReport{
		ID:         "r1",
		Name:       "AR by customer",
		EntityType: "invoices",
		DefinitionJSON: map[string]any{
			"columns": []any{
				map[string]any{"field": "customer_name", "label": "Customer"},
				map[string]any{"field": "total_amount", "label": "Total", "aggregation": "SUM"},
			},
			"filters":   []any{map[string]any{"field": "status", "operator": "=", "value": "OPEN"}},
			"groupings": []any{map[string]any{"field": "customer_name"}},
		},
	}

	def, err := definitionFromSaved(report)
	if err != nil {
		t.Fatalf("definitionFromSaved: %v", err)
	}
	if len(def.Columns) != 2 {
		t.Fatalf("decoded %d columns, want 2 — an empty definition is what BuildAndExecuteQuery rejects with \"no columns selected\"", len(def.Columns))
	}
	if def.Columns[0].Field != "customer_name" || def.Columns[0].Label != "Customer" {
		t.Errorf("column[0] = %+v, want customer_name/Customer", def.Columns[0])
	}
	if def.Columns[1].Aggregation != "SUM" {
		t.Errorf("column[1].Aggregation = %q, want SUM", def.Columns[1].Aggregation)
	}
	if len(def.Filters) != 1 || def.Filters[0].Operator != "=" {
		t.Errorf("filters = %+v, want the one stored filter", def.Filters)
	}
	if len(def.Groupings) != 1 || def.Groupings[0].Field != "customer_name" {
		t.Errorf("groupings = %+v, want the one stored grouping", def.Groupings)
	}
}

// A report with no stored definition decodes to an empty one rather than
// erroring — the failure surfaces at execution time as "no columns selected",
// which is the accurate message.
func TestDefinitionFromSaved_EmptyDefinitionIsNotAnError(t *testing.T) {
	def, err := definitionFromSaved(&SavedReport{ID: "r1"})
	if err != nil {
		t.Fatalf("definitionFromSaved: %v", err)
	}
	if len(def.Columns) != 0 {
		t.Errorf("columns = %+v, want none", def.Columns)
	}
}

func TestRunSavedReport_MissingReportIs404(t *testing.T) {
	_, mux := newScheduleTestMux(newFakeRepo(), nil)

	if w := do(mux, http.MethodPost, "/api/v1/reporting/saved/gone/run", ""); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body)
	}
}
