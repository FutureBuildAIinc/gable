// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dealerprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg" // registers the decoder the logo check uses
	_ "image/png"  // registers the decoder the logo check uses
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Guards are the role guards the routes mount behind: Read for the profile
// and logo reads (every role that prints), Write for the two writes.
type Guards struct {
	Read  func(http.Handler) http.Handler
	Write func(http.Handler) http.Handler
}

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func guarded(g func(http.Handler) http.Handler, h http.HandlerFunc) http.HandlerFunc {
	if g == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) { g(h).ServeHTTP(w, r) }
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, g Guards) {
	mux.HandleFunc("GET /api/v1/dealer-profile", guarded(g.Read, h.Get))
	mux.HandleFunc("PUT /api/v1/dealer-profile", guarded(g.Write, h.Put))
	mux.HandleFunc("GET /api/v1/dealer-profile/logo", guarded(g.Read, h.GetLogo))
	mux.HandleFunc("PUT /api/v1/dealer-profile/logo", guarded(g.Write, h.PutLogo))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.svc.Get(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, p.Revision)
	writeJSON(w, http.StatusOK, p)
}

type putRequest struct {
	LegalName   string `json:"legal_name"`
	TradingName string `json:"trading_name"`
	Phone       string `json:"phone"`
	Email       string `json:"email"`
	Website     string `json:"website"`
	RemitTo     string `json:"remit_to"`
	Revision    *int64 `json:"revision"`
}

func (req putRequest) parse() (Input, error) {
	v := &httpx.Validator{}
	in := Input{
		LegalName:   strings.TrimSpace(req.LegalName),
		TradingName: strings.TrimSpace(req.TradingName),
		Phone:       strings.TrimSpace(req.Phone),
		Email:       strings.TrimSpace(req.Email),
		Website:     strings.TrimSpace(req.Website),
		RemitTo:     strings.TrimSpace(req.RemitTo),
	}
	v.Required("legal_name", in.LegalName)
	v.Check(len(in.LegalName) <= 200, "legal_name", "must be at most 200 characters")
	v.Check(len(in.TradingName) <= 200, "trading_name", "must be at most 200 characters")
	v.Check(len(in.Phone) <= 50, "phone", "must be at most 50 characters")
	v.Check(len(in.RemitTo) <= 1000, "remit_to", "must be at most 1000 characters")
	if in.Email != "" {
		_, err := mail.ParseAddress(in.Email)
		v.Check(err == nil && !strings.ContainsAny(in.Email, "\r\n"), "email", "must be an email address")
	}
	if in.Website != "" {
		u, err := url.Parse(in.Website)
		v.Check(err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "", "website", "must be an http or https address")
	}
	return in, v.Err()
}

func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req putRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.svc.Update(r.Context(), in, r.Header.Get("If-Match"), req.Revision)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, p.Revision)
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) GetLogo(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	l, err := h.svc.GetLogo(r.Context())
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, r, httpx.NotFound("no logo is set"))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", l.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache")
	_, _ = w.Write(l.Bytes)
}

// PutLogo takes the image as the raw request body: PNG or JPEG, at most
// MaxLogoBytes, whose bytes must decode as the type the Content-Type names.
func (h *Handler) PutLogo(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctype := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if ctype != "image/png" && ctype != "image/jpeg" {
		httpx.WriteError(w, r, httpx.UnsupportedMediaType("the logo must be image/png or image/jpeg"))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxLogoBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.WriteError(w, r, httpx.PayloadTooLarge("the logo is larger than the size cap"))
			return
		}
		httpx.WriteError(w, r, httpx.BadRequest("the logo body could not be read"))
		return
	}
	_, format, derr := image.DecodeConfig(bytes.NewReader(raw))
	want := map[string]string{"image/png": "png", "image/jpeg": "jpeg"}[ctype]
	if derr != nil || format != want {
		httpx.WriteError(w, r, httpx.BadRequest("the body is not a valid "+want+" image",
			httpx.FieldError{Field: "body", Message: "must decode as " + ctype}))
		return
	}
	p, err := h.svc.SetLogo(r.Context(), Logo{Bytes: raw, ContentType: ctype}, r.Header.Get("If-Match"), nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, p.Revision)
	writeJSON(w, http.StatusOK, p)
}
