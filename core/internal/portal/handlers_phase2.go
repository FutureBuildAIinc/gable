// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/google/uuid"
)

func requireAnyCapability(w http.ResponseWriter, r *http.Request, capabilities ...string) bool {
	claims := portalClaims(r)
	if claims != nil {
		caps := CapabilitiesFor(claims.Role)
		for _, c := range capabilities {
			if caps.Has(c) {
				return true
			}
		}
	}
	httputil.RespondError(w, r, "Your role cannot do this", http.StatusForbidden, nil)
	return false
}

func (h *Handler) registerPhase2Routes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	// Part 2 (PE-05): Quote intake (pasted list or photo) and quote-to-order conversion.
	mux.Handle("POST /api/portal/v1/quotes/intake", authMw(http.HandlerFunc(h.HandleQuoteIntake)))
	mux.Handle("POST /api/portal/v1/quotes/{id}/convert", authMw(http.HandlerFunc(h.HandleConvertQuote)))

	// Part 3 (PE-03): Reorder price-difference preview and named material lists (procurement board).
	mux.Handle("GET /api/portal/v1/orders/{id}/reorder-preview", authMw(http.HandlerFunc(h.HandleReorderPreview)))
	mux.Handle("GET /api/portal/v1/procurement/projects", authMw(http.HandlerFunc(h.HandleListProcurementProjects)))
	mux.Handle("POST /api/portal/v1/procurement/projects", authMw(http.HandlerFunc(h.HandleCreateProcurementProject)))
	mux.Handle("GET /api/portal/v1/procurement/projects/{id}", authMw(http.HandlerFunc(h.HandleGetProcurementProject)))
	mux.Handle("PUT /api/portal/v1/procurement/projects/{id}", authMw(http.HandlerFunc(h.HandleUpdateProcurementProject)))
	mux.Handle("DELETE /api/portal/v1/procurement/projects/{id}", authMw(http.HandlerFunc(h.HandleDeleteProcurementProject)))
	mux.Handle("POST /api/portal/v1/procurement/projects/{id}/lines", authMw(http.HandlerFunc(h.HandleAddProcurementLine)))
	mux.Handle("PUT /api/portal/v1/procurement/projects/{id}/lines/{lineId}", authMw(http.HandlerFunc(h.HandleUpdateProcurementLine)))
	mux.Handle("DELETE /api/portal/v1/procurement/projects/{id}/lines/{lineId}", authMw(http.HandlerFunc(h.HandleDeleteProcurementLine)))
	mux.Handle("POST /api/portal/v1/procurement/projects/{id}/submit-rfq", authMw(http.HandlerFunc(h.HandleSubmitProcurementRFQ)))

	// Part 5 (PE-01, PE-04): Billing summary, statements, aging, job summaries, and PDFs (see_money).
	mux.Handle("GET /api/portal/v1/billing/summary", authMw(http.HandlerFunc(h.HandleBillingSummary)))
	mux.Handle("GET /api/portal/v1/statement", authMw(http.HandlerFunc(h.HandleGetStatement)))
	mux.Handle("GET /api/portal/v1/statements", authMw(http.HandlerFunc(h.HandleListStatements)))
	mux.Handle("GET /api/portal/v1/invoices/{id}/pdf", authMw(http.HandlerFunc(h.HandleInvoicePDF)))
	mux.Handle("GET /api/portal/v1/statement/pdf", authMw(http.HandlerFunc(h.HandleStatementPDF)))
	mux.Handle("GET /api/portal/v1/statements/{id}/pdf", authMw(http.HandlerFunc(h.HandleStatementRunItemPDF)))

	// Part 6 (PE-02): Saved payment methods (tokens only), single-flight payment, and allocation history.
	mux.Handle("GET /api/portal/v1/payment-methods", authMw(http.HandlerFunc(h.HandleListPaymentMethods)))
	mux.Handle("POST /api/portal/v1/payment-methods", authMw(http.HandlerFunc(h.HandleCreatePaymentMethod)))
	mux.Handle("DELETE /api/portal/v1/payment-methods/{id}", authMw(http.HandlerFunc(h.HandleDeletePaymentMethod)))
	mux.Handle("PUT /api/portal/v1/payment-methods/{id}/default", authMw(http.HandlerFunc(h.HandleSetDefaultPaymentMethod)))
	mux.Handle("GET /api/portal/v1/payments", authMw(http.HandlerFunc(h.HandleListPayments)))
	mux.Handle("POST /api/portal/v1/payments", authMw(http.HandlerFunc(h.HandleCreatePayment)))
}

