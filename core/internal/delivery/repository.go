// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// FleetListFilter is the fleet lists' paging: no filters beside the
// platform's cursor and limit (the fleet is dealer-wide).
type FleetListFilter struct {
	Limit     int
	AfterTime *time.Time
	AfterID   uuid.UUID
}

// RouteListFilter is the route list's filters beside the platform's cursor
// and limit. Every filter filters; anything else is refused by the strict
// query guard.
type RouteListFilter struct {
	Limit int
	// The dates are YYYY-MM-DD strings: a date is not a moment, and a
	// timestamptz parameter would shift a day across the session's zone.
	AfterDate *string
	AfterID   uuid.UUID
	Date      *string
	DriverID  *uuid.UUID
	Status    *RouteStatus
}

// StopListFilter is a route's stop list paging.
type StopListFilter struct {
	Limit        int
	AfterStopSeq *int
	AfterID      uuid.UUID
}

// PhotoListFilter is a stop's photo list paging.
type PhotoListFilter struct {
	Limit     int
	AfterTime *time.Time
	AfterID   uuid.UUID
}

// Repository is the delivery store. Every statement goes through the
// context's executor, so a write inside a caller's transaction joins it.
//
// The branch wall: vehicles and drivers carry no branch (the fleet is
// dealer-wide, a stated limit of this module's contract), so their reads
// have no wall. Routes and stops wall through the stop's order branch: a
// caller held to branch A finds branch B's stop a 404 on every route, and a
// route is visible when none of its stops belongs to a branch the caller
// cannot see (a route with no stops carries no branch fact and is visible).
type Repository interface {
	// Fleet
	CreateVehicle(ctx context.Context, v *Vehicle) error
	GetVehicle(ctx context.Context, id uuid.UUID) (*Vehicle, error)
	ListVehicles(ctx context.Context, f FleetListFilter, wantTotal bool) ([]Vehicle, bool, *int64, error)
	UpdateVehicle(ctx context.Context, v *Vehicle) error
	DeleteVehicle(ctx context.Context, id uuid.UUID) error
	LockVehicle(ctx context.Context, id uuid.UUID) error
	SetVehiclePhoto(ctx context.Context, id uuid.UUID, url string) error

	// Drivers
	CreateDriver(ctx context.Context, d *Driver) error
	GetDriver(ctx context.Context, id uuid.UUID) (*Driver, error)
	ListDrivers(ctx context.Context, f FleetListFilter, wantTotal bool) ([]Driver, bool, *int64, error)
	UpdateDriver(ctx context.Context, d *Driver) error
	DeleteDriver(ctx context.Context, id uuid.UUID) error
	LockDriver(ctx context.Context, id uuid.UUID) error
	SetDriverPhoto(ctx context.Context, id uuid.UUID, url string) error

	// Routes
	CreateRoute(ctx context.Context, route *Route) error
	GetRoute(ctx context.Context, id uuid.UUID) (*Route, error)
	ListRoutes(ctx context.Context, f RouteListFilter, wantTotal bool) ([]Route, bool, *int64, error)
	UpdateRouteStatus(ctx context.Context, id uuid.UUID, status RouteStatus) error
	TouchRoute(ctx context.Context, id uuid.UUID) error
	CountDeliveriesByRoute(ctx context.Context, routeID uuid.UUID) (int64, error)
	CountNonTerminalDeliveriesByRoute(ctx context.Context, routeID uuid.UUID) (int64, error)
	NextStopSequenceForRoute(ctx context.Context, routeID uuid.UUID) (int, error)
	LockRoute(ctx context.Context, id uuid.UUID) error

	// Stops
	CreateDelivery(ctx context.Context, d *Stop) error
	GetDelivery(ctx context.Context, id uuid.UUID) (*Stop, error)
	ListDeliveriesByRoute(ctx context.Context, routeID uuid.UUID, f StopListFilter) ([]Stop, bool, error)
	AllDeliveriesByRoute(ctx context.Context, routeID uuid.UUID) ([]Stop, error)
	UpdateDeliveryStatus(ctx context.Context, id uuid.UUID, status StopStatus, pod *PODUpdate) error
	LockDelivery(ctx context.Context, id uuid.UUID) error
	ReorderRouteDeliveries(ctx context.Context, routeID uuid.UUID, deliveryIDs []uuid.UUID) error
	ListStopsForRoutes(ctx context.Context, routeIDs []uuid.UUID) (map[uuid.UUID][]Stop, error)

	// Photos
	SavePODPhoto(ctx context.Context, photo *PODPhoto) error
	GetPODPhotos(ctx context.Context, deliveryID uuid.UUID, f PhotoListFilter) ([]PODPhoto, bool, error)

	// Quantity adjustments
	InsertQtyAdjustments(ctx context.Context, stopID, adjustedBy uuid.UUID, lines []Adjustment) error
	TouchDelivery(ctx context.Context, id uuid.UUID) error

	// Capacity
	GetRouteLoadWeight(ctx context.Context, routeID uuid.UUID) (float64, error)
	GetOrderEstimatedWeight(ctx context.Context, orderID uuid.UUID) (float64, error)

	// Branch origin (route start/end) + geocoding backfill
	GetRouteBranchID(ctx context.Context, routeID uuid.UUID) (uuid.UUID, error)
	GetBranchOrigin(ctx context.Context, branchID uuid.UUID) (*BranchOrigin, error)
	SetBranchLatLng(ctx context.Context, branchID uuid.UUID, lat, lng float64) error

	// Geocoding + ETA persistence for deliveries
	GetOrderDeliveryAddress(ctx context.Context, orderID uuid.UUID) (string, error)
	SetDeliveryLatLng(ctx context.Context, deliveryID uuid.UUID, lat, lng float64) error
	SetDeliveryETA(ctx context.Context, deliveryID uuid.UUID, eta time.Time) error

	// GetOrderBranchID resolves the order's branch through the wall so an
	// assign from a caller held to a branch can refuse a cross-branch
	// order the same way it refuses a cross-branch route.
	GetOrderBranchID(ctx context.Context, orderID uuid.UUID) (uuid.UUID, error)
}

