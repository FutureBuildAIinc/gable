// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// ScheduleExecutor is the seam a working scheduled-report runner plugs into.
// *Scheduler satisfies it, but nothing in this repository attaches one — see
// Handler.WithScheduleExecutor and the Scheduler doc comment in scheduler.go.
type ScheduleExecutor interface {
	AddSchedule(ctx context.Context, schedule ReportSchedule) error
}

type Handler struct {
	service *Service

	// scheduleExecutor is nil in every current deployment. Its nil-ness is the
	// single source of truth for whether the schedule endpoints may claim that
	// stored schedules actually run; see Handler.execution.
	scheduleExecutor ScheduleExecutor
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// WithScheduleExecutor attaches a runner for stored schedules.
//
// This is the ONLY switch that makes the schedule endpoints report
// execution.enabled = true and create schedules with status ACTIVE. Do not
// call it with a Scheduler until the two bugs in scheduler.go are fixed
// (ExecuteAndSendReport drops the saved report definition, and no EmailSender
// implementation exists) — attaching a broken executor would restore exactly
// the silent failure this plumbing exists to prevent.
func (h *Handler) WithScheduleExecutor(e ScheduleExecutor) *Handler {
	h.scheduleExecutor = e
	return h
}

// execution reports, truthfully, whether stored schedules run in this process.
func (h *Handler) execution() ScheduleExecution {
	if h.scheduleExecutor == nil {
		return scheduleExecutionDisabled()
	}
	return scheduleExecutionEnabled()
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("GET /api/v1/reports/daily-till", guard(h.HandleDailyTill))
	mux.HandleFunc("GET /api/v1/reports/sales-summary", guard(h.HandleSalesSummary))
	mux.HandleFunc("GET /api/v1/reports/ar-aging", guard(h.HandleARAgingReport))
	mux.HandleFunc("GET /api/v1/reports/customer-statement/{id}", guard(h.HandleCustomerStatement))
}

func (h *Handler) HandleDailyTill(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	report, err := h.service.GetDailyTill(r.Context(), dateStr)
	if err != nil {
		httputil.RespondError(w, r, "failed to get daily till report", http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

func (h *Handler) HandleSalesSummary(w http.ResponseWriter, r *http.Request) {
	start := r.URL.Query().Get("start")
	end := r.URL.Query().Get("end")
	report, err := h.service.GetSalesSummary(r.Context(), start, end)
	if err != nil {
		httputil.RespondError(w, r, "failed to get sales summary report", http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

func (h *Handler) HandleARAgingReport(w http.ResponseWriter, r *http.Request) {
	report, err := h.service.GetARAgingReport(r.Context())
	if err != nil {
		httputil.RespondError(w, r, "failed to get AR aging report", http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

func (h *Handler) HandleCustomerStatement(w http.ResponseWriter, r *http.Request) {
	customerID := r.PathValue("id")
	if customerID == "" {
		httputil.RespondError(w, r, "customer ID required", http.StatusBadRequest, nil)
		return
	}
	if _, err := uuid.Parse(customerID); err != nil {
		httputil.RespondError(w, r, "invalid customer ID format", http.StatusBadRequest, err)
		return
	}
	start := r.URL.Query().Get("start")
	end := r.URL.Query().Get("end")
	stmt, err := h.service.GetCustomerStatement(r.Context(), customerID, start, end)
	if err != nil {
		httputil.RespondError(w, r, "failed to get customer statement", http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stmt)
}

func (h *Handler) RegisterBuilderRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("POST /api/v1/reporting/builder/preview", guard(h.HandleBuilderPreview))
	mux.HandleFunc("POST /api/v1/reporting/builder/export", guard(h.HandleBuilderExport))
	mux.HandleFunc("POST /api/v1/reporting/save", guard(h.HandleSaveReport))
	mux.HandleFunc("GET /api/v1/reporting/saved", guard(h.HandleListSavedReports))
	mux.HandleFunc("GET /api/v1/reporting/saved/{id}", guard(h.HandleGetSavedReport))
	mux.HandleFunc("PUT /api/v1/reporting/saved/{id}", guard(h.HandleUpdateSavedReport))
	mux.HandleFunc("DELETE /api/v1/reporting/saved/{id}", guard(h.HandleDeleteSavedReport))
	mux.HandleFunc("POST /api/v1/reporting/saved/{id}/run", guard(h.HandleRunSavedReport))
	mux.HandleFunc("POST /api/v1/reporting/schedules", guard(h.HandleCreateReportSchedule))
	mux.HandleFunc("GET /api/v1/reporting/schedules", guard(h.HandleListReportSchedules))
	mux.HandleFunc("DELETE /api/v1/reporting/schedules/{id}", guard(h.HandleDeleteReportSchedule))
}

func (h *Handler) HandleBuilderPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntityType string           `json:"entity_type"`
		Definition ReportDefinition `json:"definition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "failed to decode report preview request", http.StatusBadRequest, err)
		return
	}

	results, err := h.service.ExecuteReportDefinition(r.Context(), &req.Definition, req.EntityType)
	if err != nil {
		httputil.RespondError(w, r, "failed to execute report preview", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func (h *Handler) HandleBuilderExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntityType string           `json:"entity_type"`
		Format     string           `json:"format"` // csv, xlsx
		Definition ReportDefinition `json:"definition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "failed to decode report export request", http.StatusBadRequest, err)
		return
	}

	results, err := h.service.ExecuteReportDefinition(r.Context(), &req.Definition, req.EntityType)
	if err != nil {
		httputil.RespondError(w, r, "failed to execute report export", http.StatusInternalServerError, err)
		return
	}

	var buf bytes.Buffer
	var contentType, disposition string

	switch req.Format {
	case "csv":
		contentType = "text/csv"
		disposition = `attachment; filename="report.csv"`
		if err := ExportCSV(&buf, req.Definition.Columns, results); err != nil {
			httputil.RespondError(w, r, "failed to export CSV", http.StatusInternalServerError, err)
			return
		}
	case "xlsx":
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		disposition = `attachment; filename="report.xlsx"`
		if err := ExportXLSX(&buf, req.Definition.Columns, results); err != nil {
			httputil.RespondError(w, r, "failed to export XLSX", http.StatusInternalServerError, err)
			return
		}
	default:
		httputil.RespondError(w, r, "unsupported format", http.StatusBadRequest, nil)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disposition)
	io.Copy(w, &buf)
}

func (h *Handler) HandleSaveReport(w http.ResponseWriter, r *http.Request) {
	var report SavedReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		httputil.RespondError(w, r, "failed to decode save report request", http.StatusBadRequest, err)
		return
	}

	// Extract authenticated user identity from JWT claims
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil && claims.Subject != "" {
		report.CreatedBy = claims.Subject
	} else {
		report.CreatedBy = "system"
	}

	if err := h.service.CreateSavedReport(r.Context(), &report); err != nil {
		httputil.RespondError(w, r, "failed to save report", http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

func (h *Handler) HandleListSavedReports(w http.ResponseWriter, r *http.Request) {
	reports, err := h.service.ListSavedReports(r.Context())
	if err != nil {
		httputil.RespondError(w, r, "failed to list saved reports", http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reports)
}

func (h *Handler) HandleGetSavedReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	report, err := h.service.GetSavedReport(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, r, "failed to get saved report", http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

func (h *Handler) HandleUpdateSavedReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var report SavedReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		httputil.RespondError(w, r, "failed to decode update report request", http.StatusBadRequest, err)
		return
	}
	report.ID = id

	if err := h.service.UpdateSavedReport(r.Context(), &report); err != nil {
		httputil.RespondError(w, r, "failed to update saved report", http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) HandleDeleteSavedReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.service.DeleteSavedReport(r.Context(), id); err != nil {
		httputil.RespondError(w, r, "failed to delete saved report", http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- saved report execution ----------------------------------------------

// HandleRunSavedReport handles POST /reporting/saved/{id}/run — executes a
// saved report right now and returns its rows.
//
// Note what this does that the scheduler does not: it decodes the saved
// report's definition_json into a ReportDefinition before executing it. That
// decode is the exact step Scheduler.ExecuteAndSendReport omits, which is why
// every scheduled run produces "no columns selected" while this endpoint
// works. If the scheduler is ever fixed, this is the code it should share.
func (h *Handler) HandleRunSavedReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httputil.RespondError(w, r, "saved report ID required", http.StatusBadRequest, nil)
		return
	}

	report, err := h.service.GetSavedReport(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, r, "failed to get saved report", http.StatusNotFound, err)
		return
	}
	if report == nil {
		httputil.RespondError(w, r, "saved report not found", http.StatusNotFound, nil)
		return
	}

	def, err := definitionFromSaved(report)
	if err != nil {
		httputil.RespondError(w, r, "failed to parse report definition", http.StatusInternalServerError, err)
		return
	}

	results, err := h.service.ExecuteReportDefinition(r.Context(), def, report.EntityType)
	if err != nil {
		httputil.RespondError(w, r, fmt.Sprintf("failed to execute saved report: %s", report.Name), http.StatusInternalServerError, err)
		return
	}
	if results == nil {
		results = []map[string]interface{}{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// definitionFromSaved decodes a SavedReport's untyped definition_json into the
// typed ReportDefinition the query builder needs.
func definitionFromSaved(report *SavedReport) (*ReportDefinition, error) {
	defBytes, err := json.Marshal(report.DefinitionJSON)
	if err != nil {
		return nil, fmt.Errorf("marshal report definition: %w", err)
	}
	var def ReportDefinition
	if err := json.Unmarshal(defBytes, &def); err != nil {
		return nil, fmt.Errorf("parse report definition: %w", err)
	}
	return &def, nil
}

// --- schedules ------------------------------------------------------------

// HandleCreateReportSchedule handles POST /reporting/schedules.
//
// The schedule is persisted, and the response says plainly whether anything
// will ever run it. With no executor attached — the state of every deployment
// today — the row is written with status STORED and the response carries
// execution.enabled = false plus the list of blockers. Returning a bare 201
// here would let an operator believe their controller is getting a report
// every Monday morning when nothing will ever fire.
func (h *Handler) HandleCreateReportSchedule(w http.ResponseWriter, r *http.Request) {
	var schedule ReportSchedule
	if err := json.NewDecoder(r.Body).Decode(&schedule); err != nil {
		httputil.RespondError(w, r, "failed to decode create report schedule request", http.StatusBadRequest, err)
		return
	}

	if schedule.ReportID == "" || schedule.CronExpression == "" || len(schedule.Recipients) == 0 {
		httputil.RespondError(w, r, "report_id, cron_expression, and recipients are required", http.StatusBadRequest, nil)
		return
	}

	format, err := normalizeScheduleFormat(schedule.Format)
	if err != nil {
		httputil.RespondError(w, r, err.Error(), http.StatusBadRequest, nil)
		return
	}
	schedule.Format = format

	// Reject an expression the engine could never register. Storing one would
	// create a schedule that is silently unrunnable even after the scheduler
	// is fixed, and the six-field dialect is surprising enough that the error
	// has to name it.
	if err := ValidateCronExpression(schedule.CronExpression); err != nil {
		httputil.RespondError(w, r, err.Error(), http.StatusBadRequest, err)
		return
	}

	if h.scheduleExecutor != nil {
		schedule.Status = ScheduleStatusActive
	} else {
		schedule.Status = ScheduleStatusStored
	}

	if err := h.service.CreateReportSchedule(r.Context(), &schedule); err != nil {
		httputil.RespondError(w, r, "failed to create report schedule", http.StatusInternalServerError, err)
		return
	}

	if h.scheduleExecutor != nil {
		if err := h.scheduleExecutor.AddSchedule(r.Context(), schedule); err != nil {
			httputil.RespondError(w, r, "schedule was saved but could not be registered with the scheduler", http.StatusInternalServerError, err)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(ReportScheduleResponse{
		Schedule:  schedule,
		Execution: h.execution(),
	})
}

// HandleListReportSchedules handles GET /reporting/schedules. The execution
// block travels with the list for the same reason it travels with the create
// response: a UI rendering these rows must be able to say whether they run.
func (h *Handler) HandleListReportSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := h.service.ListReportSchedules(r.Context())
	if err != nil {
		httputil.RespondError(w, r, "failed to list report schedules", http.StatusInternalServerError, err)
		return
	}
	if schedules == nil {
		schedules = []ReportSchedule{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ReportScheduleListResponse{
		Schedules: schedules,
		Execution: h.execution(),
	})
}

// HandleDeleteReportSchedule handles DELETE /reporting/schedules/{id}.
func (h *Handler) HandleDeleteReportSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httputil.RespondError(w, r, "schedule ID required", http.StatusBadRequest, nil)
		return
	}

	if err := h.service.DeleteReportSchedule(r.Context(), id); err != nil {
		httputil.RespondError(w, r, "failed to delete report schedule", http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// scheduleFormats are the output formats a schedule may request. Only CSV has
// an implemented rendering path in the (unwired) scheduler; XLSX and PDF are
// accepted because the export endpoints support them and a schedule is only a
// stored preference today.
var scheduleFormats = map[string]string{"CSV": "CSV", "XLSX": "XLSX", "PDF": "PDF"}

func normalizeScheduleFormat(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "CSV", nil
	}
	if f, ok := scheduleFormats[strings.ToUpper(strings.TrimSpace(raw))]; ok {
		return f, nil
	}
	return "", fmt.Errorf("unsupported format %q: expected CSV, XLSX or PDF", raw)
}

func (h *Handler) RegisterBIIntegrationRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("GET /api/v1/reporting/export/{entity}", guard(h.HandleBIEntityExport))
}

func (h *Handler) HandleBIEntityExport(w http.ResponseWriter, r *http.Request) {
	entity := r.PathValue("entity")

	// Validate entity via existing schemata from builder
	_, ok := entitySchemas[entity]
	if !ok {
		httputil.RespondError(w, r, "Invalid entity requested for BI export", http.StatusBadRequest, nil)
		return
	}

	// Create a "SELECT *" equivalent definition for the BI tool
	def := &ReportDefinition{
		Columns: []ReportColumn{},
	}

	for fieldName := range entitySchemas[entity] {
		def.Columns = append(def.Columns, ReportColumn{
			Field: fieldName,
			Label: fieldName,
		})
	}

	// Fetch raw data
	results, err := h.service.ExecuteReportDefinition(r.Context(), def, entity)
	if err != nil {
		httputil.RespondError(w, r, "failed to execute BI entity export", http.StatusInternalServerError, err)
		return
	}

	// Output as structured JSON dump
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(results); err != nil {
		httputil.RespondError(w, r, "Failed to encode BI output", http.StatusInternalServerError, err)
	}
}
