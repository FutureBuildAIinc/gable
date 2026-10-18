// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// The layouts of the counter receipt route (GET /pos/transactions/{id}/receipt,
// the layout query). The default is the sale's invoice document, as it has
// always printed; thermal80 is the 80mm till receipt.
const (
	LayoutInvoice   = "invoice"
	LayoutThermal80 = "thermal80"
)

// ReceiptSignature is the sealed signature a sale carries: the signer's typed
// name, when it was captured, the seal's hex and the drawn PNG.
type ReceiptSignature struct {
	SignerName string
	SignedAt   time.Time
	SealHex    string
	PNG        []byte
}

// ReceiptPrint is what one print of a sale's receipt knows: which print it is
// (1 is the first), and the signature when one is sealed. A print past the
// first is a reprint and says so.
type ReceiptPrint struct {
	Number    int
	Signature *ReceiptSignature
}

// Reprint reports whether this print is the second or a later one.
func (p ReceiptPrint) Reprint() bool { return p.Number > 1 }

// ReceiptPrints is the counter's side of a print: it counts the print against
// the sale and hands back its facts, and takes the count back when the render
// that followed failed. The lookup is walled like the sale read, so a sale the
// caller cannot read is the missing sale's 404 here too.
type ReceiptPrints interface {
	StartReceiptPrint(ctx context.Context, saleID uuid.UUID) (ReceiptPrint, error)
	CancelReceiptPrint(ctx context.Context, saleID uuid.UUID)
}

// ThermalRenderer renders the 80mm receipt of a sale's invoice for one print.
type ThermalRenderer func(ctx context.Context, invoiceID uuid.UUID, print ReceiptPrint) ([]byte, error)

// ThermalReceipts builds the plain 80mm receipt input for a sale's invoice.
type ThermalReceipts interface {
	ThermalReceipt(ctx context.Context, invoiceID uuid.UUID, print ReceiptPrint) (branch uuid.UUID, receipt ThermalReceipt, err error)
}

// ThermalRendererOf adapts a ThermalReceipts source to the handler's own
// document service (GenerateThermalReceiptPDF).
func (h *Handler) ThermalRendererOf(src ThermalReceipts) ThermalRenderer {
	if src == nil || h.docSvc == nil {
		return nil
	}
	return func(ctx context.Context, invoiceID uuid.UUID, print ReceiptPrint) ([]byte, error) {
		branch, r, err := src.ThermalReceipt(ctx, invoiceID, print)
		if err != nil {
			return nil, err
		}
		return h.docSvc.GenerateThermalReceiptPDF(ctx, branch, r)
	}
}

// WithReceiptPrints supplies the print count and signature lookup.
func (h *Handler) WithReceiptPrints(p ReceiptPrints) *Handler { h.prints = p; return h }

// WithThermalReceipt supplies the 80mm renderer. Without it the thermal80
// layout answers 409 layout_unavailable and counts no print.
func (h *Handler) WithThermalReceipt(r ThermalRenderer) *Handler { h.thermal = r; return h }

// receiptLayout reads the layout query: absent or invoice is the default,
// thermal80 the till receipt, anything else is a 400.
func receiptLayout(w http.ResponseWriter, r *http.Request) (string, bool) {
	q, err := httpx.StrictQuery(r, "layout")
	if err != nil {
		httpx.WriteError(w, r, err)
		return "", false
	}
	switch layout := q.Get("layout"); layout {
	case "", LayoutInvoice:
		return LayoutInvoice, true
	case LayoutThermal80:
		return LayoutThermal80, true
	}
	httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
		httpx.FieldError{Field: "layout", Message: "must be invoice or thermal80"}))
	return "", false
}

// printSaleReceipt prints the sale's receipt in the layout: it counts the
// print first (so racing prints are numbered apart), renders, and takes the
// count back when the render fails. The default layout is the invoice
// document exactly as before.
func (h *Handler) printSaleReceipt(w http.ResponseWriter, r *http.Request, saleID, invoiceID uuid.UUID, layout string) {
	if layout == LayoutThermal80 && (h.thermal == nil || h.prints == nil) {
		msg := "the 80mm receipt layout is not available on this server: print the default layout"
		httpx.WriteError(w, r, &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict, Message: msg,
			Details: []httpx.FieldError{httpx.Blocker("layout_unavailable", msg)}})
		return
	}
	var print ReceiptPrint
	if h.prints != nil {
		p, err := h.prints.StartReceiptPrint(r.Context(), saleID)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		print = p
		w.Header().Set("X-Receipt-Print", strconv.Itoa(print.Number))
	}
	if layout == LayoutInvoice {
		h.printInvoice(w, r, invoiceID)
		return
	}
	pdf, err := h.thermal(r.Context(), invoiceID, print)
	if err != nil {
		h.prints.CancelReceiptPrint(r.Context(), saleID)
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=receipt.pdf")
	_, _ = w.Write(pdf)
}