// TouchDelivery moves a stop's revision without changing its content: the
// evidence attaches (photos, quantity adjustments) that change the document
// the client holds.
func (r *PostgresRepository) TouchDelivery(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE deliveries SET revision = revision + 1, updated_at = NOW() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to touch delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PostgresRepository implements Repository against Postgres.
type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// wallArgs are the two parameters every walled predicate carries: the
// context branch and the caller's grants subject, each passed as a typed
// nil (SQL NULL) when absent, a dereferenced zero value would make the
// predicate's IS NULL arms false and wall everything.
func wallArgs(ctx context.Context) (any, any) {
	var branch, sub any
	if b := middleware.BranchIDForQuery(ctx); b != nil {
		branch = *b
	}
	if g := middleware.GrantsSubForQuery(ctx); g != nil {
		sub = *g
	}
	return branch, sub
}

// stopVisible is the three-state visibility of a stop's order branch: the
// context branch, the caller's granted branches, or everything. args holds
// (branch uuid, grants text); $n names their positions.
func stopVisible(branchArg, subArg int) string {
	return fmt.Sprintf(`(
		($%d::uuid IS NOT NULL AND o.branch_id = $%d)
		OR ($%d::uuid IS NULL AND $%d::text IS NOT NULL AND o.branch_id IN
			(SELECT branch_id FROM user_locations WHERE user_sub = $%d))
		OR ($%d::uuid IS NULL AND $%d::text IS NULL)
	)`, branchArg, branchArg, branchArg, subArg, subArg, branchArg, subArg)
}

// routeVisible is the route wall: a route is visible when none of its stops
// belongs to a branch the caller cannot see.
func routeVisible(branchArg, subArg int) string {
	return fmt.Sprintf(`NOT EXISTS (
		SELECT 1 FROM deliveries d JOIN orders o ON o.id = d.order_id
		WHERE d.route_id = r.id AND NOT %s
	)`, stopVisible(branchArg, subArg))
}

func mapWriteError(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		field := "id"
		switch pgErr.ConstraintName {
		case "delivery_routes_vehicle_id_fkey":
			field = "vehicle_id"
		case "delivery_routes_driver_id_fkey":
			field = "driver_id"
		case "deliveries_order_id_fkey":
			field = "order_id"
		case "delivery_qty_adjustments_product_id_fkey":
			field = "product_id"
		}
		return &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
			Message: "a referenced record does not exist",
			Details: []httpx.FieldError{{Field: field, Message: "no such record"}}}
	}
	return fmt.Errorf("%s: %w", what, err)
}

const vehicleColumns = `v.id, v.name, v.vehicle_type, v.license_plate, v.capacity_weight_lbs,
	v.vin, v.year, v.make, v.model, v.insurance_expiry, v.next_service_date, v.odometer_miles,
	v.notes, v.photo_url, v.revision, v.created_at, v.updated_at`

func scanVehicle(row pgx.Row) (*Vehicle, error) {
	var (
		v                  Vehicle
		insurance, service *time.Time
		created, updated   time.Time
	)
	err := row.Scan(&v.ID, &v.Name, &v.VehicleType, &v.LicensePlate, &v.CapacityWeightLbs,
		&v.VIN, &v.Year, &v.Make, &v.Model, &insurance, &service, &v.OdometerMiles,
		&v.Notes, &v.PhotoURL, &v.Revision, &created, &updated)
	if err != nil {
		return nil, err
	}
	v.InsuranceExpiry, v.NextServiceDate = dateOf(insurance), dateOf(service)
	v.CreatedAt, v.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &v, nil
}

func (r *PostgresRepository) CreateVehicle(ctx context.Context, v *Vehicle) error {
	var created, updated time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO vehicles (name, vehicle_type, license_plate, capacity_weight_lbs,
			vin, year, make, model, insurance_expiry, next_service_date, odometer_miles, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10::date, $11, $12)
		RETURNING id, revision, created_at, updated_at`,
		v.Name, v.VehicleType, v.LicensePlate, v.CapacityWeightLbs,
		v.VIN, v.Year, v.Make, v.Model, v.InsuranceExpiry, v.NextServiceDate, v.OdometerMiles, v.Notes,
	).Scan(&v.ID, &v.Revision, &created, &updated)
	if err != nil {
		return mapWriteError(err, "failed to create vehicle")
	}
	return nil
}

func (r *PostgresRepository) GetVehicle(ctx context.Context, id uuid.UUID) (*Vehicle, error) {
	v, err := scanVehicle(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+vehicleColumns+` FROM vehicles v WHERE v.id = $1 AND v.deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get vehicle: %w", err)
	}
	return v, nil
}

