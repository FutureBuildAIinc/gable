// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package configurator

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/apps"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes mounts configurator routes. mux is the apps.Router surface —
// the configurator registers under the "millwork" app, so these routes gate
// on that app's enablement; *http.ServeMux also satisfies the interface.
func (h *Handler) RegisterRoutes(mux apps.Router, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("GET /api/v1/configurator/rules", guard(h.handleGetRules))
	mux.HandleFunc("POST /api/v1/configurator/validate", guard(h.handleValidate))
	mux.HandleFunc("POST /api/v1/configurator/build-sku", guard(h.handleBuildSKU))
	mux.HandleFunc("GET /api/v1/configurator/options", guard(h.handleGetOptions))
	mux.HandleFunc("GET /api/v1/configurator/presets", guard(h.handleGetPresets))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

func (h *Handler) handleGetRules(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rules, err := h.service.AllRules(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// selectionsRequest is the body of validate and build-sku.
type selectionsRequest struct {
	Selections map[string]string `json:"selections"`
}

// parseSelectionsBody decodes the body strictly and requires a non-empty
// selections map, collecting every problem into one 400.
func parseSelectionsBody(r *http.Request, productType bool) (Selections, string, error) {
	var req struct {
		Selections  map[string]string `json:"selections"`
		ProductType *string           `json:"product_type"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return nil, "", err
	}
	v := &httpx.Validator{}
	v.Check(len(req.Selections) > 0, "selections", "is required")
	pt := ""
	if productType {
		v.Required("product_type", deref(req.ProductType))
		if req.ProductType != nil {
			pt = strings.TrimSpace(*req.ProductType)
			v.Check(pt != "", "product_type", "is required")
		}
	} else if req.ProductType != nil {
		v.Check(false, "product_type", "is not a field of this route")
	}
	if err := v.Err(); err != nil {
		return nil, "", err
	}
	return req.Selections, pt, nil
}

func (h *Handler) handleValidate(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	selections, _, err := parseSelectionsBody(r, false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.service.ValidateConfig(r.Context(), selections)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleBuildSKU(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	selections, productType, err := parseSelectionsBody(r, true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.service.BuildSKU(r.Context(), productType, selections)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseSelectionsParam reads the selections query parameter: a comma
// separated list of Type=Value pairs, URL encoded, naming the selections
// the options read is constrained by. A malformed pair is a 400 naming the
// parameter; this is the strict posture the base's every-unknown-name-is-a-
// selection never had.
func parseSelectionsParam(v *httpx.Validator, raw string) Selections {
	if raw == "" {
		return Selections{}
	}
	out := Selections{}
	for _, pair := range strings.Split(raw, ",") {
		eq := strings.Index(pair, "=")
		if eq <= 0 || eq == len(pair)-1 {
			v.Check(false, "selections", "must be a comma separated list of Type=Value pairs")
			return nil
		}
		key := strings.TrimSpace(pair[:eq])
		val := strings.TrimSpace(pair[eq+1:])
		if key == "" || val == "" {
			v.Check(false, "selections", "must be a comma separated list of Type=Value pairs")
			return nil
		}
		out[key] = val
	}
	return out
}

func (h *Handler) handleGetOptions(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "attribute_type", "selections")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	attributeType := ""
	if vals := q["attribute_type"]; len(vals) == 0 {
		v.Check(false, "attribute_type", "is required")
	} else if len(vals) > 1 {
		v.Check(false, "attribute_type", "parameter is repeated")
	} else {
		attributeType = vals[0]
		v.Check(strings.TrimSpace(attributeType) != "", "attribute_type", "is required")
	}
	var selections Selections
	if vals := q["selections"]; len(vals) > 1 {
		v.Check(false, "selections", "parameter is repeated")
	} else if len(vals) == 1 {
		selections = parseSelectionsParam(v, vals[0])
	} else {
		selections = Selections{}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	options, err := h.service.GetAvailableOptions(r.Context(), attributeType, selections)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, options)
}

func (h *Handler) handleGetPresets(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "product_type")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	productType := ""
	if vals := q["product_type"]; len(vals) > 1 {
		v.Check(false, "product_type", "parameter is repeated")
	} else if len(vals) == 1 {
		productType = vals[0]
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	presets, err := h.service.Presets(r.Context(), productType)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, presets)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
