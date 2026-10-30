// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package emaildelivery

import (
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

const ordering = "email_deliveries.created_at_id_desc"

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterRoutes mounts GET /api/v1/email-deliveries behind guard (the role
// guard composed with the branch middleware); nil mounts it bare.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	var list http.Handler = http.HandlerFunc(h.List)
	if guard != nil {
		list = guard(list)
	}
	mux.Handle("GET /api/v1/email-deliveries", list)
}

func badKey() error {
	return httpx.BadRequest("cursor keyset is malformed", httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "document_type", "status", "document_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, ordering)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var after *time.Time
	var afterID *uuid.UUID
	if page.Key != nil {
		if len(page.Key) != 2 {
			httpx.WriteError(w, r, badKey())
			return
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			httpx.WriteError(w, r, badKey())
			return
		}
		after, afterID = &at, &id
	}
	v := &httpx.Validator{}
	f := Filter{DocumentType: q.Get("document_type"), Status: q.Get("status")}
	if f.DocumentType != "" {
		v.Check(f.DocumentType == DocInvoice || f.DocumentType == DocStatement || f.DocumentType == DocQuote || f.DocumentType == DocPurchaseOrder,
			"document_type", "must be one of: invoice, statement, quote, purchase_order")
	}
	if f.Status != "" {
		v.Check(f.Status == StatusQueued || f.Status == StatusSent || f.Status == StatusLogged || f.Status == StatusFailed,
			"status", "must be one of: queued, sent, logged, failed")
	}
	if raw := q.Get("document_id"); raw != "" {
		s := raw
		if id, ok := v.UUID("document_id", &s, false); ok {
			f.DocumentID = &id
		}
	}
	wantTotal := false
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "include", "parameter is repeated")
		} else if set, ierr := httpx.ParseInclude(vals[0], httpx.IncludeTotal); ierr != nil {
			httpx.WriteError(w, r, ierr)
			return
		} else {
			wantTotal = set.Has(httpx.IncludeTotal)
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rows, more, err := h.svc.List(r.Context(), f, after, afterID, page.Limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if more && len(rows) > 0 {
		last := rows[len(rows)-1]
		next, err = httpx.MintCursor(ordering, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	opts := []httpx.ListOption{}
	if wantTotal {
		n, err := h.svc.Count(r.Context(), f)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		opts = append(opts, httpx.WithTotal(n))
	}
	httpx.WriteList(w, rows, next, page.Limit, opts...)
}