func (r *PostgresRepository) ListVehicles(ctx context.Context, f FleetListFilter, wantTotal bool) ([]Vehicle, bool, *int64, error) {
	args := []any{}
	conds := []string{"v.deleted_at IS NULL"}
	if f.AfterTime != nil {
		args = append(args, *f.AfterTime, f.AfterID)
		conds = append(conds, fmt.Sprintf(`(v.created_at, v.id) < ($%d, $%d)`, len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+vehicleColumns+` FROM vehicles v WHERE `+strings.Join(conds, " AND ")+
			fmt.Sprintf(` ORDER BY v.created_at DESC, v.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, false, nil, fmt.Errorf("failed to list vehicles: %w", err)
	}
	defer rows.Close()
	items := []Vehicle{}
	for rows.Next() {
		v, err := scanVehicle(rows)
		if err != nil {
			return nil, false, nil, fmt.Errorf("failed to scan vehicle: %w", err)
		}
		items = append(items, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, false, nil, err
	}
	more := hasMore(len(items), f.Limit)
	if len(items) > f.Limit {
		items = items[:f.Limit]
	}
	var total *int64
	if wantTotal {
		var n int64
		if err := r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT count(*) FROM vehicles v WHERE v.deleted_at IS NULL`).Scan(&n); err != nil {
			return nil, false, nil, fmt.Errorf("failed to count vehicles: %w", err)
		}
		total = &n
	}
	return items, more, total, nil
}

func (r *PostgresRepository) UpdateVehicle(ctx context.Context, v *Vehicle) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE vehicles SET
			name = $1, vehicle_type = $2, license_plate = $3, capacity_weight_lbs = $4,
			vin = $5, year = $6, make = $7, model = $8,
			insurance_expiry = $9::date, next_service_date = $10::date, odometer_miles = $11, notes = $12,
			revision = revision + 1, updated_at = NOW()
		WHERE id = $13 AND deleted_at IS NULL`,
		v.Name, v.VehicleType, v.LicensePlate, v.CapacityWeightLbs,
		v.VIN, v.Year, v.Make, v.Model, v.InsuranceExpiry, v.NextServiceDate, v.OdometerMiles, v.Notes,
		v.ID)
	if err != nil {
		return mapWriteError(err, "failed to update vehicle")
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) DeleteVehicle(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE vehicles SET deleted_at = NOW(), revision = revision + 1, updated_at = NOW()
		 WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("failed to delete vehicle: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) LockVehicle(ctx context.Context, id uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM vehicles WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock vehicle: %w", err)
	}
	return nil
}

func (r *PostgresRepository) SetVehiclePhoto(ctx context.Context, id uuid.UUID, url string) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE vehicles SET photo_url = $1, revision = revision + 1, updated_at = NOW()
		 WHERE id = $2 AND deleted_at IS NULL`, url, id)
	if err != nil {
		return fmt.Errorf("failed to set vehicle photo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const driverColumns = `d.id, d.name, d.license_number, d.status, d.phone_number,
	d.cdl_class, d.cdl_expiry, d.hire_date, d.email, d.photo_url, d.revision, d.created_at, d.updated_at`

func scanDriver(row pgx.Row) (*Driver, error) {
	var (
		d                Driver
		cdlExpiry, hire  *time.Time
		created, updated time.Time
	)
	err := row.Scan(&d.ID, &d.Name, &d.LicenseNumber, &d.Status, &d.PhoneNumber,
		&d.CDLClass, &cdlExpiry, &hire, &d.Email, &d.PhotoURL, &d.Revision, &created, &updated)
	if err != nil {
		return nil, err
	}
	d.CDLExpiry, d.HireDate = dateOf(cdlExpiry), dateOf(hire)
	d.CreatedAt, d.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &d, nil
}

func (r *PostgresRepository) CreateDriver(ctx context.Context, d *Driver) error {
	var created, updated time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO drivers (name, license_number, status, phone_number, cdl_class, cdl_expiry, hire_date, email)
		VALUES ($1, $2, $3, $4, $5, $6::date, $7::date, $8)
		RETURNING id, revision, created_at, updated_at`,
		d.Name, d.LicenseNumber, d.Status, d.PhoneNumber, d.CDLClass, d.CDLExpiry, d.HireDate, d.Email,
	).Scan(&d.ID, &d.Revision, &created, &updated)
	if err != nil {
		return mapWriteError(err, "failed to create driver")
	}
	d.CreatedAt, d.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

func (r *PostgresRepository) GetDriver(ctx context.Context, id uuid.UUID) (*Driver, error) {
	d, err := scanDriver(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+driverColumns+` FROM drivers d WHERE d.id = $1 AND d.deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get driver: %w", err)
	}
	return d, nil
}

func (r *PostgresRepository) ListDrivers(ctx context.Context, f FleetListFilter, wantTotal bool) ([]Driver, bool, *int64, error) {
	args := []any{}
	conds := []string{"d.deleted_at IS NULL"}
	if f.AfterTime != nil {
		args = append(args, *f.AfterTime, f.AfterID)
		conds = append(conds, fmt.Sprintf(`(d.created_at, d.id) < ($%d, $%d)`, len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+driverColumns+` FROM drivers d WHERE `+strings.Join(conds, " AND ")+
			fmt.Sprintf(` ORDER BY d.created_at DESC, d.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, false, nil, fmt.Errorf("failed to list drivers: %w", err)
	}
	defer rows.Close()
	items := []Driver{}
	for rows.Next() {
		d, err := scanDriver(rows)
		if err != nil {
			return nil, false, nil, fmt.Errorf("failed to scan driver: %w", err)
		}
		items = append(items, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, false, nil, err
	}
	more := hasMore(len(items), f.Limit)
	if len(items) > f.Limit {
		items = items[:f.Limit]
	}
	var total *int64
	if wantTotal {
		var n int64
		if err := r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT count(*) FROM drivers d WHERE d.deleted_at IS NULL`).Scan(&n); err != nil {
			return nil, false, nil, fmt.Errorf("failed to count drivers: %w", err)
		}
		total = &n
	}
	return items, more, total, nil
}

func (r *PostgresRepository) UpdateDriver(ctx context.Context, d *Driver) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE drivers SET
			name = $1, license_number = $2, status = $3, phone_number = $4,
			cdl_class = $5, cdl_expiry = $6::date, hire_date = $7::date, email = $8,
			revision = revision + 1, updated_at = NOW()
		WHERE id = $9 AND deleted_at IS NULL`,
		d.Name, d.LicenseNumber, d.Status, d.PhoneNumber,
		d.CDLClass, d.CDLExpiry, d.HireDate, d.Email, d.ID)
	if err != nil {
		return mapWriteError(err, "failed to update driver")
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) DeleteDriver(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE drivers SET deleted_at = NOW(), revision = revision + 1, updated_at = NOW()
		 WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("failed to delete driver: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) LockDriver(ctx context.Context, id uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM drivers WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock driver: %w", err)
	}
	return nil
}

func (r *PostgresRepository) SetDriverPhoto(ctx context.Context, id uuid.UUID, url string) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE drivers SET photo_url = $1, revision = revision + 1, updated_at = NOW()
		 WHERE id = $2 AND deleted_at IS NULL`, url, id)
	if err != nil {
		return fmt.Errorf("failed to set driver photo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Routes

const routeColumns = `r.id, r.vehicle_id, r.driver_id, r.scheduled_date, r.status, r.notes,
	r.total_duration_mins, r.total_distance_miles,
	v.name, d.name,
	(SELECT count(*) FROM deliveries WHERE route_id = r.id),
	r.revision, r.created_at, r.updated_at`

const routeFrom = ` FROM delivery_routes r
	LEFT JOIN vehicles v ON r.vehicle_id = v.id
	LEFT JOIN drivers d ON r.driver_id = d.id`

func scanRoute(row pgx.Row) (*Route, error) {
	var (
		route            Route
		vehicleName      *string
		driverName       *string
		scheduled        time.Time
		created, updated time.Time
	)
	err := row.Scan(&route.ID, &route.VehicleID, &route.DriverID, &scheduled, &route.Status, &route.Notes,
		&route.TotalDurationMins, &route.TotalDistanceMiles,
		&vehicleName, &driverName, &route.StopCount,
		&route.Revision, &created, &updated)
	if err != nil {
		return nil, err
	}
	if vehicleName != nil {
		route.VehicleName = *vehicleName
	}
	if driverName != nil {
		route.DriverName = *driverName
	}
	route.ScheduledDate = scheduled.Format("2006-01-02")
	route.CreatedAt, route.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &route, nil
}

func (r *PostgresRepository) CreateRoute(ctx context.Context, route *Route) error {
	var created, updated time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO delivery_routes (vehicle_id, driver_id, scheduled_date, status, notes)
		VALUES ($1, $2, $3::date, $4, $5)
		RETURNING id, revision, created_at, updated_at`,
		route.VehicleID, route.DriverID, route.ScheduledDate, route.Status, route.Notes,
	).Scan(&route.ID, &route.Revision, &created, &updated)
	if err != nil {
		return mapWriteError(err, "failed to create route")
	}
	route.CreatedAt, route.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

func (r *PostgresRepository) GetRoute(ctx context.Context, id uuid.UUID) (*Route, error) {
	branch, sub := wallArgs(ctx)
	route, err := scanRoute(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+routeColumns+routeFrom+` WHERE r.id = $1 AND `+routeVisible(2, 3),
		id, branch, sub))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get route: %w", err)
	}
	stops, err := r.stopsOfRoutes(ctx, []uuid.UUID{route.ID})
	if err != nil {
		return nil, err
	}
	route.Stops = stops[route.ID]
	return route, nil
}

