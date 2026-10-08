// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package edi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/google/uuid"
)

// EDIHandler provides admin-facing API endpoints for managing EDI trading partners.
type EDIHandler struct {
	repo   EDIRepository
	bgSvc  *BuyingGroupService
	ediSvc *Service
}

// NewEDIHandler creates a new EDI admin handler.
func NewEDIHandler(repo EDIRepository, bgSvc *BuyingGroupService, ediSvc *Service) *EDIHandler {
	return &EDIHandler{repo: repo, bgSvc: bgSvc, ediSvc: ediSvc}
}

// RegisterRoutes registers EDI admin routes.
// roleGuard protects all endpoints; pass middleware.RequireRole("admin","owner") in production.
func (h *EDIHandler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("GET /api/v1/edi/partners", guard(h.ListPartners))
	mux.HandleFunc("POST /api/v1/edi/partners", guard(h.CreatePartner))
	mux.HandleFunc("GET /api/v1/edi/partners/{id}", guard(h.GetPartner))
	mux.HandleFunc("PUT /api/v1/edi/partners/{id}", guard(h.UpdatePartner))
	mux.HandleFunc("DELETE /api/v1/edi/partners/{id}", guard(h.DeletePartner))
	mux.HandleFunc("POST /api/v1/edi/partners/{id}/import-catalog", guard(h.ImportCatalog))
	mux.HandleFunc("GET /api/v1/edi/partners/{id}/catalog", guard(h.ListCatalog))
}