// --- Part 2 Handlers --------------------------------------------------------

func (h *Handler) HandleQuoteIntake(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapRequestQuotes) {
		return
	}
	customerID := getPortalCustomerID(r)

	var req QuoteIntakeRequest
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			httputil.RespondError(w, r, "Invalid multipart upload", http.StatusBadRequest, err)
			return
		}
		req.Text = r.FormValue("text")
		req.Notes = r.FormValue("notes")
		req.DeliveryType = r.FormValue("delivery_type")
		req.CreateQuote = strings.EqualFold(r.FormValue("create_quote"), "true")
		req.AllowSynthetic = strings.EqualFold(r.FormValue("allow_synthetic"), "true")
		if rawProj := r.FormValue("project_id"); rawProj != "" {
			pid, err := uuid.Parse(rawProj)
			if err != nil {
				httputil.RespondError(w, r, "Invalid project_id", http.StatusBadRequest, err)
				return
			}
			req.ProjectID = &pid
		}
		file, header, err := r.FormFile("file")
		if err == nil {
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil {
				httputil.RespondError(w, r, "Failed to read uploaded file", http.StatusBadRequest, err)
				return
			}
			req.FileBytes = data
			req.ContentType = header.Header.Get("Content-Type")
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
			return
		}
	}

	resp, err := h.svc.IntakeQuote(r.Context(), customerID, req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	if req.CreateQuote && resp.Quote != nil {
		portalWriteJSONStatus(w, http.StatusCreated, resp)
		return
	}
	portalWriteJSON(w, resp)
}

func (h *Handler) HandleConvertQuote(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapOrder) {
		return
	}
	customerID := getPortalCustomerID(r)
	quoteID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid quote ID", http.StatusBadRequest, err)
		return
	}

	resp, err := h.svc.ConvertPortalQuote(r.Context(), quoteID, customerID)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSONStatus(w, http.StatusCreated, resp)
}

// --- Part 3 Handlers --------------------------------------------------------

func (h *Handler) HandleReorderPreview(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapOrder) {
		return
	}
	customerID := getPortalCustomerID(r)
	orderID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid order ID", http.StatusBadRequest, err)
		return
	}

	preview, err := h.svc.PreviewReorder(r.Context(), customerID, orderID)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, preview)
}

func (h *Handler) HandleListProcurementProjects(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	projects, err := h.svc.ListProcurementProjects(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to list material lists", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, projects)
}

func (h *Handler) HandleCreateProcurementProject(w http.ResponseWriter, r *http.Request) {
	if !requireAnyCapability(w, r, CapRequestQuotes, CapOrder) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	claims := portalClaims(r)
	customerID := getPortalCustomerID(r)
	var createdBy *uuid.UUID
	if claims != nil && claims.CustomerUserID != uuid.Nil {
		id := claims.CustomerUserID
		createdBy = &id
	}

	var req CreateProcurementProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}

	p, err := h.svc.CreateProcurementProject(r.Context(), customerID, createdBy, req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSONStatus(w, http.StatusCreated, p)
}

func (h *Handler) HandleGetProcurementProject(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	projectID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid material list ID", http.StatusBadRequest, err)
		return
	}
	p, err := h.svc.GetProcurementProject(r.Context(), customerID, projectID)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, p)
}

