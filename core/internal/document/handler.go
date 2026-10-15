// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/emaildelivery"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/notification"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// defaultSendTimeout bounds one background send, dial to answer.
const defaultSendTimeout = 60 * time.Second

type Handler struct {
	docSvc      *Service
	orderSvc    *order.Service
	invoiceSvc  *invoice.Service
	customerSvc *customer.Service
	emailSvc    notification.EmailService
	guard       BranchGuard // optional; see WithBranchWall

	quoteSvc   *quote.Service          // optional; see WithQuotes
	poSvc      *purchase_order.Service // optional; see WithPurchaseOrders
	deliveries emaildelivery.Recorder  // optional; see WithDeliveries
	sales      SaleInvoices            // optional; see WithSaleReceipts
	sends      sync.WaitGroup

	sendTimeout time.Duration // zero means the package's sendTimeout; tests shorten it
}

// WithQuotes mounts the quote print and email routes.
func (h *Handler) WithQuotes(q *quote.Service) *Handler { h.quoteSvc = q; return h }

// WithPurchaseOrders mounts the purchase order print route.
func (h *Handler) WithPurchaseOrders(p *purchase_order.Service) *Handler { h.poSvc = p; return h }

// WithDeliveries sets the recorder every send writes its delivery row to.
func (h *Handler) WithDeliveries(r emaildelivery.Recorder) *Handler { h.deliveries = r; return h }

// Wait blocks until the sends in flight have finished and recorded.
func (h *Handler) Wait() { h.sends.Wait() }

// WaitContext is Wait bounded by ctx: true when every send in flight has
// recorded, false when ctx ended first. Shutdown calls it so a send in flight
// at SIGTERM finishes and writes its delivery row.
func (h *Handler) WaitContext(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		h.sends.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
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
	mux.HandleFunc("GET /api/v1/documents/print/statement/{id}", guard(h.HandlePrintStatement))
	mux.HandleFunc("POST /api/v1/ar/customers/{id}/statement/email", guard(h.HandleEmailStatement))
	if h.quoteSvc != nil {
		mux.HandleFunc("GET /api/v1/documents/print/quote/{id}", guard(h.HandlePrintQuote))
		mux.HandleFunc("POST /api/v1/quotes/{id}/email", guard(h.HandleEmailQuote))
	}
	if h.poSvc != nil {
		// Purchase orders belong to the purchasing roles, not the selling ones.
		po := guard
		if len(roleGuard) > 1 && roleGuard[1] != nil {
			po = func(handler http.HandlerFunc) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) { roleGuard[1](handler).ServeHTTP(w, r) }
			}
		}
		mux.HandleFunc("GET /api/v1/documents/print/purchase-order/{id}", po(h.HandlePrintPurchaseOrder))
	}
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
	h.printInvoice(w, r, id)
}

// printInvoice answers the invoice's document: the documents route and the
// counter's receipt route render it through this one function.
func (h *Handler) printInvoice(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	inv, err := h.invoiceSvc.GetInvoiceRecord(r.Context(), id)
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
	h.emailInvoice(w, r, id, noEmail)
}