// routeFilterConds builds the WHERE conditions shared by the route list and
// its count: the wall, the date, the driver and the status. The cursor
// predicate is added only to the list.
func routeFilterConds(ctx context.Context, f RouteListFilter, args *[]any) []string {
	branch, sub := wallArgs(ctx)
	*args = append(*args, branch, sub)
	conds := []string{routeVisible(1, 2)}
	if f.Date != nil {
		*args = append(*args, *f.Date)
		conds = append(conds, fmt.Sprintf(`r.scheduled_date = $%d::date`, len(*args)))
	}
	if f.DriverID != nil {
		*args = append(*args, *f.DriverID)
		conds = append(conds, fmt.Sprintf(`r.driver_id = $%d`, len(*args)))
	}
	if f.Status != nil {
		*args = append(*args, string(*f.Status))
		conds = append(conds, fmt.Sprintf(`r.status = $%d`, len(*args)))
	}
	return conds
}

func (r *PostgresRepository) ListRoutes(ctx context.Context, f RouteListFilter, wantTotal bool) ([]Route, bool, *int64, error) {
	args := []any{}
	conds := routeFilterConds(ctx, f, &args)
	if f.AfterDate != nil {
		args = append(args, *f.AfterDate, f.AfterID)
		conds = append(conds, fmt.Sprintf(`(r.scheduled_date, r.id) < ($%d::date, $%d)`, len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+routeColumns+routeFrom+` WHERE `+strings.Join(conds, " AND ")+
			fmt.Sprintf(` ORDER BY r.scheduled_date DESC, r.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, false, nil, fmt.Errorf("failed to list routes: %w", err)
	}
	defer rows.Close()
	items := []Route{}
	for rows.Next() {
		route, err := scanRoute(rows)
		if err != nil {
			return nil, false, nil, fmt.Errorf("failed to scan route: %w", err)
		}
		items = append(items, *route)
	}
	if err := rows.Err(); err != nil {
		return nil, false, nil, err
	}
	more := hasMore(len(items), f.Limit)
	if len(items) > f.Limit {
		items = items[:f.Limit]
	}
	var total *int64
	if wantTotal {
		countArgs := []any{}
		countConds := routeFilterConds(ctx, f, &countArgs)
		var n int64
		if err := r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT count(*) FROM delivery_routes r WHERE `+strings.Join(countConds, " AND "), countArgs...).Scan(&n); err != nil {
			return nil, false, nil, fmt.Errorf("failed to count routes: %w", err)
		}
		total = &n
	}
	return items, more, total, nil
}

func (r *PostgresRepository) UpdateRouteStatus(ctx context.Context, id uuid.UUID, status RouteStatus) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE delivery_routes SET status = $1, revision = revision + 1, updated_at = NOW() WHERE id = $2`,
		status, id)
	if err != nil {
		return fmt.Errorf("failed to update route status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchRoute moves a route's revision without changing its header: the
// content writes (reorder, optimize) that change the stops the route
// document holds.
func (r *PostgresRepository) TouchRoute(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE delivery_routes SET revision = revision + 1, updated_at = NOW() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to touch route: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) CountDeliveriesByRoute(ctx context.Context, routeID uuid.UUID) (int64, error) {
	branch, sub := wallArgs(ctx)
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT count(*) FROM deliveries s JOIN orders o ON o.id = s.order_id
		 WHERE s.route_id = $1 AND `+stopVisible(2, 3), routeID, branch, sub).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count deliveries: %w", err)
	}
	return n, nil
}

