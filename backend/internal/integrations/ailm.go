// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package integrations

// AI_LM (AI Load Management) integration surface.
//
// AI_LM (github.com/gablelbm/gable-ai-lm) is a standalone sidecar service. It
// authenticates its operators against this ERP, pulls its source-of-truth data
// (fleet, drivers, the dealer's branches, a calendar date's confirmed orders,
// and the product catalog with per-unit weight + PIM geometry), runs a 3D
// packing/routing solver, and writes approved delivery routes — including the
// packing manifest that powers the yard "Pack Trucks" instructions — back onto
// the dispatch board.
//
// The branch surface exists because a dealer with more than one yard ships from
// several yards on the same day. Every order carries the branch it ships from
// (orders.branch_id, NOT NULL since migration 062) and every branch carries its
// geocoded coordinates (locations.latitude/longitude, migration 072), so AI_LM
// can root a route at the yard the load actually leaves from instead of at one
// globally configured depot.
//
// Every endpoint here is gated by the X-Integration-Key header (see
// Handler.authMiddleware) and every list endpoint returns a BARE JSON ARRAY,
// not an envelope, because AI_LM's client decodes straight into a Go slice.
//
// The wire shapes below mirror gable-ai-lm/backend/internal/gable/client.go
// field for field. That mirroring is machine-checked by ailm_contract_test.go
// against golden fixtures, so a rename or a nullability change on either side
// fails a build instead of failing at runtime.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Product list caps. A filtered request is a typeahead search and stays small;
// an unfiltered request is AI_LM hydrating its whole load-planning catalog and
// needs a cap high enough not to silently truncate a dealer's SKU list.
const (
	productSearchLimit = 20
	productBulkLimit   = 1000
)

// aiLMModuleID is the module a staff member must be granted to use AI_LM.
const aiLMModuleID = "ai_lm"

// dateLayout is the wire format AI_LM uses for every date it sends.
const dateLayout = "2006-01-02"

// --- wire types -------------------------------------------------------------
//
// Mirrors of gable-ai-lm/backend/internal/gable/client.go. Pointer fields are
// pointers on purpose: they let the payload say "this ERP has no value" rather
// than asserting a zero.

// VehicleResponse is a fleet vehicle. Mirrors gable.Vehicle. AI_LM keys its own
// axle/bed-dimension profiles by ID, so the ID must be stable.
type VehicleResponse struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	VehicleType       string `json:"vehicle_type"`
	LicensePlate      string `json:"license_plate,omitempty"`
	CapacityWeightLbs *int   `json:"capacity_weight_lbs,omitempty"` // nil = rating not recorded
	Make              string `json:"make,omitempty"`
	Model             string `json:"model,omitempty"`
	Year              int    `json:"year,omitempty"`
}

// DriverResponse is a fleet driver. Mirrors gable.Driver. AI_LM needs a valid
// driver ID to attach to each route it writes back.
type DriverResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // ACTIVE / INACTIVE / ON_LEAVE
}

// LocationResponse is one of the dealer's branches — a yard AI_LM may root a
// route at. Mirrors gable.Location.
//
// Latitude and Longitude are POINTERS because locations.latitude/longitude are
// lazily backfilled (migration 072): a branch nobody has geocoded yet is NULL,
// and NULL must stay distinguishable from a real 0.0. Null island is in the
// Gulf of Guinea, so a COALESCE(...,0) here would silently root a whole day's
// routes off the coast of Africa instead of telling AI_LM "I don't know where
// this yard is, fall back".
//
// Address is the composed "street, city, state zip" free-text form, i.e. what
// you would hand a geocoder — the same composition delivery.GetBranchOrigin
// uses, so a branch geocoded by either path resolves to the same place.
type LocationResponse struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Address   string   `json:"address,omitempty"`
	Latitude  *float64 `json:"latitude,omitempty"`  // nil = never geocoded
	Longitude *float64 `json:"longitude,omitempty"` // nil = never geocoded
}

