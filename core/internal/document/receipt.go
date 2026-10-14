// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// SaleInvoices names the invoice behind a counter sale's receipt. The lookup
// is the counter's own sale read, so a sale the caller cannot read is the
// missing sale's 404 here too; a sale that is not completed or has no invoice
// is a 409 no_receipt.
type SaleInvoices interface {
	ReceiptInvoice(ctx context.Context, saleID uuid.UUID) (uuid.UUID, error)
}

// WithSaleReceipts supplies the sale lookup the counter's receipt routes need.
func (h *Handler) WithSaleReceipts(s SaleInvoices) *Handler { h.sales = s; return h }

// RegisterReceiptRoutes mounts the counter's receipt pair under the guard the
// counter's own routes carry (the POS roles, not the documents roles). It
// mounts nothing until WithSaleReceipts has supplied the sale lookup.
func (h *Handler) RegisterReceiptRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	if h.sales == nil {
		return
	}
	mux.Handle("GET /api/v1/pos/transactions/{id}/receipt", guard(http.HandlerFunc(h.HandlePrintSaleReceipt)))
	mux.Handle("POST /api/v1/pos/transactions/{id}/receipt/email", guard(http.HandlerFunc(h.HandleEmailSaleReceipt)))
}

// HandlePrintSaleReceipt is GET /pos/transactions/{id}/receipt: the sale's
// invoice document, rendered by the documents print route's own function.
func (h *Handler) HandlePrintSaleReceipt(w http.ResponseWriter, r *http.Request) {
	invoiceID, ok := h.saleInvoice(w, r)
	if !ok {
		return
	}
	h.printInvoice(w, r, invoiceID)
}

// HandleEmailSaleReceipt is POST /pos/transactions/{id}/receipt/email: the
// sale's invoice to the account's contact, sent by the invoice email route's
// own function.
func (h *Handler) HandleEmailSaleReceipt(w http.ResponseWriter, r *http.Request) {
	invoiceID, ok := h.saleInvoice(w, r)
	if !ok {
		return
	}
	h.emailInvoice(w, r, invoiceID, noContact)
}

func (h *Handler) saleInvoice(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := pathUUID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return uuid.Nil, false
	}
	invoiceID, err := h.sales.ReceiptInvoice(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return uuid.Nil, false
	}
	return invoiceID, true
}

// noContact is the refusal in words the Sale complete card shows: a walk in
// sale, or an account with no email address on file, has nobody to send to.
func noContact() error {
	const msg = "this sale has no customer email address to send the receipt to; print it, or add an email address to the account"
	return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict, Message: msg,
		Details: []httpx.FieldError{httpx.Blocker("no_contact", msg)}}
}
