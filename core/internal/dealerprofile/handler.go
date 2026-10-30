// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dealerprofile

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Guards are the role guards the routes mount behind: Read for the profile
// and logo reads (every role that prints), Write for the two writes, Locale
// for the locale alone (every signed in desk person: the pages format their
// dates for it).
type Guards struct {
	Read   func(http.Handler) http.Handler
	Write  func(http.Handler) http.Handler
	Locale func(http.Handler) http.Handler
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
	mux.HandleFunc("GET /api/v1/dealer-profile/locale", guarded(g.Locale, h.GetLocale))
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

// GetLocale answers the locale alone, {"locale": "en-CA"}, for the pages
// that format dates.
func (h *Handler) GetLocale(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	locale, err := h.svc.GetLocale(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"locale": locale})
}

type putRequest struct {
	LegalName   string  `json:"legal_name"`
	TradingName string  `json:"trading_name"`
	Phone       string  `json:"phone"`
	Email       string  `json:"email"`
	Website     string  `json:"website"`
	RemitTo     string  `json:"remit_to"`
	Locale      *string `json:"locale"`
	// The GST/HST fields are pointers so a caller from before they existed
	// keeps them as they are.
	GSTHSTNumber        *string `json:"gst_hst_number"`
	GSTHSTNotRegistered *bool   `json:"gst_hst_not_registered"`
	Revision            *int64  `json:"revision"`
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
	if req.Locale != nil {
		locale := strings.TrimSpace(*req.Locale)
		if err := CheckLocale(locale); err != nil {
			v.Check(false, "locale", err.Error())
		} else {
			in.Locale = locale
		}
	}
	if req.GSTHSTNumber != nil {
		n, err := NormalizeGSTHSTNumber(*req.GSTHSTNumber)
		if err != nil {
			v.Check(false, "gst_hst_number", err.Error())
		} else {
			in.GSTHSTNumber = &n
		}
	}
	if req.GSTHSTNotRegistered != nil {
		on := *req.GSTHSTNotRegistered
		in.GSTHSTNotRegistered = &on
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
// A PNG is stored as sent; a JPEG is stored as its image written again.
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
	want := map[string]string{"image/png": "png", "image/jpeg": "jpeg"}[ctype]
	if err := CheckLogoImage(raw, want); err != nil {
		var tooBig *LogoSizeError
		if errors.As(err, &tooBig) {
			httpx.WriteError(w, r, httpx.BadRequest(err.Error(),
				httpx.FieldError{Field: "body", Message: "must be at most 2000 pixels a side and 4 megapixels"}))
			return
		}
		httpx.WriteError(w, r, httpx.BadRequest("the body is not a valid "+want+" image",
			httpx.FieldError{Field: "body", Message: "must decode as " + ctype}))
		return
	}
	if want == "jpeg" {
		// A segment walk cannot find a payload placed inside the last
		// scan, so the picture is written again and only it is stored.
		if raw, err = ReencodeLogoJPEG(raw); err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("the body is not a valid jpeg image",
				httpx.FieldError{Field: "body", Message: "must decode as " + ctype}))
			return
		}
	}
	p, err := h.svc.SetLogo(r.Context(), Logo{Bytes: raw, ContentType: ctype}, r.Header.Get("If-Match"), nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, p.Revision)
	writeJSON(w, http.StatusOK, p)
}
