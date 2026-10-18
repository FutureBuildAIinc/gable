// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/dealerprofile"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// quotesScope names the list's ordering: created_at then id, newest first.
const quotesScope = "customer_quotes.created_at_id_desc"

// Handler serves the contractor's routes under /api/portal/v1/customer-quotes
// behind the portal chain: every read and write is scoped to the account the
// chain identifies.
type Handler struct {
	svc *Service
}

// NewHandler builds the contractor's handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func fail(w http.ResponseWriter, r *http.Request, err error) { httpx.WriteError(w, r, wireErr(err)) }

func actorOf(r *http.Request) (Actor, error) {
	claims, ok := r.Context().Value(middleware.PortalClaimsKey).(*middleware.PortalClaims)
	if !ok || claims == nil || claims.CustomerID == uuid.Nil {
		return Actor{}, httpx.Unauthorized("the portal chain identifies the account")
	}
	a := Actor{CustomerID: claims.CustomerID, Name: claims.Name, Email: claims.Email, Role: claims.Role}
	if claims.CustomerUserID != uuid.Nil {
		id := claims.CustomerUserID
		a.UserID = &id
	}
	return a, nil
}

func idOf(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid quote id", httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

// contractorBase is the contractor routes' prefix.
const contractorBase = "/api/portal/v1/customer-quotes"

// RegisterRoutes registers the contractor routes behind the portal chain.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, portal func(http.Handler) http.Handler) {
	mux.Handle("GET "+contractorBase, portal(http.HandlerFunc(h.List)))
	mux.Handle("POST "+contractorBase, portal(http.HandlerFunc(h.Create)))
	mux.Handle("GET "+contractorBase+"/export", portal(http.HandlerFunc(h.Export)))
	mux.Handle("GET "+contractorBase+"/settings", portal(http.HandlerFunc(h.GetSettings)))
	mux.Handle("PUT "+contractorBase+"/settings", portal(http.HandlerFunc(h.PutSettings)))
	mux.Handle("PUT "+contractorBase+"/settings/logo", portal(http.HandlerFunc(h.PutLogo)))
	mux.Handle("DELETE "+contractorBase+"/settings/logo", portal(http.HandlerFunc(h.DeleteLogo)))
	mux.Handle("GET "+contractorBase+"/{id}", portal(http.HandlerFunc(h.Get)))
	mux.Handle("PUT "+contractorBase+"/{id}", portal(http.HandlerFunc(h.Update)))
	mux.Handle("GET "+contractorBase+"/{id}/preview", portal(http.HandlerFunc(h.Preview)))
	mux.Handle("GET "+contractorBase+"/{id}/pdf", portal(http.HandlerFunc(h.PDF)))
	mux.Handle("GET "+contractorBase+"/{id}/approvals", portal(http.HandlerFunc(h.Approvals)))
	mux.Handle("POST "+contractorBase+"/{id}/send", portal(http.HandlerFunc(h.Send)))
	mux.Handle("POST "+contractorBase+"/{id}/link/rotate", portal(http.HandlerFunc(h.Rotate)))
	mux.Handle("POST "+contractorBase+"/{id}/link/revoke", portal(http.HandlerFunc(h.Revoke)))
	mux.Handle("POST "+contractorBase+"/{id}/withdraw", portal(http.HandlerFunc(h.Withdraw)))
	mux.Handle("POST "+contractorBase+"/{id}/revise", portal(http.HandlerFunc(h.Revise)))
	mux.Handle("GET "+contractorBase+"/{id}/confirm-preview", portal(http.HandlerFunc(h.ConfirmPreview)))
	mux.Handle("POST "+contractorBase+"/{id}/confirm", portal(http.HandlerFunc(h.Confirm)))
}

func (h *Handler) view(r *http.Request, q *Quote) (*QuoteView, error) {
	link, err := h.svc.LiveLink(r.Context(), q)
	if err != nil {
		return nil, err
	}
	order, err := h.svc.OrderOf(r.Context(), q)
	if err != nil {
		return nil, err
	}
	return BuildQuoteView(q, h.svc.now(), link, order)
}