// CountNonTerminalDeliveriesByRoute answers the route completion gate: how
// many stops on this route are not yet delivered, failed or partial. The
// completion runs the count under the route's row lock so the gate is
// read against the same write state the transaction is about to commit;
// without that, a stop that arrives between the gate read and the
// commit slips into the route after the count cleared (PR 70 review
// round 4 P2-1).
func (r *PostgresRepository) CountNonTerminalDeliveriesByRoute(ctx context.Context, routeID uuid.UUID) (int64, error) {
	branch, sub := wallArgs(ctx)
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT count(*) FROM deliveries s JOIN orders o ON o.id = s.order_id
		 WHERE s.route_id = $1 AND s.status NOT IN ('DELIVERED','FAILED','PARTIAL')
		   AND `+stopVisible(2, 3), routeID, branch, sub).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count non-terminal deliveries: %w", err)
	}
	return n, nil
}

// NextStopSequenceForRoute returns the next free stop_sequence for a route,
// `MAX(stop_sequence) + 1` with the start value of 1 when the route carries
// no stops. The assign transaction reads the value under the route's row
// lock so two concurrent assigns onto the same route cannot land on the
// same sequence (PR 70 review round 2 P2-1 fix held for routes of any size,
// round 4 P2-1 lifted the 200 stop page that silently truncated the read).
func (r *PostgresRepository) NextStopSequenceForRoute(ctx context.Context, routeID uuid.UUID) (int, error) {
	var next *int
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT COALESCE(MAX(s.stop_sequence), 0) + 1 FROM deliveries s WHERE s.route_id = $1`, routeID).Scan(&next)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 1, nil
		}
		return 0, fmt.Errorf("failed to read next stop sequence: %w", err)
	}
	return *next, nil
}