// emailInvoice queues the invoice's email to its customer: the invoice email
// route and the counter's receipt email route send through this one function,
// and differ only in the refusal they answer when the customer has no address.
func (h *Handler) emailInvoice(w http.ResponseWriter, r *http.Request, id uuid.UUID, noAddress func() error) {
	inv, err := h.invoiceSvc.GetInvoiceRecord(r.Context(), id)
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

	// The refusal is in words: the legacy responder below replaces a message
	// with its generic one, so this one answers the wire envelope.
	email := cust.EmailOrEmpty()
	if email == "" {
		httpx.WriteError(w, r, noAddress())
		return
	}
	pdfBytes, err := h.docSvc.GenerateInvoicePDF(r.Context(), inv, cust)
	if err != nil {
		httputil.RespondError(w, r, "failed to generate pdf", http.StatusInternalServerError, err)
		return
	}
	// Async dispatch: the HTTP thread never waits on the mail server. The
	// send, its answer and any error are written to email_deliveries, which is
	// where a failed send is read (GET /api/v1/email-deliveries).
	dealer := h.docSvc.DealerName(r.Context())
	h.dispatch(r.Context(), outgoing{
		to:       email,
		subject:  withDealer("Invoice "+inv.Number, dealer),
		body:     closing("Please find invoice "+inv.Number+" attached.", dealer),
		filename: "invoice-" + inv.Number + ".pdf",
		pdf:      pdfBytes,
		entry: emaildelivery.Entry{
			BranchID: branchPtr(inv.BranchID), DocumentType: emaildelivery.DocInvoice,
			DocumentID: &inv.ID, DocumentNumber: inv.Number,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"queued"}`))
}

// outgoing is one document email a route has validated and rendered.
type outgoing struct {
	to       string
	subject  string
	body     string
	filename string
	pdf      []byte
	entry    emaildelivery.Entry // everything but the recipient, subject and outcome
}

func withDealer(subject, dealer string) string {
	if dealer == "" {
		return subject
	}
	return subject + " from " + dealer
}

func closing(body, dealer string) string {
	if dealer == "" {
		return body
	}
	return body + "\n\n" + dealer
}

func branchPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// dispatch sends in the background on a context that keeps the request's
// values (who sent it) but not its cancellation, then records the delivery
// with the sender's answer or its error. A panic in the send path is
// contained here: this goroutine runs outside the HTTP recovery middleware.
func (h *Handler) dispatch(reqCtx context.Context, o outgoing) {
	ctx := context.WithoutCancel(reqCtx)
	h.sends.Add(1)
	go func() {
		defer h.sends.Done()
		entry := o.entry
		entry.Recipient, entry.Subject = o.to, o.subject
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					entry.Status, entry.Error = emaildelivery.StatusFailed, fmt.Sprintf("panic in the send path: %v", rec)
					slog.Error("panic sending document email", "recover", rec, "document_type", entry.DocumentType)
				}
			}()
			limit := h.sendTimeout
			if limit <= 0 {
				limit = defaultSendTimeout
			}
			sendCtx, cancel := context.WithTimeout(ctx, limit)
			defer cancel()
			answer, err := notification.SendWithAnswer(sendCtx, h.emailSvc, []string{o.to}, o.subject, o.body, o.filename, o.pdf)
			if err != nil {
				entry.Status, entry.Error = emaildelivery.StatusFailed, err.Error()
				slog.Error("Failed to send document email", "error", err, "document_type", entry.DocumentType)
				return
			}
			entry.Status, entry.SenderAnswer = emaildelivery.StatusSent, answer
		}()
		if h.deliveries == nil {
			return
		}
		recCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if _, err := h.deliveries.Record(recCtx, entry); err != nil {
			slog.Error("Failed to record the email delivery", "error", err, "document_type", entry.DocumentType)
		}
	}()
}

// pathUUID parses the path id for the wire envelope routes.
func pathUUID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid id", httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"})
	}
	return id, nil
}

// wallErr holds a loaded record's branch to the caller's wall for the wire
// envelope routes: nil to proceed, otherwise the error to write.
func (h *Handler) wallErr(r *http.Request, what string, branch uuid.UUID) error {
	if h.guard == nil || branch == uuid.Nil {
		return nil
	}
	if err := h.guard.CheckPayloadBranch(r.Context(), branch); err != nil {
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			return httpx.Forbidden(what + " is in a branch this caller may not target")
		}
		return err
	}
	return nil
}

func writePDF(w http.ResponseWriter, filename string, pdf []byte) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename="+filename)
	_, _ = w.Write(pdf)
}

func writeQueued(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "queued"})
}

func noEmail() error {
	return httpx.BadRequest("customer has no email address on file",
		httpx.FieldError{Field: "customer", Message: "has no email address on file; add one on the customer first"})
}

// loadStatement resolves the customer and the statement a route prints or
// emails: the customer (walled by its primary branch), the optional from, to
// and job_id parameters (the AR statement's own), and the statement itself.
func (h *Handler) loadStatement(w http.ResponseWriter, r *http.Request) (*customer.Customer, *account.Statement, bool) {
	q, err := httpx.StrictQuery(r, "from", "to", "job_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return nil, nil, false
	}
	id, err := pathUUID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return nil, nil, false
	}
	v := &httpx.Validator{}
	date := func(name string) time.Time {
		raw := q.Get(name)
		if raw == "" {
			return time.Time{}
		}
		t, perr := time.Parse("2006-01-02", raw)
		if perr != nil || t.Format("2006-01-02") != raw {
			v.Check(false, name, "must be a date, YYYY-MM-DD")
		}
		return t
	}
	sq := account.StatementQuery{CustomerID: id, From: date("from"), To: date("to")}
	if raw := q.Get("job_id"); raw != "" {
		if jid, ok := v.UUID("job_id", &raw, true); ok {
			sq.JobID = &jid
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return nil, nil, false
	}
	cust, err := h.customerSvc.GetCustomer(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, httpx.NotFound("customer not found"))
		return nil, nil, false
	}
	// The customer read above is the customer wall (header, grants and
	// customer_branches, ADR 0007 section 5.7), so a customer the caller's
	// branch shares prints its statement here as it reads on the AR route;
	// the primary branch alone is not the rule.
	if sq.To.IsZero() {
		today, terr := h.docSvc.statements.LocalDate(r.Context(), cust.PrimaryBranchID, h.docSvc.statements.Now())
		if terr != nil {
			httpx.WriteError(w, r, terr)
			return nil, nil, false
		}
		sq.To = today
	}
	if sq.From.IsZero() {
		sq.From = sq.To.AddDate(0, -1, 0)
	}
	if sq.From.After(sq.To) {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "from", Message: "must not be after to"}))
		return nil, nil, false
	}
	st, err := h.docSvc.Statement(r.Context(), sq)
	if err != nil {
		httpx.WriteError(w, r, err)
		return nil, nil, false
	}
	return cust, st, true
}

func (h *Handler) HandlePrintStatement(w http.ResponseWriter, r *http.Request) {
	cust, st, ok := h.loadStatement(w, r)
	if !ok {
		return
	}
	pdf, err := h.docSvc.GenerateStatementPDF(r.Context(), st, cust)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writePDF(w, "statement.pdf", pdf)
}

func (h *Handler) HandleEmailStatement(w http.ResponseWriter, r *http.Request) {
	cust, st, ok := h.loadStatement(w, r)
	if !ok {
		return
	}
	email := cust.EmailOrEmpty()
	if email == "" {
		httpx.WriteError(w, r, noEmail())
		return
	}
	pdf, err := h.docSvc.GenerateStatementPDF(r.Context(), st, cust)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	dealer := h.docSvc.DealerName(r.Context())
	period := h.docSvc.StatementPeriod(r.Context(), st)
	label := "Statement " + period
	h.dispatch(r.Context(), outgoing{
		to: email, subject: withDealer(label, dealer),
		body:     closing("Please find your account statement for "+period+" attached.", dealer),
		filename: "statement-" + cust.AccountNumber + ".pdf", pdf: pdf,
		entry: emaildelivery.Entry{
			BranchID: branchPtr(cust.PrimaryBranchID), DocumentType: emaildelivery.DocStatement,
			DocumentID: &cust.ID, DocumentNumber: cust.AccountNumber,
		},
	})
	writeQueued(w)
}

func (h *Handler) loadQuote(w http.ResponseWriter, r *http.Request) (*quote.Quote, *customer.Customer, bool) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return nil, nil, false
	}
	id, err := pathUUID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return nil, nil, false
	}
	q, err := h.quoteSvc.GetQuote(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return nil, nil, false
	}
	if err := h.wallErr(r, "quote", q.BranchID); err != nil {
		httpx.WriteError(w, r, err)
		return nil, nil, false
	}
	cust, err := h.customerSvc.GetCustomer(r.Context(), q.CustomerID)
	if err != nil {
		httpx.WriteError(w, r, httpx.NotFound("customer not found"))
		return nil, nil, false
	}
	return q, cust, true
}

func (h *Handler) HandlePrintQuote(w http.ResponseWriter, r *http.Request) {
	q, cust, ok := h.loadQuote(w, r)
	if !ok {
		return
	}
	pdf, err := h.docSvc.GenerateQuotePDF(r.Context(), q, cust)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writePDF(w, "quote.pdf", pdf)
}

func (h *Handler) HandleEmailQuote(w http.ResponseWriter, r *http.Request) {
	q, cust, ok := h.loadQuote(w, r)
	if !ok {
		return
	}
	email := cust.EmailOrEmpty()
	if email == "" {
		httpx.WriteError(w, r, noEmail())
		return
	}
	pdf, err := h.docSvc.GenerateQuotePDF(r.Context(), q, cust)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	dealer := h.docSvc.DealerName(r.Context())
	h.dispatch(r.Context(), outgoing{
		to: email, subject: withDealer("Quote "+q.Number, dealer),
		body:     closing("Please find quote "+q.Number+" attached.", dealer),
		filename: "quote-" + q.Number + ".pdf", pdf: pdf,
		entry: emaildelivery.Entry{
			BranchID: branchPtr(q.BranchID), DocumentType: emaildelivery.DocQuote,
			DocumentID: &q.ID, DocumentNumber: q.Number,
		},
	})
	writeQueued(w)
}

func (h *Handler) HandlePrintPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathUUID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	po, err := h.poSvc.GetPOForPrint(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, httpx.NotFound("purchase order not found"))
		return
	}
	if err := h.wallErr(r, "purchase order", po.BranchID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pdf, err := h.docSvc.GeneratePurchaseOrderPDF(r.Context(), po)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writePDF(w, "purchase-order.pdf", pdf)
}