func (h *Handler) HandleUpdateProcurementProject(w http.ResponseWriter, r *http.Request) {
	if !requireAnyCapability(w, r, CapRequestQuotes, CapOrder) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)
	projectID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid material list ID", http.StatusBadRequest, err)
		return
	}
	var req UpdateProcurementProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}
	p, err := h.svc.UpdateProcurementProject(r.Context(), customerID, projectID, req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, p)
}

func (h *Handler) HandleDeleteProcurementProject(w http.ResponseWriter, r *http.Request) {
	if !requireAnyCapability(w, r, CapRequestQuotes, CapOrder) {
		return
	}
	customerID := getPortalCustomerID(r)
	projectID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid material list ID", http.StatusBadRequest, err)
		return
	}
	if err := h.svc.DeleteProcurementProject(r.Context(), customerID, projectID); err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) HandleAddProcurementLine(w http.ResponseWriter, r *http.Request) {
	if !requireAnyCapability(w, r, CapRequestQuotes, CapOrder) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)
	projectID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid material list ID", http.StatusBadRequest, err)
		return
	}
	var req UpsertProcurementLineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}
	p, err := h.svc.AddProcurementLine(r.Context(), customerID, projectID, req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSONStatus(w, http.StatusCreated, p)
}

func (h *Handler) HandleUpdateProcurementLine(w http.ResponseWriter, r *http.Request) {
	if !requireAnyCapability(w, r, CapRequestQuotes, CapOrder) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)
	projectID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid material list ID", http.StatusBadRequest, err)
		return
	}
	lineID, err := uuid.Parse(r.PathValue("lineId"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid line ID", http.StatusBadRequest, err)
		return
	}
	var req UpsertProcurementLineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}
	p, err := h.svc.UpdateProcurementLine(r.Context(), customerID, projectID, lineID, req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, p)
}

func (h *Handler) HandleDeleteProcurementLine(w http.ResponseWriter, r *http.Request) {
	if !requireAnyCapability(w, r, CapRequestQuotes, CapOrder) {
		return
	}
	customerID := getPortalCustomerID(r)
	projectID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid material list ID", http.StatusBadRequest, err)
		return
	}
	lineID, err := uuid.Parse(r.PathValue("lineId"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid line ID", http.StatusBadRequest, err)
		return
	}
	p, err := h.svc.DeleteProcurementLine(r.Context(), customerID, projectID, lineID)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, p)
}

func (h *Handler) HandleSubmitProcurementRFQ(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapRequestQuotes) {
		return
	}
	customerID := getPortalCustomerID(r)
	projectID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid material list ID", http.StatusBadRequest, err)
		return
	}
	var req SubmitProcurementRFQRequest
	if r.Body != nil && r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
			return
		}
	}
	p, err := h.svc.SubmitProcurementRFQ(r.Context(), customerID, projectID, req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSONStatus(w, http.StatusCreated, p)
}

// --- Part 5 Handlers (see_money) --------------------------------------------

func parseStatementPeriod(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	to := now
	if raw := r.URL.Query().Get("to"); raw != "" {
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		to = t
	}
	from := to.AddDate(0, 0, -30)
	if raw := r.URL.Query().Get("from"); raw != "" {
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		from = t
	}
	if from.After(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("from must not be after to")
	}
	return from, to, nil
}

func (h *Handler) HandleBillingSummary(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapSeeMoney) {
		return
	}
	customerID := getPortalCustomerID(r)
	sum, err := h.svc.GetBillingSummary(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to load billing summary", err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, sum)
}

func (h *Handler) HandleGetStatement(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapSeeMoney) {
		return
	}
	customerID := getPortalCustomerID(r)
	from, to, err := parseStatementPeriod(r)
	if err != nil {
		httputil.RespondError(w, r, "Invalid date format; expected YYYY-MM-DD", http.StatusBadRequest, err)
		return
	}
	st, err := h.svc.GetLiveStatement(r.Context(), customerID, from, to)
	if err != nil {
		portalWriteError(w, r, "Failed to load statement", err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, st)
}

func (h *Handler) HandleListStatements(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapSeeMoney) {
		return
	}
	customerID := getPortalCustomerID(r)
	runs, err := h.svc.ListStatementRuns(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to list statements", err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, runs)
}