// AllDeliveriesByRoute returns every stop of a route, in stop_sequence
// order, without any paging. The reorder and optimize gates read the
// whole route: truncating at the list's page bound (200 on the previous
// read) dropped stops from a route above that bound, so reorder could
// not validate every stop exactly once and optimize could not return
// every stop in its answer (PR 70 review round 4 P2-1).
func (r *PostgresRepository) AllDeliveriesByRoute(ctx context.Context, routeID uuid.UUID) ([]Stop, error) {
	branch, sub := wallArgs(ctx)
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+stopColumns+stopFrom+` WHERE s.route_id = $1 AND `+stopVisible(2, 3)+
			` ORDER BY s.stop_sequence ASC, s.id ASC`, routeID, branch, sub)
	if err != nil {
		return nil, fmt.Errorf("failed to list all deliveries: %w", err)
	}
	defer rows.Close()
	items := []Stop{}
	for rows.Next() {
		s, err := scanStop(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan delivery: %w", err)
		}
		items = append(items, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *PostgresRepository) LockRoute(ctx context.Context, id uuid.UUID) error {
	branch, sub := wallArgs(ctx)
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT r.id FROM delivery_routes r WHERE r.id = $1 AND `+routeVisible(2, 3)+` FOR UPDATE OF r`,
		id, branch, sub).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock route: %w", err)
	}
	return nil
}

// Stops

const stopColumns = `s.id, s.route_id, s.order_id, COALESCE(o.number, CAST(o.id AS TEXT)),
	s.stop_sequence, s.status,
	s.pod_proof_url, s.pod_signed_by, s.pod_timestamp, s.signature_data_url, s.delivery_instructions,
	s.latitude, s.longitude, s.estimated_arrival, s.scheduled_start, s.scheduled_end,
	c.name, c.address, s.revision, s.created_at, s.updated_at`

const stopFrom = ` FROM deliveries s
	JOIN orders o ON o.id = s.order_id
	LEFT JOIN customers c ON c.id = o.customer_id`

func scanStop(row pgx.Row) (*Stop, error) {
	var (
		s                    Stop
		podTime, eta, ss, se *time.Time
		created, updated     time.Time
	)
	err := row.Scan(&s.ID, &s.RouteID, &s.OrderID, &s.OrderNumber, &s.StopSequence, &s.Status,
		&s.PODProofURL, &s.PODSignedBy, &podTime, &s.SignatureDataURL, &s.DeliveryInstructions,
		&s.Latitude, &s.Longitude, &eta, &ss, &se,
		&s.CustomerName, &s.Address, &s.Revision, &created, &updated)
	if err != nil {
		return nil, err
	}
	s.PODTimestamp, s.EstimatedArrival = httpx.PtrTimestamp(podTime), httpx.PtrTimestamp(eta)
	s.ScheduledStart, s.ScheduledEnd = httpx.PtrTimestamp(ss), httpx.PtrTimestamp(se)
	s.CreatedAt, s.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &s, nil
}

func (r *PostgresRepository) CreateDelivery(ctx context.Context, d *Stop) error {
	var created, updated time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO deliveries (route_id, order_id, stop_sequence, status, delivery_instructions, latitude, longitude)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, revision, created_at, updated_at`,
		d.RouteID, d.OrderID, d.StopSequence, d.Status, d.DeliveryInstructions, d.Latitude, d.Longitude,
	).Scan(&d.ID, &d.Revision, &created, &updated)
	if err != nil {
		return mapWriteError(err, "failed to create delivery")
	}
	d.CreatedAt, d.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

func (r *PostgresRepository) GetDelivery(ctx context.Context, id uuid.UUID) (*Stop, error) {
	branch, sub := wallArgs(ctx)
	s, err := scanStop(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+stopColumns+stopFrom+` WHERE s.id = $1 AND `+stopVisible(2, 3),
		id, branch, sub))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get delivery: %w", err)
	}
	return s, nil
}

