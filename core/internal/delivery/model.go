// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery

import (
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// The storage vocabularies are uppercase (the seed's and the CHECK comments'
// spelling); the wire vocabularies are lowercase snake_case and the mapping
// happens here, at the boundary (ADR 0001 section 6).

type VehicleType string

const (
	VehicleTypeBoxTruck VehicleType = "BOX_TRUCK"
	VehicleTypeFlatbed  VehicleType = "FLATBED"
	VehicleTypePickup   VehicleType = "PICKUP"
	VehicleTypeVan      VehicleType = "VAN"
	VehicleTypeCrane    VehicleType = "CRANE"
)

var vehicleTypeNames = map[VehicleType]string{
	VehicleTypeBoxTruck: "box_truck",
	VehicleTypeFlatbed:  "flatbed",
	VehicleTypePickup:   "pickup",
	VehicleTypeVan:      "van",
	VehicleTypeCrane:    "crane",
}

func (t VehicleType) MarshalText() ([]byte, error) {
	if s, ok := vehicleTypeNames[t]; ok {
		return []byte(s), nil
	}
	return []byte("van"), nil
}

// ParseVehicleType accepts only the lowercase spelling; any other casing is
// a refusal, not a synonym.
func ParseVehicleType(s string) (VehicleType, bool) {
	for storage, wire := range vehicleTypeNames {
		if s == wire {
			return storage, true
		}
	}
	return "", false
}

// VehicleTypeNames lists the wire vocabulary, for error messages.
func VehicleTypeNames() []string {
	return []string{"box_truck", "flatbed", "pickup", "van", "crane"}
}

type DriverStatus string

const (
	DriverStatusActive   DriverStatus = "ACTIVE"
	DriverStatusInactive DriverStatus = "INACTIVE"
	DriverStatusOnLeave  DriverStatus = "ON_LEAVE"
)

var driverStatusNames = map[DriverStatus]string{
	DriverStatusActive:   "active",
	DriverStatusInactive: "inactive",
	DriverStatusOnLeave:  "on_leave",
}

func (s DriverStatus) MarshalText() ([]byte, error) {
	if w, ok := driverStatusNames[s]; ok {
		return []byte(w), nil
	}
	return []byte("active"), nil
}

// ParseDriverStatus accepts only the lowercase spelling.
func ParseDriverStatus(s string) (DriverStatus, bool) {
	for storage, wire := range driverStatusNames {
		if s == wire {
			return storage, true
		}
	}
	return "", false
}

// DriverStatusNames lists the wire vocabulary, for error messages.
func DriverStatusNames() []string {
	return []string{"active", "inactive", "on_leave"}
}

type RouteStatus string

const (
	RouteStatusDraft     RouteStatus = "DRAFT"
	RouteStatusScheduled RouteStatus = "SCHEDULED"
	RouteStatusInTransit RouteStatus = "IN_TRANSIT"
	RouteStatusCompleted RouteStatus = "COMPLETED"
	RouteStatusCancelled RouteStatus = "CANCELLED"
)

var routeStatusNames = map[RouteStatus]string{
	RouteStatusDraft:     "draft",
	RouteStatusScheduled: "scheduled",
	RouteStatusInTransit: "in_transit",
	RouteStatusCompleted: "completed",
	RouteStatusCancelled: "cancelled",
}

func (s RouteStatus) MarshalText() ([]byte, error) {
	if w, ok := routeStatusNames[s]; ok {
		return []byte(w), nil
	}
	return []byte("draft"), nil
}

// ParseRouteStatus accepts only the lowercase spelling.
func ParseRouteStatus(s string) (RouteStatus, bool) {
	for storage, wire := range routeStatusNames {
		if s == wire {
			return storage, true
		}
	}
	return "", false
}

// RouteStatusNames lists the wire vocabulary, for error messages.
func RouteStatusNames() []string {
	return []string{"draft", "scheduled", "in_transit", "completed", "cancelled"}
}

type StopStatus string

const (
	StopStatusPending        StopStatus = "PENDING"
	StopStatusOutForDelivery StopStatus = "OUT_FOR_DELIVERY"
	StopStatusDelivered      StopStatus = "DELIVERED"
	StopStatusFailed         StopStatus = "FAILED"
	StopStatusPartial        StopStatus = "PARTIAL"
)

var stopStatusNames = map[StopStatus]string{
	StopStatusPending:        "pending",
	StopStatusOutForDelivery: "out_for_delivery",
	StopStatusDelivered:      "delivered",
	StopStatusFailed:         "failed",
	StopStatusPartial:        "partial",
}

func (s StopStatus) MarshalText() ([]byte, error) {
	if w, ok := stopStatusNames[s]; ok {
		return []byte(w), nil
	}
	return []byte("pending"), nil
}

// ParseStopStatus accepts only the lowercase spelling.
func ParseStopStatus(s string) (StopStatus, bool) {
	for storage, wire := range stopStatusNames {
		if s == wire {
			return storage, true
		}
	}
	return "", false
}

// StopStatusNames lists the wire vocabulary, for error messages.
func StopStatusNames() []string {
	return []string{"pending", "out_for_delivery", "delivered", "failed", "partial"}
}

// Vehicle is the fleet's wire shape. The expiry and service dates are
// business dates on the wire (YYYY-MM-DD, ADR 0001 section 12); optional
// fields are pointers without omitempty, so they serialize as null.
type Vehicle struct {
	ID                uuid.UUID       `json:"id"`
	Name              string          `json:"name"`
	VehicleType       VehicleType     `json:"vehicle_type"`
	LicensePlate      string          `json:"license_plate"`
	CapacityWeightLbs *int            `json:"capacity_weight_lbs"`
	VIN               *string         `json:"vin"`
	Year              *int            `json:"year"`
	Make              *string         `json:"make"`
	Model             *string         `json:"model"`
	InsuranceExpiry   *string         `json:"insurance_expiry"`
	NextServiceDate   *string         `json:"next_service_date"`
	OdometerMiles     *int            `json:"odometer_miles"`
	Notes             *string         `json:"notes"`
	PhotoURL          *string         `json:"photo_url"`
	Revision          int64           `json:"revision"`
	CreatedAt         httpx.Timestamp `json:"created_at"`
	UpdatedAt         httpx.Timestamp `json:"updated_at"`
}

// Driver is the roster's wire shape.
type Driver struct {
	ID            uuid.UUID       `json:"id"`
	Name          string          `json:"name"`
	LicenseNumber *string         `json:"license_number"`
	Status        DriverStatus    `json:"status"`
	PhoneNumber   *string         `json:"phone_number"`
	CDLClass      *string         `json:"cdl_class"`
	CDLExpiry     *string         `json:"cdl_expiry"`
	HireDate      *string         `json:"hire_date"`
	Email         *string         `json:"email"`
	PhotoURL      *string         `json:"photo_url"`
	Revision      int64           `json:"revision"`
	CreatedAt     httpx.Timestamp `json:"created_at"`
	UpdatedAt     httpx.Timestamp `json:"updated_at"`
}

// Route is one run's wire shape. Stops is null unless the list was asked for
// with include=stops (the board read: routes and stops in one payload).
// VehicleID and DriverID are nullable: a legacy row with no vehicle or no
// driver reads back as JSON null, never the zero UUID, so a client cannot
// reach for the all-zero id (PR 70 review round 4 P3-3).
type Route struct {
	ID                 uuid.UUID       `json:"id"`
	VehicleID          *uuid.UUID      `json:"vehicle_id"`
	DriverID           *uuid.UUID      `json:"driver_id"`
	ScheduledDate      string          `json:"scheduled_date"`
	Status             RouteStatus     `json:"status"`
	Notes              *string         `json:"notes"`
	TotalDurationMins  *int            `json:"total_duration_mins"`
	TotalDistanceMiles *float64        `json:"total_distance_miles"`
	VehicleName        string          `json:"vehicle_name"`
	DriverName         string          `json:"driver_name"`
	StopCount          int             `json:"stop_count"`
	Stops              []Stop          `json:"stops"`
	Revision           int64           `json:"revision"`
	CreatedAt          httpx.Timestamp `json:"created_at"`
	UpdatedAt          httpx.Timestamp `json:"updated_at"`
}

// Stop is one stop on a route (a deliveries row), the wire shape of the
// module's stop routes. RouteID is null for a stop that exists and is
// geocoded but is not on a route yet (the state the seed's dispatch day and
// the optimizer's planner write). OrderNumber is the order's document number.
type Stop struct {
	ID                   uuid.UUID        `json:"id"`
	RouteID              *uuid.UUID       `json:"route_id"`
	OrderID              uuid.UUID        `json:"order_id"`
	OrderNumber          *string          `json:"order_number"`
	StopSequence         int              `json:"stop_sequence"`
	Status               StopStatus       `json:"status"`
	PODProofURL          *string          `json:"pod_proof_url"`
	PODSignedBy          *string          `json:"pod_signed_by"`
	PODTimestamp         *httpx.Timestamp `json:"pod_timestamp"`
	SignatureDataURL     *string          `json:"signature_data_url"`
	DeliveryInstructions *string          `json:"delivery_instructions"`
	Latitude             *float64         `json:"latitude"`
	Longitude            *float64         `json:"longitude"`
	EstimatedArrival     *httpx.Timestamp `json:"estimated_arrival"`
	ScheduledStart       *httpx.Timestamp `json:"scheduled_start"`
	ScheduledEnd         *httpx.Timestamp `json:"scheduled_end"`
	CustomerName         *string          `json:"customer_name"`
	Address              *string          `json:"address"`
	Revision             int64            `json:"revision"`
	CreatedAt            httpx.Timestamp  `json:"created_at"`
	UpdatedAt            httpx.Timestamp  `json:"updated_at"`
}

// PODPhoto is one proof-of-delivery photo attached to a stop.
type PODPhoto struct {
	ID         uuid.UUID       `json:"id"`
	DeliveryID uuid.UUID       `json:"delivery_id"`
	PhotoURL   string          `json:"photo_url"`
	PhotoType  string          `json:"photo_type"`
	UploadedAt httpx.Timestamp `json:"uploaded_at"`
}

// QtyAdjustment is one recorded driver quantity adjustment (a short ship, a
// damage), quantities as decimal strings on the wire (ADR 0001 section 7a).
type QtyAdjustment struct {
	ID          uuid.UUID       `json:"id"`
	DeliveryID  uuid.UUID       `json:"delivery_id"`
	ProductID   uuid.UUID       `json:"product_id"`
	OriginalQty string          `json:"original_qty"`
	AdjustedQty string          `json:"adjusted_qty"`
	ReasonCode  string          `json:"reason_code"`
	Notes       *string         `json:"notes"`
	AdjustedBy  uuid.UUID       `json:"adjusted_by"`
	CreatedAt   httpx.Timestamp `json:"created_at"`
}

// AdjustReasonCodes is the closed reason vocabulary of a quantity
// adjustment, lowercase on the wire, uppercase in storage.
var adjustReasonCodes = map[string]string{
	"short_ship":    "SHORT_SHIP",
	"damaged":       "DAMAGED",
	"refused":       "REFUSED",
	"wrong_product": "WRONG_PRODUCT",
	"other":         "OTHER",
}

// ParseAdjustReasonCode accepts only the lowercase spelling.
func ParseAdjustReasonCode(s string) (string, bool) {
	storage, ok := adjustReasonCodes[s]
	return storage, ok
}

// AdjustReasonCodeNames lists the wire vocabulary, for error messages.
func AdjustReasonCodeNames() []string {
	return []string{"short_ship", "damaged", "refused", "wrong_product", "other"}
}

// CapacityWarning is returned when an assignment would exceed the vehicle's
// weight capacity: a soft warning, the assignment still happens.
type CapacityWarning struct {
	VehicleCapacityLbs float64 `json:"vehicle_capacity_lbs"`
	CurrentLoadLbs     float64 `json:"current_load_lbs"`
	OrderWeightLbs     float64 `json:"order_weight_lbs"`
	TotalAfterLbs      float64 `json:"total_after_lbs"`
}

// ErrNotFound reports that a read or write named a record that is not there
// (or is behind the caller's branch wall); the handler answers 404.
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "record not found" }

// dateOf formats a storage date as the wire's YYYY-MM-DD.
func dateOf(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}