func (h *Handler) writeQuote(w http.ResponseWriter, r *http.Request, status int, q *Quote) {
	v, err := h.view(r, q)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, q.Revision)
	writeJSON(w, status, v)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	qv, err := httpx.StrictQuery(r, "cursor", "limit", "status")
	if err != nil {
		fail(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, quotesScope)
	if err != nil {
		fail(w, r, err)
		return
	}
	f := ListFilter{Limit: page.Limit}
	if vals := qv["status"]; len(vals) > 0 {
		st := Status(vals[0])
		switch st {
		case StatusDraft, StatusSent, StatusApproved, StatusDeclined, StatusConfirmed, StatusWithdrawn:
			f.Status = &st
		default:
			fail(w, r, httpx.BadRequest("the status is not known", httpx.FieldError{Field: "status", Message: "must be one of: draft, sent, approved, declined, confirmed, withdrawn"}))
			return
		}
	}
	if page.Key != nil {
		if len(page.Key) != 2 {
			fail(w, r, httpx.BadRequest("cursor keyset is malformed", httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"}))
			return
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			fail(w, r, httpx.BadRequest("cursor keyset is malformed", httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"}))
			return
		}
		f.AfterTime, f.AfterID = &at, id
	}
	qs, err := h.svc.List(r.Context(), a, f)
	if err != nil {
		fail(w, r, err)
		return
	}
	next := ""
	if len(qs) > f.Limit {
		qs = qs[:f.Limit]
		last := qs[len(qs)-1]
		if next, err = httpx.MintCursor(quotesScope, httpx.FormatKeyTime(last.CreatedAt), last.ID.String()); err != nil {
			fail(w, r, err)
			return
		}
	}
	items := make([]QuoteSummary, 0, len(qs))
	for _, q := range qs {
		s, err := SummaryOf(q, h.svc.now())
		if err != nil {
			fail(w, r, err)
			return
		}
		items = append(items, s)
	}
	httpx.WriteList(w, items, next, f.Limit)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var in QuoteInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	q, err := h.svc.Create(r.Context(), a, &in)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/portal/v1/customer-quotes/"+q.ID.String())
	h.writeQuote(w, r, http.StatusCreated, q)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err == nil {
		var id uuid.UUID
		if id, err = idOf(r); err == nil {
			var q *Quote
			if q, err = h.svc.Get(r.Context(), a, id); err == nil {
				h.writeQuote(w, r, http.StatusOK, q)
				return
			}
		}
	}
	fail(w, r, err)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var in QuoteInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	q, err := h.svc.Update(r.Context(), a, id, r.Header.Get("If-Match"), &in)
	if err != nil {
		fail(w, r, err)
		return
	}
	h.writeQuote(w, r, http.StatusOK, q)
}

func (h *Handler) Preview(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	v, err := h.svc.Preview(r.Context(), a, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *Handler) PDF(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	b, err := h.svc.ContractorPDF(r.Context(), a, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="quote.pdf"`)
	_, _ = w.Write(b)
}

func (h *Handler) Approvals(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	log, err := h.svc.Approvals(r.Context(), a, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]ApprovalView, 0, len(log))
	for _, e := range log {
		items = append(items, ApprovalViewOf(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// linkBody is the answer to issuing a link. The raw token appears only here.
type linkBody struct {
	Quote *QuoteView `json:"quote"`
	Link  struct {
		Path      string          `json:"path"`
		URL       string          `json:"url,omitempty"`
		ExpiresAt httpx.Timestamp `json:"expires_at"`
		Delivered bool            `json:"delivered"`
	} `json:"link"`
}

func (h *Handler) writeLink(w http.ResponseWriter, r *http.Request, res *LinkResult) {
	v, err := h.view(r, res.Quote)
	if err != nil {
		fail(w, r, err)
		return
	}
	var b linkBody
	b.Quote = v
	b.Link.Path, b.Link.URL, b.Link.ExpiresAt, b.Link.Delivered = res.Path, res.URL, httpx.TimestampOf(res.ExpiresAt), res.Delivered
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, b)
}

func (h *Handler) Send(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	res, err := h.svc.Send(r.Context(), a, id, r.Header.Get("If-Match"))
	if err != nil {
		fail(w, r, err)
		return
	}
	h.writeLink(w, r, res)
}

func (h *Handler) Rotate(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	res, err := h.svc.Rotate(r.Context(), a, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	h.writeLink(w, r, res)
}

func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := h.svc.Revoke(r.Context(), a, id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) transition(w http.ResponseWriter, r *http.Request, fn func(*Handler, *http.Request, Actor, uuid.UUID) (*Quote, error)) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q, err := fn(h, r, a, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	h.writeQuote(w, r, http.StatusOK, q)
}

func (h *Handler) Withdraw(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, func(h *Handler, r *http.Request, a Actor, id uuid.UUID) (*Quote, error) {
		return h.svc.Withdraw(r.Context(), a, id)
	})
}

func (h *Handler) Revise(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, func(h *Handler, r *http.Request, a Actor, id uuid.UUID) (*Quote, error) {
		return h.svc.Revise(r.Context(), a, id)
	})
}

func (h *Handler) ConfirmPreview(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	prev, err := h.svc.ConfirmPreview(r.Context(), a, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, prev)
}

func (h *Handler) Confirm(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, func(h *Handler, r *http.Request, a Actor, id uuid.UUID) (*Quote, error) {
		var in ConfirmInput
		if r.ContentLength != 0 {
			r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
			if err := httpx.DecodeJSON(r, &in); err != nil {
				return nil, err
			}
		}
		return h.svc.Confirm(r.Context(), a, id, in)
	})
}

func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	st, err := h.svc.GetSettings(r.Context(), a)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *Handler) PutSettings(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in SettingsInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	st, err := h.svc.PutSettings(r.Context(), a, in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// PutLogo takes the contractor's mark as the raw body: PNG or JPEG, held to
// the same limits as the dealer's own logo.
func (h *Handler) PutLogo(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	ctype := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if ctype != "image/png" && ctype != "image/jpeg" {
		fail(w, r, httpx.UnsupportedMediaType("the logo must be image/png or image/jpeg"))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, dealerprofile.MaxLogoBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(w, r, httpx.PayloadTooLarge("the logo is larger than the size cap"))
			return
		}
		fail(w, r, httpx.BadRequest("the logo body could not be read"))
		return
	}
	want := map[string]string{"image/png": "png", "image/jpeg": "jpeg"}[ctype]
	if err := dealerprofile.CheckLogoImage(raw, want); err != nil {
		fail(w, r, httpx.BadRequest("the body is not a valid "+want+" image", httpx.FieldError{Field: "body", Message: "must decode as " + ctype}))
		return
	}
	if want == "jpeg" {
		if raw, err = dealerprofile.ReencodeLogoJPEG(raw); err != nil {
			fail(w, r, httpx.BadRequest("the body is not a valid jpeg image", httpx.FieldError{Field: "body", Message: "must decode as " + ctype}))
			return
		}
	}
	if err := h.svc.PutLogo(r.Context(), a, &Logo{Bytes: raw, ContentType: ctype}); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DeleteLogo(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := h.svc.PutLogo(r.Context(), a, nil); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Export is the one click export: JSON of everything, or one table as CSV.
func (h *Handler) Export(w http.ResponseWriter, r *http.Request) {
	a, err := actorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	qv, err := httpx.StrictQuery(r, "format", "kind")
	if err != nil {
		fail(w, r, err)
		return
	}
	format, kind := "json", "quotes"
	if v := qv["format"]; len(v) > 0 {
		format = v[0]
	}
	if v := qv["kind"]; len(v) > 0 {
		kind = v[0]
	}
	if format != "json" && format != "csv" {
		fail(w, r, httpx.BadRequest("the format is not known", httpx.FieldError{Field: "format", Message: "must be one of: json, csv"}))
		return
	}
	ex, err := h.svc.Export(r.Context(), a)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	stamp := time.Now().UTC().Format("20060102")
	if format == "json" {
		w.Header().Set("Content-Disposition", `attachment; filename="customer-quotes-`+stamp+`.json"`)
		writeJSON(w, http.StatusOK, ex)
		return
	}
	var buf strings.Builder
	if err := ex.WriteCSV(&buf, kind); err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="customer-`+kind+`-`+stamp+`.csv"`)
	_, _ = io.WriteString(w, buf.String())
}