// ProductResponse is a catalog product with its per-unit weight and the PIM's
// canonical parametric geometry. Mirrors gable.Product.
//
// LengthIn/WidthIn/HeightIn/Stackable are pointers so that "the PIM has no
// geometry for this SKU" (null) stays distinguishable from a real zero
// dimension or a real `false`. AI_LM's resolveGeometry() branches on exactly
// that distinction to decide whether to fall back to its own override table.
//
// Price is an ERP-side extra that AI_LM ignores; it predates the AI_LM contract
// and is kept for the existing pricing consumers of this endpoint.
type ProductResponse struct {
	ID             string   `json:"id"`
	SKU            string   `json:"sku"`
	Name           string   `json:"name"`
	Category       string   `json:"category"`
	UOM            string   `json:"uom"`
	Price          int64    `json:"price"`      // cents
	WeightLbs      float64  `json:"weight_lbs"` // per-unit weight (lb); 0 = unknown
	LengthIn       *float64 `json:"length_in"`  // inches; null = no PIM geometry
	WidthIn        *float64 `json:"width_in"`   // inches; null = no PIM geometry
	HeightIn       *float64 `json:"height_in"`  // inches; null = no PIM geometry
	Stackable      *bool    `json:"stackable"`  // null = unknown (AI_LM defaults true)
	GeometrySource string   `json:"geometry_source"`
}

// IntegrationOrderLine is one line of an order. Mirrors gable.OrderLine.
// WeightLbs is the PER-UNIT catalog weight, not the line total — AI_LM
// multiplies by Quantity itself.
type IntegrationOrderLine struct {
	ProductID string  `json:"product_id"`
	SKU       string  `json:"sku"`
	Quantity  float64 `json:"quantity"`
	WeightLbs float64 `json:"weight_lbs"`
}

// IntegrationOrderResponse is an order plus its lines and, when a delivery stop
// for it has been geocoded, its destination coordinates. Mirrors gable.Order.
// ScheduledDate is an ERP-side extra (AI_LM ignores it) that echoes back which
// date the row matched, which makes a wrong ?date= filter obvious.
//
// BranchID is the yard this order ships from. It is deliberately NOT omitempty:
// orders.branch_id is NOT NULL (migration 062), so an empty string on the wire
// is a bug in this ERP, and AI_LM must be able to SEE that bug rather than
// receive a payload in which "no branch" and "field dropped" look identical.
// AI_LM cross-references it against GET /api/integration/locations to pick the
// depot a plan is rooted at.
type IntegrationOrderResponse struct {
	ID            string                 `json:"id"`
	Status        string                 `json:"status"`
	BranchID      string                 `json:"branch_id"` // orders.branch_id; never empty
	CustomerName  string                 `json:"customer_name,omitempty"`
	Address       string                 `json:"address,omitempty"`
	Latitude      *float64               `json:"latitude,omitempty"`  // nil = not geocoded
	Longitude     *float64               `json:"longitude,omitempty"` // nil = not geocoded
	ScheduledDate string                 `json:"scheduled_date,omitempty"`
	Lines         []IntegrationOrderLine `json:"lines"`
}

// DeliveryStopInput is one stop of a route AI_LM pushes back. Mirrors
// gable.RouteStop. AI_LM always sends lat/lng as plain numbers; they are
// pointers here so a caller that genuinely has no coordinate can omit them
// instead of pinning the stop to null island (0,0).
type DeliveryStopInput struct {
	OrderID  string   `json:"order_id"`
	Sequence int      `json:"sequence"`
	Lat      *float64 `json:"lat"`
	Lng      *float64 `json:"lng"`
}

// DeliveryRouteRequest is the approved-plan write-back body. Mirrors
// gable.DeliveryRoute. DriverID is optional: AI_LM plans a truck before a
// driver is necessarily assigned, and its client omits the field when empty.
// LoadManifest is opaque JSON (AI_LM types it `any`) persisted verbatim.
type DeliveryRouteRequest struct {
	VehicleID     string              `json:"vehicle_id"`
	DriverID      string              `json:"driver_id"`
	ScheduledDate string              `json:"scheduled_date"` // YYYY-MM-DD
	Notes         string              `json:"notes"`
	Stops         []DeliveryStopInput `json:"stops"`
	LoadManifest  json.RawMessage     `json:"load_manifest,omitempty"`
}

// DeliveryRouteResponse acknowledges a write-back. AI_LM discards the body and
// only checks the status code, so this exists for humans and for curl.
type DeliveryRouteResponse struct {
	RouteID   string `json:"route_id"`
	StopCount int    `json:"stop_count"`
	Created   bool   `json:"created"`
	Replaced  bool   `json:"replaced"` // true when a prior plan for this vehicle+date was superseded
}

