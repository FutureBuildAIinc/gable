// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/clientip"
)

// PublicHandler serves the capability link's routes under
// /api/public/v1/customer-quotes/{token}. There is no session and no cookie:
// the token in the path is the whole authority, and it grants one quote. The
// handler writes nothing that names the token, and it logs nothing but the
// route (httputil.SafePath), the status and the request id.
type PublicHandler struct {
	svc     *Service
	trusted clientip.Trusted
}

// NewPublicHandler builds the public handler. trusted decides which forwarding
// headers are believed when it resolves the source address.
func NewPublicHandler(svc *Service, trusted clientip.Trusted) *PublicHandler {
	return &PublicHandler{svc: svc, trusted: trusted}
}

// RegisterRoutes registers the public routes. They sit outside every
// authentication layer on purpose.
func (h *PublicHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+publicBase, h.guard(h.View))
	mux.HandleFunc("GET "+publicBase+"/pdf", h.guard(h.PDF))
	mux.HandleFunc("GET "+publicBase+"/logo", h.guard(h.Logo))
	mux.HandleFunc("PUT "+publicBase+"/selections", h.guard(h.Selections))
	mux.HandleFunc("POST "+publicBase+"/code", h.guard(h.Code))
	mux.HandleFunc("POST "+publicBase+"/accept", h.guard(h.Accept))
	mux.HandleFunc("POST "+publicBase+"/decline", h.guard(h.Decline))
	mux.HandleFunc("GET "+dealerQuoteBase, h.guard(h.DealerQuoteView))
	mux.HandleFunc("POST "+dealerQuoteBase+"/accept", h.guard(h.DealerQuoteAccept))
	mux.HandleFunc("POST "+dealerQuoteBase+"/decline", h.guard(h.DealerQuoteDecline))
}

// dealerQuoteBase is the prefix of the dealer's own quote link.
const dealerQuoteBase = "/api/public/v1/dealer-quotes/{token}"

// publicBase is the capability link's route prefix.
const publicBase = "/api/public/v1/customer-quotes/{token}"

// guard sets the headers every public answer carries: nothing is cached or
// indexed, the token is not leaked in a Referer, and a body is never sniffed
// into another type.
func (h *PublicHandler) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Cache-Control", "no-store")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Robots-Tag", "noindex, nofollow")
		hd.Set("X-Content-Type-Options", "nosniff")
		next(w, r)
	}
}

func (h *PublicHandler) caller(r *http.Request) Caller {
	return Caller{Address: h.trusted.Of(r), UserAgent: clip(r.UserAgent(), maxUserAgent)}
}

func (h *PublicHandler) View(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.View(r.Context(), r.PathValue("token"), h.caller(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *PublicHandler) PDF(w http.ResponseWriter, r *http.Request) {
	b, err := h.svc.PublicPDF(r.Context(), r.PathValue("token"), h.caller(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="quote.pdf"`)
	_, _ = w.Write(b)
}

func (h *PublicHandler) Logo(w http.ResponseWriter, r *http.Request) {
	l, err := h.svc.PublicLogo(r.Context(), r.PathValue("token"), h.caller(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	if l == nil {
		fail(w, r, ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", l.ContentType)
	_, _ = w.Write(l.Bytes)
}

type selectionsBody struct {
	Choices []Choice `json:"choices"`
}

func (h *PublicHandler) Selections(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var b selectionsBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		fail(w, r, err)
		return
	}
	v, err := h.svc.Choose(r.Context(), r.PathValue("token"), h.caller(r), b.Choices)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type codeBody struct {
	Channel string `json:"channel"`
}

func (h *PublicHandler) Code(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var b codeBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		fail(w, r, err)
		return
	}
	if err := h.svc.RequestCode(r.Context(), r.PathValue("token"), h.caller(r), b.Channel); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *PublicHandler) Accept(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var in AcceptInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	v, err := h.svc.Accept(r.Context(), r.PathValue("token"), h.caller(r), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *PublicHandler) Decline(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var in DeclineInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	v, err := h.svc.Decline(r.Context(), r.PathValue("token"), h.caller(r), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *PublicHandler) DealerQuoteView(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.DealerQuoteView(r.Context(), r.PathValue("token"), h.caller(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *PublicHandler) DealerQuoteAccept(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in DealerQuoteAcceptInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	v, err := h.svc.DealerQuoteDecide(r.Context(), r.PathValue("token"), h.caller(r), true, in.FullName, "")
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *PublicHandler) DealerQuoteDecline(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var in DealerQuoteDeclineInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	v, err := h.svc.DealerQuoteDecide(r.Context(), r.PathValue("token"), h.caller(r), false, in.FullName, in.Reason)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