func writePDFResponse(w http.ResponseWriter, pdf []byte, filename string) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

func (h *Handler) HandleInvoicePDF(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapSeeMoney) {
		return
	}
	customerID := getPortalCustomerID(r)
	invoiceID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid invoice ID", http.StatusBadRequest, err)
		return
	}
	pdf, filename, err := h.svc.RenderInvoicePDF(r.Context(), customerID, invoiceID)
	if err != nil {
		portalWriteError(w, r, "Invoice not found", err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	writePDFResponse(w, pdf, filename)
}

func (h *Handler) HandleStatementPDF(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapSeeMoney) {
		return
	}
	customerID := getPortalCustomerID(r)
	from, to, err := parseStatementPeriod(r)
	if err != nil {
		httputil.RespondError(w, r, "Invalid date format; expected YYYY-MM-DD", http.StatusBadRequest, err)
		return
	}
	pdf, filename, err := h.svc.RenderStatementPDF(r.Context(), customerID, from, to)
	if err != nil {
		portalWriteError(w, r, "Failed to generate statement PDF", err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	writePDFResponse(w, pdf, filename)
}

func (h *Handler) HandleStatementRunItemPDF(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapSeeMoney) {
		return
	}
	customerID := getPortalCustomerID(r)
	itemID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid statement ID", http.StatusBadRequest, err)
		return
	}
	pdf, filename, err := h.svc.RenderStatementRunItemPDF(r.Context(), customerID, itemID)
	if err != nil {
		portalWriteError(w, r, "Statement not found", err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	writePDFResponse(w, pdf, filename)
}

// --- Part 6 Handlers (pay / see_money) --------------------------------------

func (h *Handler) HandleListPaymentMethods(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapPay) {
		return
	}
	customerID := getPortalCustomerID(r)
	methods, err := h.svc.ListPaymentMethods(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to list payment methods", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, methods)
}

func (h *Handler) HandleCreatePaymentMethod(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapPay) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}
	var req CreatePortalPaymentMethodRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}

	claims := portalClaims(r)
	customerID := getPortalCustomerID(r)
	var createdBy *uuid.UUID
	if claims != nil && claims.CustomerUserID != uuid.Nil {
		id := claims.CustomerUserID
		createdBy = &id
	}

	pm, err := h.svc.CreatePaymentMethod(r.Context(), customerID, createdBy, rawBody, req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSONStatus(w, http.StatusCreated, pm)
}

func (h *Handler) HandleDeletePaymentMethod(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapPay) {
		return
	}
	customerID := getPortalCustomerID(r)
	methodID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid payment method ID", http.StatusBadRequest, err)
		return
	}
	if err := h.svc.DeletePaymentMethod(r.Context(), customerID, methodID); err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) HandleSetDefaultPaymentMethod(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapPay) {
		return
	}
	customerID := getPortalCustomerID(r)
	methodID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid payment method ID", http.StatusBadRequest, err)
		return
	}
	pm, err := h.svc.SetDefaultPaymentMethod(r.Context(), customerID, methodID)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, pm)
}

func (h *Handler) HandleListPayments(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapSeeMoney) {
		return
	}
	customerID := getPortalCustomerID(r)
	list, err := h.svc.ListPortalPayments(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to list payments", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, list)
}

func (h *Handler) HandleCreatePayment(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapPay) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}
	var req CreatePortalPaymentRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = r.Header.Get("Idempotency-Key")
	}

	claims := portalClaims(r)
	customerID := getPortalCustomerID(r)
	actor := "portal:" + customerID.String()
	if claims != nil && claims.CustomerUserID != uuid.Nil {
		actor = "portal_user:" + claims.CustomerUserID.String()
	}

	dto, replayed, err := h.svc.PayInvoices(r.Context(), customerID, actor, rawBody, req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	if replayed {
		portalWriteJSONStatus(w, http.StatusOK, dto)
		return
	}
	portalWriteJSONStatus(w, http.StatusCreated, dto)
}
