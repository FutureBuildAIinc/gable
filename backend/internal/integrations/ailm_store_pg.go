// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package integrations

// Postgres implementation of ailmStore. Every query here targets the schema
// created by migrations 001-080; the columns introduced specifically for this
// surface (product geometry, orders.scheduled_delivery_date,
// orders.packing_manifest, staff, module_grants) come from
// migrations/080_ailm_integration_contract.sql.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// pgAILMStore serves the AI_LM integration surface from Postgres.
type pgAILMStore struct {
	db *database.DB
}

func newPGAILMStore(db *database.DB) *pgAILMStore { return &pgAILMStore{db: db} }

// ListVehicles returns every non-deleted vehicle.
//
// capacity_weight_lbs is scanned into *int rather than COALESCEd to 0: AI_LM
// treats a missing rating as "unknown, use my own vehicle profile", whereas a
// zero would claim the truck can carry nothing and it would never be loaded.
func (s *pgAILMStore) ListVehicles(ctx context.Context) ([]VehicleResponse, error) {
	const q = `
		SELECT v.id::text, v.name, v.vehicle_type, COALESCE(v.license_plate, ''),
		       v.capacity_weight_lbs, COALESCE(v.make, ''), COALESCE(v.model, ''),
		       COALESCE(v.year, 0)
		FROM vehicles v
		WHERE v.deleted_at IS NULL
		ORDER BY v.name`

	rows, err := s.db.GetExecutor(ctx).Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query vehicles: %w", err)
	}
	defer rows.Close()

	out := []VehicleResponse{}
	for rows.Next() {
		var v VehicleResponse
		if err := rows.Scan(&v.ID, &v.Name, &v.VehicleType, &v.LicensePlate,
			&v.CapacityWeightLbs, &v.Make, &v.Model, &v.Year); err != nil {
			return nil, fmt.Errorf("scan vehicle: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListDrivers returns every non-deleted driver. AI_LM filters on Status itself
// (it still needs INACTIVE drivers to resolve names on historical routes).
func (s *pgAILMStore) ListDrivers(ctx context.Context) ([]DriverResponse, error) {
	const q = `
		SELECT d.id::text, d.name, COALESCE(d.status, 'ACTIVE')
		FROM drivers d
		WHERE d.deleted_at IS NULL
		ORDER BY d.name`

	rows, err := s.db.GetExecutor(ctx).Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query drivers: %w", err)
	}
	defer rows.Close()

	out := []DriverResponse{}
	for rows.Next() {
		var d DriverResponse
		if err := rows.Scan(&d.ID, &d.Name, &d.Status); err != nil {
			return nil, fmt.Errorf("scan driver: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListLocations returns the dealer's active branches with their lazily
// backfilled coordinates.
//
// A branch is a locations row with type='BRANCH' AND parent_id IS NULL (see
// migration 057's chk_branch_is_root); the parent_id predicate is redundant
// under that constraint but stated anyway so the query still means "top-level
// yard" if the constraint is ever relaxed.
//
// latitude/longitude are deliberately NOT COALESCEd. They are nullable by
// design (migration 072 backfills them the first time a route is optimized from
// that branch), and a COALESCE(...,0) would hand AI_LM a yard at 0,0 — null
// island, in the Gulf of Guinea — which it has no way to tell apart from a real
// coordinate. Nil instead makes "never geocoded" explicit, and AI_LM falls back
// to its configured depot and says so on the plan.
func (s *pgAILMStore) ListLocations(ctx context.Context) ([]LocationResponse, error) {
	const q = `
		SELECT l.id::text, COALESCE(NULLIF(l.name, ''), l.code),
		       l.latitude, l.longitude,
		       l.address, l.city, l.state, l.zip
		FROM locations l
		WHERE l.type = 'BRANCH' AND l.parent_id IS NULL AND l.active = TRUE
		ORDER BY COALESCE(NULLIF(l.name, ''), l.code), l.id`

	rows, err := s.db.GetExecutor(ctx).Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query locations: %w", err)
	}
	defer rows.Close()

	out := []LocationResponse{}
	for rows.Next() {
		var (
			loc                    LocationResponse
			addr, city, state, zip *string
		)
		if err := rows.Scan(&loc.ID, &loc.Name, &loc.Latitude, &loc.Longitude,
			&addr, &city, &state, &zip); err != nil {
			return nil, fmt.Errorf("scan location: %w", err)
		}
		loc.Address = composeBranchAddress(addr, city, state, zip)
		out = append(out, loc)
	}
	return out, rows.Err()
}

// composeBranchAddress joins a branch's structured address parts into the single
// free-text line a geocoder wants, e.g. "2450 Enterprise Way, Kelowna, BC V1X 7K2".
//
// Duplicated from internal/delivery deliberately: internal/integrations must not
// import internal/delivery (the integration surface is a leaf that has to stay
// httptest-able without the delivery module's dependencies). The rule it encodes
// — skip empty parts, glue state and zip with a space — must match
// delivery.composeBranchAddress, because a branch geocoded through either path
// has to resolve to the same point.
func composeBranchAddress(addr, city, state, zip *string) string {
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	parts := []string{}
	if a := deref(addr); a != "" {
		parts = append(parts, a)
	}
	if c := deref(city); c != "" {
		parts = append(parts, c)
	}
	if sz := strings.TrimSpace(deref(state) + " " + deref(zip)); sz != "" {
		parts = append(parts, sz)
	}
	return strings.Join(parts, ", ")
}

// ListProducts returns the catalog slice described by f, with per-unit weight
// and the PIM's parametric geometry attached.
//
// The geometry columns are NOT COALESCEd — a NULL length_in must reach the wire
// as JSON null so AI_LM can tell "no geometry entered" from "zero inches long".
func (s *pgAILMStore) ListProducts(ctx context.Context, f productFilter) ([]ProductResponse, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = productBulkLimit
	}

	q := `SELECT p.id::text, p.sku, p.description, COALESCE(p.category, ''),
		         p.uom_primary::text, COALESCE(p.base_price, 0), COALESCE(p.weight_lbs, 0),
		         p.length_in, p.width_in, p.height_in, p.stackable,
		         COALESCE(p.geometry_source, '')
		  FROM products p
		  WHERE 1=1`
	args := []any{}
	argIdx := 1

	if f.Category != "" {
		q += fmt.Sprintf(" AND p.category = $%d", argIdx)
		args = append(args, f.Category)
		argIdx++
	}
	if f.Query != "" {
		q += fmt.Sprintf(" AND (p.sku ILIKE $%d OR p.description ILIKE $%d)", argIdx, argIdx)
		args = append(args, "%"+f.Query+"%")
		argIdx++
	}
	q += fmt.Sprintf(" ORDER BY p.sku LIMIT $%d", argIdx)
	args = append(args, limit)

	rows, err := s.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query products: %w", err)
	}
	defer rows.Close()

	out := []ProductResponse{}
	for rows.Next() {
		var (
			p          ProductResponse
			priceFloat float64
		)
		if err := rows.Scan(&p.ID, &p.SKU, &p.Name, &p.Category, &p.UOM, &priceFloat,
			&p.WeightLbs, &p.LengthIn, &p.WidthIn, &p.HeightIn, &p.Stackable,
			&p.GeometrySource); err != nil {
			return nil, fmt.Errorf("scan product: %w", err)
		}
		p.Price = int64(priceFloat * 100)
		p.GeometrySource = resolveGeometrySource(p)
		out = append(out, p)
	}
	return out, rows.Err()
}

// resolveGeometrySource reports the provenance of a product's L/W/H triple.
// An explicit products.geometry_source always wins. Otherwise a row that
// carries dimensions is 'parametric' (the historical default), and a row with
// no dimensions reports "" — claiming 'parametric' for a SKU that has no
// geometry at all would be a lie AI_LM has no way to detect.
func resolveGeometrySource(p ProductResponse) string {
	if p.GeometrySource != "" {
		return p.GeometrySource
	}
	if p.LengthIn != nil || p.WidthIn != nil || p.HeightIn != nil {
		return "parametric"
	}
	return ""
}

// ListOrders returns orders with their line items, per-unit weights and (when a
// delivery stop for the order has been geocoded) its destination coordinates.
//
// Coordinates come from a LATERAL pick of a SINGLE geocoded delivery row per
// order. A plain JOIN to deliveries would multiply the line-item rows the
// moment AI_LM writes a route back, because a second deliveries row appears for
// the same order and every line would be emitted twice.
//
// branch_id is selected as a plain (NOT NULL) column: every order has shipped
// from a yard since migration 062, and AI_LM roots its route at that yard's
// coordinates rather than at one global depot.
//
// The date filter matches COALESCE(scheduled_delivery_date, created_at::date)
// so orders predating migration 080 (which have no scheduled date) still
// resolve by their creation date instead of vanishing from the board.
func (s *pgAILMStore) ListOrders(ctx context.Context, date, status string) ([]IntegrationOrderResponse, error) {
	q := `
		SELECT o.id::text, o.status, o.branch_id::text,
		       COALESCE(c.name, ''), COALESCE(c.address, ''),
		       COALESCE(to_char(COALESCE(o.scheduled_delivery_date, o.created_at::date), 'YYYY-MM-DD'), ''),
		       d.latitude, d.longitude,
		       ol.product_id::text, p.sku, ol.quantity, COALESCE(p.weight_lbs, 0)
		FROM orders o
		JOIN order_lines ol ON ol.order_id = o.id
		JOIN products p ON p.id = ol.product_id
		LEFT JOIN customers c ON c.id = o.customer_id
		LEFT JOIN LATERAL (
		    SELECT latitude, longitude
		    FROM deliveries
		    WHERE order_id = o.id AND latitude IS NOT NULL AND longitude IS NOT NULL
		    ORDER BY created_at
		    LIMIT 1
		) d ON TRUE
		WHERE 1=1`
	args := []any{}
	argIdx := 1

	if status != "" {
		q += fmt.Sprintf(" AND o.status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}
	if date != "" {
		q += fmt.Sprintf(" AND COALESCE(o.scheduled_delivery_date, o.created_at::date) = $%d::date", argIdx)
		args = append(args, date)
		argIdx++
	}
	q += " ORDER BY o.created_at, o.id, ol.created_at, ol.product_id"

	rows, err := s.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query orders: %w", err)
	}
	defer rows.Close()

	byID := map[string]*IntegrationOrderResponse{}
	order := []string{}
	for rows.Next() {
		var (
			id, status, branchID, custName, custAddr, schedDate string
			lat, lng                                            *float64
			productID, sku                                      string
			qty, weight                                         float64
		)
		if err := rows.Scan(&id, &status, &branchID, &custName, &custAddr, &schedDate,
			&lat, &lng, &productID, &sku, &qty, &weight); err != nil {
			return nil, fmt.Errorf("scan order row: %w", err)
		}

		o, ok := byID[id]
		if !ok {
			o = &IntegrationOrderResponse{
				ID:            id,
				Status:        status,
				BranchID:      branchID,
				CustomerName:  custName,
				Address:       custAddr,
				Latitude:      lat,
				Longitude:     lng,
				ScheduledDate: schedDate,
				Lines:         []IntegrationOrderLine{},
			}
			byID[id] = o
			order = append(order, id)
		}
		if o.Latitude == nil && lat != nil {
			o.Latitude, o.Longitude = lat, lng
		}
		o.Lines = append(o.Lines, IntegrationOrderLine{
			ProductID: productID,
			SKU:       sku,
			Quantity:  qty,
			WeightLbs: weight,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate order rows: %w", err)
	}

	out := make([]IntegrationOrderResponse, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// ReplaceDeliveryRoute writes an approved plan onto the dispatch board in one
// transaction: it removes any not-yet-dispatched route for the same
// (vehicle_id, scheduled_date), inserts the new route plus its stops, and
// persists the packing manifest onto every stop's order.
//
// Only DRAFT/SCHEDULED routes are replaced. A route already IN_TRANSIT or
// COMPLETED describes a truck that has left the yard, and re-planning must
// never rewrite history.
func (s *pgAILMStore) ReplaceDeliveryRoute(ctx context.Context, req DeliveryRouteRequest) (*DeliveryRouteResponse, error) {
	vehicleID, err := uuid.Parse(req.VehicleID)
	if err != nil {
		return nil, fmt.Errorf("parse vehicle_id: %w", err)
	}
	var driverID *uuid.UUID
	if req.DriverID != "" {
		d, err := uuid.Parse(req.DriverID)
		if err != nil {
			return nil, fmt.Errorf("parse driver_id: %w", err)
		}
		driverID = &d
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		DELETE FROM deliveries WHERE route_id IN (
		    SELECT id FROM delivery_routes
		    WHERE vehicle_id = $1 AND scheduled_date = $2::date
		      AND status IN ('DRAFT', 'SCHEDULED')
		)`, vehicleID, req.ScheduledDate); err != nil {
		return nil, fmt.Errorf("clear superseded stops: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM delivery_routes
		WHERE vehicle_id = $1 AND scheduled_date = $2::date
		  AND status IN ('DRAFT', 'SCHEDULED')`, vehicleID, req.ScheduledDate)
	if err != nil {
		return nil, fmt.Errorf("clear superseded route: %w", err)
	}
	replaced := tag.RowsAffected() > 0

	notes := req.Notes
	if notes == "" {
		notes = "Optimized by AI Load Management"
	}

	var routeID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO delivery_routes (vehicle_id, driver_id, scheduled_date, status, notes)
		VALUES ($1, $2, $3::date, 'SCHEDULED', $4)
		RETURNING id::text`, vehicleID, driverID, req.ScheduledDate, notes).Scan(&routeID); err != nil {
		return nil, fmt.Errorf("insert route: %w", err)
	}

	orderIDs := make([]uuid.UUID, 0, len(req.Stops))
	for _, stop := range req.Stops {
		orderID, err := uuid.Parse(stop.OrderID)
		if err != nil {
			return nil, fmt.Errorf("parse order_id %q: %w", stop.OrderID, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO deliveries (route_id, order_id, stop_sequence, status, latitude, longitude)
			VALUES ($1::uuid, $2, $3, 'PENDING', $4, $5)`,
			routeID, orderID, stop.Sequence, stop.Lat, stop.Lng); err != nil {
			return nil, fmt.Errorf("insert stop for order %s: %w", stop.OrderID, err)
		}
		orderIDs = append(orderIDs, orderID)
	}

	// The route-level manifest is stored on every order in the route so the
	// yard "Pack Trucks" view can replay loading from any of them. Re-pushing an
	// edited plan overwrites it: the latest approved manifest always wins.
	if len(req.LoadManifest) > 0 && string(req.LoadManifest) != "null" && len(orderIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE orders SET packing_manifest = $1, updated_at = NOW()
			WHERE id = ANY($2::uuid[])`, []byte(req.LoadManifest), orderIDs); err != nil {
			return nil, fmt.Errorf("persist packing manifest: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit route: %w", err)
	}

	return &DeliveryRouteResponse{
		RouteID:   routeID,
		StopCount: len(req.Stops),
		Created:   true,
		Replaced:  replaced,
	}, nil
}

// LookupStaff resolves a staff member by email (case-insensitive, preferred) or
// staff_no, and returns the module IDs they hold that are ALSO globally enabled
// via the system_settings key `modules.<id>.enabled`. Intersecting inside the
// query means one operator flag revokes a module for the whole roster.
//
// A missing staff member is (Found=false, nil error): a failed login is not a
// server error.
func (s *pgAILMStore) LookupStaff(ctx context.Context, email, staffNo string) (*staffLookup, error) {
	const q = `
		SELECT st.id::text, st.email, st.full_name, COALESCE(st.role, ''), st.active,
		       COALESCE(
		           ARRAY_REMOVE(ARRAY_AGG(g.module_id) FILTER (WHERE ss.value = 'true'), NULL),
		           '{}'
		       ) AS modules
		FROM staff st
		LEFT JOIN module_grants g ON g.staff_id = st.id
		LEFT JOIN system_settings ss ON ss.key = 'modules.' || g.module_id || '.enabled'
		WHERE CASE
		          WHEN $1::text <> '' THEN LOWER(st.email) = LOWER($1::text)
		          ELSE st.staff_no = $2::text
		      END
		GROUP BY st.id`

	var out staffLookup
	err := s.db.GetExecutor(ctx).QueryRow(ctx, q, email, staffNo).Scan(
		&out.ID, &out.Email, &out.Name, &out.Role, &out.Active, &out.Modules,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &staffLookup{Found: false, Modules: []string{}}, nil
		}
		return nil, fmt.Errorf("look up staff: %w", err)
	}
	out.Found = true
	if out.Modules == nil {
		out.Modules = []string{}
	}
	return &out, nil
}