func (h *EDIHandler) ListPartners(w http.ResponseWriter, r *http.Request) {
	partners, err := h.repo.ListPartners(r.Context())
	if err != nil {
		httputil.RespondError(w, r, "failed to list EDI partners", http.StatusInternalServerError, err)
		return
	}
	if partners == nil {
		partners = []TradingPartner{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(partners)
}

func (h *EDIHandler) CreatePartner(w http.ResponseWriter, r *http.Request) {
	var p TradingPartner
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}
	if p.Name == "" {
		httputil.RespondError(w, r, "name is required", http.StatusBadRequest, nil)
		return
	}
	if p.TransportConfig == "" {
		p.TransportConfig = "{}"
	}
	if p.EDIVersion == "" {
		p.EDIVersion = "004010"
	}
	if p.TransportType == "" {
		p.TransportType = "SFTP"
	}
	if len(p.SupportedDocuments) == 0 {
		p.SupportedDocuments = []string{"832", "846", "850"}
	}

	if err := h.repo.CreatePartner(r.Context(), &p); err != nil {
		httputil.RespondError(w, r, "failed to create EDI partner", http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(p)
}

func (h *EDIHandler) GetPartner(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid partner ID", http.StatusBadRequest, err)
		return
	}
	p, err := h.repo.GetPartner(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, r, "Partner not found", http.StatusNotFound, err)
		return
	}

	// Include catalog count
	count, _ := h.repo.GetCatalogEntryCount(r.Context(), id)

	type PartnerWithCount struct {
		TradingPartner
		CatalogCount int `json:"catalog_count"`
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PartnerWithCount{
		TradingPartner: *p,
		CatalogCount:   count,
	})
}

func (h *EDIHandler) UpdatePartner(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid partner ID", http.StatusBadRequest, err)
		return
	}

	var p TradingPartner
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}
	p.ID = id

	if err := h.repo.UpdatePartner(r.Context(), &p); err != nil {
		// A PUT to an id that is not there is a client error, and it is the
		// same condition GetPartner already reports as 404. Anything else is
		// still a 500: an unrecognised failure must not be flattened into a
		// 4xx that tells the caller not to retry.
		status := http.StatusInternalServerError
		if errors.Is(err, ErrPartnerNotFound) {
			status = http.StatusNotFound
		}
		httputil.RespondError(w, r, "failed to update EDI partner", status, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func (h *EDIHandler) DeletePartner(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid partner ID", http.StatusBadRequest, err)
		return
	}
	if err := h.repo.DeletePartner(r.Context(), id); err != nil {
		httputil.RespondError(w, r, "failed to delete EDI partner", http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ImportCatalog accepts an EDI 832 file or CSV upload and persists catalog entries for the partner.
func (h *EDIHandler) ImportCatalog(w http.ResponseWriter, r *http.Request) {
	partnerID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid partner ID", http.StatusBadRequest, err)
		return
	}

	// Read the uploaded file body
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 50<<20)) // 50MB limit
	if err != nil {
		httputil.RespondError(w, r, "Failed to read request body", http.StatusBadRequest, err)
		return
	}

	format := r.URL.Query().Get("format") // "x12" or "csv", default "x12"
	if format == "" {
		format = "x12"
	}

	// Parse using existing BuyingGroupService
	var entries []CatalogEntry
	if format == "csv" {
		csvItems, parseErr := h.bgSvc.ParseCSVCatalog(string(data), "import")
		if parseErr != nil {
			httputil.RespondError(w, r, "CSV parse error", http.StatusUnprocessableEntity, parseErr)
			return
		}
		entries = toCatalogEntries(csvItems)
	} else {
		// Recognise the document before parsing it. Parse832Catalog skips every
		// segment it does not know — which is what makes it tolerant of a
		// supplier's extra segments — so on its own it answers "zero items,
		// no error" to a CSV uploaded without ?format=csv, to a PDF and to an
		// empty body alike. Reported as 200 {"parsed_count":0}, that tells an
		// operator who picked the wrong file that the import succeeded and
		// leaves the partner's catalog silently unchanged. The CSV branch above
		// already refuses an unreadable upload; this holds the same line.
		//
		// A document that IS X12 and carries no catalog items still succeeds
		// with parsed_count 0 — "nothing to import" is a real answer, and it is
		// a different one from "I cannot read this".
		if err := ValidateX12(string(data)); err != nil {
			httputil.RespondError(w, r, "X12 parse error", http.StatusUnprocessableEntity, err)
			return
		}
		x12Items, parseErr := h.bgSvc.Parse832Catalog(string(data))
		if parseErr != nil {
			httputil.RespondError(w, r, "X12 parse error", http.StatusUnprocessableEntity, parseErr)
			return
		}
		entries = toCatalogEntries(x12Items)
	}

	// Persist to DB
	count, err := h.repo.SaveCatalogEntries(r.Context(), partnerID, entries)
	if err != nil {
		httputil.RespondError(w, r, "failed to save catalog", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"partner_id":   partnerID.String(),
		"format":       format,
		"parsed_count": len(entries),
		"saved_count":  count,
	})
}

// toCatalogEntries maps parsed supplier catalog items onto the rows persisted
// against a trading partner. CatalogEntry.VendorSKU is the PARTNER's own
// identifier (that is what an inbound 855/810 quotes back at us), so it must
// come from SupplierCatalogEntry.VendorSKU, never from our internal SKU. The
// mapping is identical for both the CSV and the X12 source, including
// MinOrderQty and PackQty, which the parsers default to 1.
func toCatalogEntries(items []SupplierCatalogEntry) []CatalogEntry {
	if len(items) == 0 {
		return nil
	}
	entries := make([]CatalogEntry, 0, len(items))
	for _, item := range items {
		entries = append(entries, CatalogEntry{
			VendorSKU:   item.VendorSKU,
			Description: item.Description,
			UnitCost:    item.UnitPrice,
			UOM:         item.UOM,
			MinOrderQty: item.MinOrderQty,
			PackQty:     float64(item.PackSize),
		})
	}
	return entries
}

func (h *EDIHandler) ListCatalog(w http.ResponseWriter, r *http.Request) {
	partnerID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid partner ID", http.StatusBadRequest, err)
		return
	}

	entries, err := h.repo.ListCatalogEntries(r.Context(), partnerID, 200)
	if err != nil {
		httputil.RespondError(w, r, "failed to list catalog entries", http.StatusInternalServerError, err)
		return
	}
	if entries == nil {
		entries = []CatalogEntry{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}