func (r *PostgresRepository) ListDeliveriesByRoute(ctx context.Context, routeID uuid.UUID, f StopListFilter) ([]Stop, bool, error) {
	branch, sub := wallArgs(ctx)
	args := []any{routeID, branch, sub}
	conds := []string{`s.route_id = $1`, stopVisible(2, 3)}
	if f.AfterStopSeq != nil {
		args = append(args, *f.AfterStopSeq, f.AfterID)
		conds = append(conds, fmt.Sprintf(`(s.stop_sequence, s.id) > ($%d, $%d)`, len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+stopColumns+stopFrom+` WHERE `+strings.Join(conds, " AND ")+
			fmt.Sprintf(` ORDER BY s.stop_sequence ASC, s.id ASC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list deliveries: %w", err)
	}
	defer rows.Close()
	items := []Stop{}
	for rows.Next() {
		s, err := scanStop(rows)
		if err != nil {
			return nil, false, fmt.Errorf("failed to scan delivery: %w", err)
		}
		items = append(items, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := hasMore(len(items), f.Limit)
	if len(items) > f.Limit {
		items = items[:f.Limit]
	}
	return items, more, nil
}

func (r *PostgresRepository) UpdateDeliveryStatus(ctx context.Context, id uuid.UUID, status StopStatus, pod *PODUpdate) error {
	var tagRows int64
	var err error
	if pod != nil {
		tag, e := r.db.GetExecutor(ctx).Exec(ctx, `
			UPDATE deliveries
			SET status = $1, pod_proof_url = $2, pod_signed_by = $3, pod_timestamp = $4,
				signature_data_url = $5, revision = revision + 1, updated_at = NOW()
			WHERE id = $6`, status, pod.ProofURL, pod.SignedBy, pod.Time, pod.SignatureDataURL, id)
		err, tagRows = e, tag.RowsAffected()
	} else {
		tag, e := r.db.GetExecutor(ctx).Exec(ctx,
			`UPDATE deliveries SET status = $1, revision = revision + 1, updated_at = NOW() WHERE id = $2`, status, id)
		err, tagRows = e, tag.RowsAffected()
	}
	if err != nil {
		return fmt.Errorf("failed to update delivery status: %w", err)
	}
	if tagRows == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) LockDelivery(ctx context.Context, id uuid.UUID) error {
	branch, sub := wallArgs(ctx)
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT s.id FROM deliveries s JOIN orders o ON o.id = s.order_id
		 WHERE s.id = $1 AND `+stopVisible(2, 3)+` FOR UPDATE OF s`,
		id, branch, sub).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock delivery: %w", err)
	}
	return nil
}

// ReorderRouteDeliveries renumbers the named stops to their list position.
// Every stop the route holds must be named exactly once; the caller checks
// the set. Each moved stop's revision moves with it.
func (r *PostgresRepository) ReorderRouteDeliveries(ctx context.Context, routeID uuid.UUID, deliveryIDs []uuid.UUID) error {
	for i, id := range deliveryIDs {
		tag, err := r.db.GetExecutor(ctx).Exec(ctx,
			`UPDATE deliveries SET stop_sequence = $1, revision = revision + 1, updated_at = NOW()
			 WHERE id = $2 AND route_id = $3`, i+1, id, routeID)
		if err != nil {
			return fmt.Errorf("failed to reorder stops: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	return nil
}

// stopsOfRoutes reads every stop of the named routes in stop order, behind
// the caller's wall: the board's one-payload read (include=stops) and the
// route detail both use it.
func (r *PostgresRepository) stopsOfRoutes(ctx context.Context, routeIDs []uuid.UUID) (map[uuid.UUID][]Stop, error) {
	out := map[uuid.UUID][]Stop{}
	if len(routeIDs) == 0 {
		return out, nil
	}
	branch, sub := wallArgs(ctx)
	args := []any{branch, sub, routeIDs}
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT `+stopColumns+stopFrom+` WHERE s.route_id = ANY($3::uuid[]) AND `+stopVisible(1, 2)+
			` ORDER BY s.route_id, s.stop_sequence ASC, s.id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list stops for routes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		s, err := scanStop(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stop: %w", err)
		}
		if s.RouteID == nil {
			continue
		}
		out[*s.RouteID] = append(out[*s.RouteID], *s)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ListStopsForRoutes(ctx context.Context, routeIDs []uuid.UUID) (map[uuid.UUID][]Stop, error) {
	return r.stopsOfRoutes(ctx, routeIDs)
}

// Photos

func (r *PostgresRepository) SavePODPhoto(ctx context.Context, photo *PODPhoto) error {
	if photo.ID == uuid.Nil {
		photo.ID = uuid.New()
	}
	var uploaded time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO delivery_pod_photos (id, delivery_id, photo_url, photo_type, uploaded_at)
		VALUES ($1, $2, $3, $4, NOW())
		RETURNING uploaded_at`,
		photo.ID, photo.DeliveryID, photo.PhotoURL, photo.PhotoType).Scan(&uploaded)
	if err != nil {
		return mapWriteError(err, "failed to save POD photo")
	}
	photo.UploadedAt = httpx.TimestampOf(uploaded)
	return nil
}

func (r *PostgresRepository) GetPODPhotos(ctx context.Context, deliveryID uuid.UUID, f PhotoListFilter) ([]PODPhoto, bool, error) {
	args := []any{deliveryID}
	conds := []string{`p.delivery_id = $1`}
	if f.AfterTime != nil {
		args = append(args, *f.AfterTime, f.AfterID)
		conds = append(conds, fmt.Sprintf(`(p.uploaded_at, p.id) > ($%d, $%d)`, len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT p.id, p.delivery_id, p.photo_url, p.photo_type, p.uploaded_at
		 FROM delivery_pod_photos p WHERE `+strings.Join(conds, " AND ")+
			fmt.Sprintf(` ORDER BY p.uploaded_at ASC, p.id ASC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list POD photos: %w", err)
	}
	defer rows.Close()
	items := []PODPhoto{}
	for rows.Next() {
		var p PODPhoto
		var uploaded time.Time
		if err := rows.Scan(&p.ID, &p.DeliveryID, &p.PhotoURL, &p.PhotoType, &uploaded); err != nil {
			return nil, false, fmt.Errorf("failed to scan POD photo: %w", err)
		}
		p.UploadedAt = httpx.TimestampOf(uploaded)
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := hasMore(len(items), f.Limit)
	if len(items) > f.Limit {
		items = items[:f.Limit]
	}
	return items, more, nil
}

// InsertQtyAdjustments records a driver's on-site quantity adjustments in
// one statement, quantities as decimal strings straight to SQL (never
// float).
func (r *PostgresRepository) InsertQtyAdjustments(ctx context.Context, stopID, adjustedBy uuid.UUID, lines []Adjustment) error {
	for _, line := range lines {
		_, err := r.db.GetExecutor(ctx).Exec(ctx, `
			INSERT INTO delivery_qty_adjustments (delivery_id, product_id, original_qty, adjusted_qty, reason_code, notes, adjusted_by)
			VALUES ($1, $2, $3::numeric, $4::numeric, $5, $6, $7)`,
			stopID, line.ProductID, line.OriginalQty.DecimalString(), line.AdjustedQty.DecimalString(),
			line.ReasonCode, line.Notes, adjustedBy)
		if err != nil {
			return mapWriteError(err, "failed to record the quantity adjustment")
		}
	}
	return nil
}

// Capacity

func (r *PostgresRepository) GetRouteLoadWeight(ctx context.Context, routeID uuid.UUID) (float64, error) {
	var weight float64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(COALESCE(p.weight_lbs, 0) * ol.quantity), 0)
		FROM deliveries d
		JOIN order_lines ol ON ol.order_id = d.order_id
		JOIN products p ON p.id = ol.product_id
		WHERE d.route_id = $1`, routeID).Scan(&weight)
	return weight, err
}

func (r *PostgresRepository) GetOrderEstimatedWeight(ctx context.Context, orderID uuid.UUID) (float64, error) {
	var weight float64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(COALESCE(p.weight_lbs, 0) * ol.quantity), 0)
		FROM order_lines ol
		JOIN products p ON p.id = ol.product_id
		WHERE ol.order_id = $1`, orderID).Scan(&weight)
	return weight, err
}

// Branch origin (route start/end) + geocoding backfill

// BranchOrigin holds a branch's stored coordinates (nil until backfilled) and
// the composed street address used to geocode it.
type BranchOrigin struct {
	BranchID  uuid.UUID
	Latitude  *float64
	Longitude *float64
	Address   string
}

// orderVisible is the three-state visibility of an order's branch (the
// stop table joins o for the predicate; this is the version without a join).
func orderVisible(branchArg, subArg int) string {
	return fmt.Sprintf(`(
		($%d::uuid IS NOT NULL AND branch_id = $%d)
		OR ($%d::uuid IS NULL AND $%d::text IS NOT NULL AND branch_id IN
			(SELECT branch_id FROM user_locations WHERE user_sub = $%d))
		OR ($%d::uuid IS NULL AND $%d::text IS NULL)
	)`, branchArg, branchArg, branchArg, subArg, subArg, branchArg, subArg)
}

// GetOrderBranchID resolves an order's branch through the same wall the
// route and stop reads use. The assign checks the order's branch up front
// against the wall: a cross-branch caller gets the same 404 as reading the
// route (PR 70 review round 1 P3-4). Without this wall, OrderDeliveryType
// uses branchctx.WithSystem and reads any order.
func (r *PostgresRepository) GetOrderBranchID(ctx context.Context, orderID uuid.UUID) (uuid.UUID, error) {
	branch, sub := wallArgs(ctx)
	var branchID uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT branch_id FROM orders WHERE id = $1 AND `+orderVisible(2, 3),
		orderID, branch, sub).Scan(&branchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to load order branch: %w", err)
	}
	return branchID, nil
}

// GetRouteBranchID resolves the branch a route belongs to via its orders
// (routes are single-branch in practice; we read the first stop). Falls back
// to system_settings.default_branch_id when the route has no deliveries yet.
func (r *PostgresRepository) GetRouteBranchID(ctx context.Context, routeID uuid.UUID) (uuid.UUID, error) {
	var branchID uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT o.branch_id
		FROM deliveries d
		JOIN orders o ON o.id = d.order_id
		WHERE d.route_id = $1
		ORDER BY d.stop_sequence ASC
		LIMIT 1`, routeID).Scan(&branchID)
	if err == pgx.ErrNoRows {
		err = r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branchID)
	}
	if err != nil {
		return uuid.Nil, err
	}
	return branchID, nil
}

// GetBranchOrigin loads a branch's stored coordinates and composed address.
func (r *PostgresRepository) GetBranchOrigin(ctx context.Context, branchID uuid.UUID) (*BranchOrigin, error) {
	var (
		lat, lng               *float64
		addr, city, state, zip *string
	)
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT latitude, longitude, address, city, state, zip
		FROM locations WHERE id = $1`, branchID).Scan(&lat, &lng, &addr, &city, &state, &zip)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("branch not found")
		}
		return nil, err
	}
	return &BranchOrigin{
		BranchID:  branchID,
		Latitude:  lat,
		Longitude: lng,
		Address:   composeBranchAddress(addr, city, state, zip),
	}, nil
}

// SetBranchLatLng persists a backfilled branch geocode.
func (r *PostgresRepository) SetBranchLatLng(ctx context.Context, branchID uuid.UUID, lat, lng float64) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE locations SET latitude = $1, longitude = $2, updated_at = NOW() WHERE id = $3`,
		lat, lng, branchID)
	return err
}

// composeBranchAddress joins a branch's structured address parts into a single
// free-text string for geocoding.
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

// GetOrderDeliveryAddress returns the delivery address for an order (the
// customer's address). Used to geocode a new delivery stop.
func (r *PostgresRepository) GetOrderDeliveryAddress(ctx context.Context, orderID uuid.UUID) (string, error) {
	var addr *string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT c.address
		FROM orders o
		JOIN customers c ON o.customer_id = c.id
		WHERE o.id = $1`, orderID).Scan(&addr)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", fmt.Errorf("order not found")
		}
		return "", err
	}
	if addr == nil {
		return "", nil
	}
	return strings.TrimSpace(*addr), nil
}

// SetDeliveryLatLng persists geocoded coordinates for a delivery.
func (r *PostgresRepository) SetDeliveryLatLng(ctx context.Context, deliveryID uuid.UUID, lat, lng float64) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE deliveries SET latitude = $1, longitude = $2, updated_at = NOW() WHERE id = $3`,
		lat, lng, deliveryID)
	return err
}

// SetDeliveryETA persists an optimized estimated arrival time for a delivery.
func (r *PostgresRepository) SetDeliveryETA(ctx context.Context, deliveryID uuid.UUID, eta time.Time) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE deliveries SET estimated_arrival = $1, updated_at = NOW() WHERE id = $2`,
		eta, deliveryID)
	return err
}

// hasMore reports whether a LIMIT limit+1 query saw one row past the page.
func hasMore(n, limit int) bool { return n > limit }