// ValidateStaffRequest is the login lookup body. AI_LM sends only `email`;
// `staff_no` is accepted for badge/kiosk callers.
type ValidateStaffRequest struct {
	Email   string `json:"email"`
	StaffNo string `json:"staff_no"`
}

// ValidateStaffResponse reports whether a staff member may use AI_LM and
// carries the identity + grants that authorize the session. Mirrors
// gable.StaffValidation.
//
// The identity fields are omitempty so an unknown email answers with the
// minimal {"entitled":false,"roles":[],"modules":[]} rather than echoing
// attacker-supplied input back. Roles and Modules are never null — AI_LM copies
// Roles straight into a JWT claim.
type ValidateStaffResponse struct {
	StaffID  string   `json:"staff_id,omitempty"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	Entitled bool     `json:"entitled"`
	Roles    []string `json:"roles"`
	Modules  []string `json:"modules"`
}

// --- store seam -------------------------------------------------------------

// productFilter selects the slice of the catalog an integration caller wants.
// A zero Category and Query means "everything" (a bulk pull).
type productFilter struct {
	Category string
	Query    string
	Limit    int
}

// staffLookup is the raw entitlement fact set for one staff member. Entitlement
// itself is derived in the handler (not the store) so the rule is unit-testable
// without a database.
type staffLookup struct {
	Found bool
	ID    string
	Email string
	Name  string
	Role  string
	// Active is staff.active; an inactive staff member is never entitled.
	Active bool
	// Modules is the set of module IDs granted to this staff member that are
	// ALSO globally enabled (granted ∩ enabled).
	Modules []string
}

// ailmStore is the data dependency of the AI_LM integration surface. It exists
// so the handlers can be exercised over httptest without Postgres; the
// production implementation is *pgAILMStore in ailm_store_pg.go.
type ailmStore interface {
	ListVehicles(ctx context.Context) ([]VehicleResponse, error)
	ListDrivers(ctx context.Context) ([]DriverResponse, error)
	ListLocations(ctx context.Context) ([]LocationResponse, error)
	ListProducts(ctx context.Context, f productFilter) ([]ProductResponse, error)
	ListOrders(ctx context.Context, date, status string) ([]IntegrationOrderResponse, error)
	ReplaceDeliveryRoute(ctx context.Context, req DeliveryRouteRequest) (*DeliveryRouteResponse, error)
	LookupStaff(ctx context.Context, email, staffNo string) (*staffLookup, error)
}

// --- fleet ------------------------------------------------------------------

// ListVehicles returns the active fleet as a bare JSON array.
//
//	GET /api/integration/vehicles
func (h *Handler) ListVehicles(w http.ResponseWriter, r *http.Request) {
	vehicles, err := h.ailm.ListVehicles(r.Context())
	if err != nil {
		slog.Error("integration: list vehicles", "error", err, "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "failed to query vehicles")
		return
	}
	writeJSON(w, http.StatusOK, nonNil(vehicles))
}

// ListDrivers returns the fleet's drivers as a bare JSON array.
//
//	GET /api/integration/drivers
func (h *Handler) ListDrivers(w http.ResponseWriter, r *http.Request) {
	drivers, err := h.ailm.ListDrivers(r.Context())
	if err != nil {
		slog.Error("integration: list drivers", "error", err, "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "failed to query drivers")
		return
	}
	writeJSON(w, http.StatusOK, nonNil(drivers))
}

// --- branches ---------------------------------------------------------------

// ListLocations returns the dealer's active branches as a bare JSON array.
//
//	GET /api/integration/locations
//
// AI_LM joins this to orders.branch_id to root a plan at the yard the load
// actually leaves from. A branch with null coordinates is returned anyway —
// AI_LM needs to know the branch EXISTS in order to report "this yard has never
// been geocoded" rather than silently planning from somewhere else.
func (h *Handler) ListLocations(w http.ResponseWriter, r *http.Request) {
	locations, err := h.ailm.ListLocations(r.Context())
	if err != nil {
		slog.Error("integration: list locations", "error", err, "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "failed to query locations")
		return
	}
	writeJSON(w, http.StatusOK, nonNil(locations))
}

// --- orders -----------------------------------------------------------------

// ListOrdersForDate returns orders with their line items and per-unit weights.
//
//	GET /api/integration/orders?date=YYYY-MM-DD&status=CONFIRMED
//
// Both filters are optional (an unfiltered call returns the whole order book),
// but a malformed ?date= is rejected with 400 rather than being handed to
// Postgres as a cast that fails with a 500.
func (h *Handler) ListOrdersForDate(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date != "" {
		if _, err := time.Parse(dateLayout, date); err != nil {
			writeError(w, http.StatusBadRequest, "invalid date; expected YYYY-MM-DD")
			return
		}
	}
	status := r.URL.Query().Get("status")

	orders, err := h.ailm.ListOrders(r.Context(), date, status)
	if err != nil {
		slog.Error("integration: list orders", "error", err, "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "failed to query orders")
		return
	}
	writeJSON(w, http.StatusOK, nonNil(orders))
}

// --- delivery route write-back ----------------------------------------------

// CreateDeliveryRoute persists an AI_LM-approved plan onto the dispatch board
// as one delivery_routes row plus one deliveries row per stop.
//
//	POST /api/integration/delivery-routes
//
// Idempotent on (vehicle_id, scheduled_date): a not-yet-dispatched route for
// that pair is REPLACED, so re-approving an edited plan updates the board
// instead of duplicating it or silently keeping the stale one.
func (h *Handler) CreateDeliveryRoute(w http.ResponseWriter, r *http.Request) {
	var req DeliveryRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, err := uuid.Parse(req.VehicleID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid vehicle_id")
		return
	}
	// driver_id is optional — AI_LM omits it for an unassigned truck — but a
	// present value must be a real UUID rather than a silently dropped stray.
	if req.DriverID != "" {
		if _, err := uuid.Parse(req.DriverID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid driver_id")
			return
		}
	}
	if _, err := time.Parse(dateLayout, req.ScheduledDate); err != nil {
		writeError(w, http.StatusBadRequest, "invalid scheduled_date; expected YYYY-MM-DD")
		return
	}
	if len(req.Stops) == 0 {
		writeError(w, http.StatusBadRequest, "route must have at least one stop")
		return
	}
	for _, s := range req.Stops {
		if _, err := uuid.Parse(s.OrderID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid order_id in stops")
			return
		}
	}

	resp, err := h.ailm.ReplaceDeliveryRoute(r.Context(), req)
	if err != nil {
		slog.Error("integration: create delivery route", "error", err, "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "failed to create delivery route")
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// --- staff validation (AI_LM's login path) ----------------------------------

// ValidateStaff reports whether a staff member is entitled to use AI_LM.
//
//	POST /api/integration/validate-staff  {"email": "..."}
//
// entitled = staff.active AND the 'ai_lm' module is granted to them AND that
// module is globally enabled. A well-formed request ALWAYS answers 200: an
// unknown email is a non-entitled result, not an error, because AI_LM's client
// turns any non-2xx into a transport failure and would report "GableLBM is
// down" for what is really a failed login.
func (h *Handler) ValidateStaff(w http.ResponseWriter, r *http.Request) {
	var req ValidateStaffRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Email == "" && req.StaffNo == "" {
		writeError(w, http.StatusBadRequest, "email or staff_no required")
		return
	}

	found, err := h.ailm.LookupStaff(r.Context(), req.Email, req.StaffNo)
	if err != nil {
		slog.Error("integration: validate staff", "error", err, "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "failed to validate staff")
		return
	}

	if found == nil || !found.Found {
		writeJSON(w, http.StatusOK, ValidateStaffResponse{
			Entitled: false,
			Roles:    []string{},
			Modules:  []string{},
		})
		return
	}

	modules := found.Modules
	if modules == nil {
		modules = []string{}
	}
	roles := []string{}
	if found.Role != "" {
		roles = append(roles, found.Role)
	}

	writeJSON(w, http.StatusOK, ValidateStaffResponse{
		StaffID:  found.ID,
		Email:    found.Email,
		Name:     found.Name,
		Entitled: found.Active && contains(modules, aiLMModuleID),
		Roles:    roles,
		Modules:  modules,
	})
}

// --- helpers ----------------------------------------------------------------

// nonNil guarantees a bare `[]` rather than `null` on the wire. AI_LM decodes
// list endpoints straight into a Go slice, and `null` would hand it a nil slice
// that reads as "no fleet configured" instead of "no rows matched".
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
