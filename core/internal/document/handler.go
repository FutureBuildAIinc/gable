// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/notification"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

type Handler struct {
	docSvc      *Service
	orderSvc    *order.Service
	invoiceSvc  *invoice.Service
	customerSvc *customer.Service
	emailSvc    notification.EmailService
	guard       BranchGuard // optional; see WithBranchWall
}

func NewHandler(d *Service, o *order.Service, i *invoice.Service, c *customer.Service, e notification.EmailService) *Handler {
	return &Handler{docSvc: d, orderSvc: o, invoiceSvc: i, customerSvc: c, emailSvc: e}
}

// BranchGuard holds the record a print or email acts on to the caller's
// branch wall (ADR 0007 section 2.3). *middleware.BranchGuard satisfies it.
type BranchGuard interface {
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
}

// WithBranchWall makes the print and email routes hold the record's own
// branch to the caller's wall. The invoice and order repositories already
// filter their reads on the request's context branch, which is what stops a
// caller with a header; the record check is what stops a bound caller with
// no context branch (its grants, none granted none). Without it that caller
// reaches any branch's record, so serve always sets it.
func (h *Handler) WithBranchWall(g BranchGuard) *Handler {
	h.guard = g
	return h
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

	mux.HandleFunc("GET /api/v1/documents/print/invoice/{id}", guard(h.HandlePrintInvoice))
	mux.HandleFunc("GET /api/v1/documents/print/pickticket/{id}", guard(h.HandlePrintPickTicket))
	mux.HandleFunc("POST /api/v1/invoices/{id}/email", guard(h.HandleEmailInvoice))
}

// recordBranchAllowed holds a loaded record's branch to the caller's wall,
// as the purchase order routes hold the path id's record. A refusal is a 403
// in the legacy error shape these unconverted routes carry; a record of no
// branch is not a crossing of the wall and passes. It reports whether the
// request may proceed.
func (h *Handler) recordBranchAllowed(w http.ResponseWriter, r *http.Request, what string, branch uuid.UUID) bool {
	if h.guard == nil || branch == uuid.Nil {
		return true
	}
	if err := h.guard.CheckPayloadBranch(r.Context(), branch); err != nil {
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httputil.RespondError(w, r, what+" is in a branch this caller may not target", http.StatusForbidden, err)
			return false
		}
		httputil.RespondError(w, r, "branch access lookup failed", http.StatusInternalServerError, err)
		return false
	}
	return true
}

func (h *Handler) HandlePrintInvoice(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "invalid id", http.StatusBadRequest, err)
		return
	}

	inv, err := h.invoiceSvc.GetInvoice(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, r, "invoice not found", http.StatusNotFound, err)
		return
	}
	if !h.recordBranchAllowed(w, r, "invoice", inv.BranchID) {
		return
	}

	cust, err := h.customerSvc.GetCustomer(r.Context(), inv.CustomerID)
	if err != nil {
		httputil.RespondError(w, r, "customer not found", http.StatusNotFound, err)
		return
	}

	pdfBytes, err := h.docSvc.GenerateInvoicePDF(r.Context(), inv, cust)
	if err != nil {
		httputil.RespondError(w, r, "failed to generate invoice PDF", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=invoice.pdf")
	w.Write(pdfBytes)
}

func (h *Handler) HandlePrintPickTicket(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "invalid id", http.StatusBadRequest, err)
		return
	}

	o, err := h.orderSvc.GetOrder(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, r, "order not found", http.StatusNotFound, err)
		return
	}
	if !h.recordBranchAllowed(w, r, "order", o.BranchID) {
		return
	}

	cust, err := h.customerSvc.GetCustomer(r.Context(), o.CustomerID)
	if err != nil {
		httputil.RespondError(w, r, "customer not found", http.StatusNotFound, err)
		return
	}

	pdfBytes, err := h.docSvc.GeneratePickTicketPDF(r.Context(), o, cust)
	if err != nil {
		httputil.RespondError(w, r, "failed to generate pick ticket PDF", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=pickticket.pdf")
	w.Write(pdfBytes)
}

func (h *Handler) HandleEmailInvoice(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "invalid id", http.StatusBadRequest, err)
		return
	}

	inv, err := h.invoiceSvc.GetInvoice(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, r, "invoice not found", http.StatusNotFound, err)
		return
	}
	// Held before anything is generated or queued: a refused caller must not
	// reach the customer read, the PDF or the email dispatch.
	if !h.recordBranchAllowed(w, r, "invoice", inv.BranchID) {
		return
	}

	cust, err := h.customerSvc.GetCustomer(r.Context(), inv.CustomerID)
	if err != nil {
		httputil.RespondError(w, r, "customer not found", http.StatusNotFound, err)
		return
	}

	pdfBytes, err := h.docSvc.GenerateInvoicePDF(r.Context(), inv, cust)
	if err != nil {
		httputil.RespondError(w, r, "failed to generate pdf", http.StatusInternalServerError, err)
		return
	}

	email := cust.EmailOrEmpty()
	if email == "" {
		httputil.RespondError(w, r, "customer has no email address on file", http.StatusBadRequest, nil)
		return
	}
	// Async Email Dispatch
	// L8 Requirement: Do not block HTTP thread on external SMTP calls.
	// This goroutine runs outside the HTTP recovery middleware, so a panic in
	// the email path (e.g. a future real SMTP sender) would crash the whole
	// process — recover here to contain it.
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic sending invoice email", "recover", rec, "invoice_id", id)
			}
		}()
		bgCtx := context.Background()
		if err := h.emailSvc.SendInvoice(bgCtx, email, inv.ID.String(), pdfBytes); err != nil {
			slog.Error("Failed to send invoice email", "error", err, "invoice_id", id)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"queued"}`))
}
