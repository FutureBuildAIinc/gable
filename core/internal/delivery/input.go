// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// datePattern is a business date (ADR 0001 section 12): YYYY-MM-DD.
var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// VehicleRequest is the body of the vehicle create and update routes. The
// fields are strings and raw JSON, never the final types, so Parse collects
// every problem into one 400 with its full field path.
type VehicleRequest struct {
	Name              *string         `json:"name"`
	VehicleType       *string         `json:"vehicle_type"`
	LicensePlate      *string         `json:"license_plate"`
	CapacityWeightLbs json.RawMessage `json:"capacity_weight_lbs"`
	VIN               *string         `json:"vin"`
	Year              json.RawMessage `json:"year"`
	Make              *string         `json:"make"`
	Model             *string         `json:"model"`
	InsuranceExpiry   *string         `json:"insurance_expiry"`
	NextServiceDate   *string         `json:"next_service_date"`
	OdometerMiles     json.RawMessage `json:"odometer_miles"`
	Notes             *string         `json:"notes"`
	Revision          json.RawMessage `json:"revision"`
}

// VehicleDraft is the parsed vehicle write.
type VehicleDraft struct {
	Name              string
	VehicleType       VehicleType
	LicensePlate      string
	CapacityWeightLbs *int
	VIN               *string
	Year              *int
	Make              *string
	Model             *string
	InsuranceExpiry   *string
	NextServiceDate   *string
	OdometerMiles     *int
	Notes             *string
	Revision          *int64
}

func (req *VehicleRequest) Parse(update bool) (*VehicleDraft, error) {
	v := &httpx.Validator{}
	d := &VehicleDraft{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		v.Check(len(name) > 0, "name", "is required")
		v.Check(utf8.RuneCountInString(name) <= 255, "name", "must be at most 255 characters")
		d.Name = name
	} else if !update {
		v.Required("name", "")
	}
	if req.VehicleType != nil {
		if t, ok := ParseVehicleType(strings.TrimSpace(*req.VehicleType)); ok {
			d.VehicleType = t
		} else {
			v.Check(false, "vehicle_type", "must be one of: "+strings.Join(VehicleTypeNames(), ", "))
		}
	} else if !update {
		v.Required("vehicle_type", "")
	}
	if req.LicensePlate != nil {
		plate := strings.TrimSpace(*req.LicensePlate)
		v.Check(len(plate) > 0, "license_plate", "is required")
		v.Check(utf8.RuneCountInString(plate) <= 20, "license_plate", "must be at most 20 characters")
		d.LicensePlate = plate
	} else if !update {
		v.Required("license_plate", "")
	}
	if n, ok := v.Int("capacity_weight_lbs", req.CapacityWeightLbs, false); ok {
		v.Check(n >= 0, "capacity_weight_lbs", "must be 0 or more")
		cap := int(n)
		d.CapacityWeightLbs = &cap
	}
	if n, ok := v.Int("year", req.Year, false); ok {
		v.Check(n >= 1900 && n <= 9999, "year", "must be a vehicle year")
		year := int(n)
		d.Year = &year
	}
	if n, ok := v.Int("odometer_miles", req.OdometerMiles, false); ok {
		v.Check(n >= 0, "odometer_miles", "must be 0 or more")
		miles := int(n)
		d.OdometerMiles = &miles
	}
	for field, raw := range map[string]*string{
		"vin": req.VIN, "make": req.Make, "model": req.Model, "notes": req.Notes,
	} {
		if raw != nil {
			s := strings.TrimSpace(*raw)
			v.Check(utf8.RuneCountInString(s) <= 255, field, "must be at most 255 characters")
			switch field {
			case "vin":
				d.VIN = &s
			case "make":
				d.Make = &s
			case "model":
				d.Model = &s
			case "notes":
				d.Notes = &s
			}
		}
	}
	for field, raw := range map[string]*string{
		"insurance_expiry":  req.InsuranceExpiry,
		"next_service_date": req.NextServiceDate,
	} {
		if raw != nil {
			s := strings.TrimSpace(*raw)
			v.Check(datePattern.MatchString(s), field, "must be a date as YYYY-MM-DD")
			if field == "insurance_expiry" {
				d.InsuranceExpiry = &s
			} else {
				d.NextServiceDate = &s
			}
		}
	}
	if update {
		if n, ok := v.Int("revision", req.Revision, false); ok {
			v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
			d.Revision = &n
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// DriverRequest is the body of the driver create and update routes.
type DriverRequest struct {
	Name          *string         `json:"name"`
	LicenseNumber *string         `json:"license_number"`
	PhoneNumber   *string         `json:"phone_number"`
	Status        *string         `json:"status"`
	CDLClass      *string         `json:"cdl_class"`
	CDLExpiry     *string         `json:"cdl_expiry"`
	HireDate      *string         `json:"hire_date"`
	Email         *string         `json:"email"`
	Revision      json.RawMessage `json:"revision"`
}

// DriverDraft is the parsed driver write.
type DriverDraft struct {
	Name          string
	LicenseNumber *string
	PhoneNumber   *string
	Status        DriverStatus
	CDLClass      *string
	CDLExpiry     *string
	HireDate      *string
	Email         *string
	Revision      *int64
}

func (req *DriverRequest) Parse(update bool) (*DriverDraft, error) {
	v := &httpx.Validator{}
	d := &DriverDraft{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		v.Check(len(name) > 0, "name", "is required")
		v.Check(utf8.RuneCountInString(name) <= 255, "name", "must be at most 255 characters")
		d.Name = name
	} else if !update {
		v.Required("name", "")
	}
	if req.Status != nil {
		if s, ok := ParseDriverStatus(strings.TrimSpace(*req.Status)); ok {
			d.Status = s
		} else {
			v.Check(false, "status", "must be one of: "+strings.Join(DriverStatusNames(), ", "))
		}
	} else if !update {
		d.Status = DriverStatusActive
	} else {
		v.Check(false, "status", "is required")
	}
	for field, raw := range map[string]*string{
		"license_number": req.LicenseNumber, "phone_number": req.PhoneNumber,
		"cdl_class": req.CDLClass, "cdl_expiry": req.CDLExpiry,
		"hire_date": req.HireDate, "email": req.Email,
	} {
		if raw != nil {
			s := strings.TrimSpace(*raw)
			v.Check(utf8.RuneCountInString(s) <= 255, field, "must be at most 255 characters")
			switch field {
			case "license_number":
				d.LicenseNumber = &s
			case "phone_number":
				d.PhoneNumber = &s
			case "cdl_class":
				d.CDLClass = &s
			case "cdl_expiry":
				v.Check(datePattern.MatchString(s), field, "must be a date as YYYY-MM-DD")
				d.CDLExpiry = &s
			case "hire_date":
				v.Check(datePattern.MatchString(s), field, "must be a date as YYYY-MM-DD")
				d.HireDate = &s
			case "email":
				d.Email = &s
			}
		}
	}
	if update {
		if n, ok := v.Int("revision", req.Revision, false); ok {
			v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
			d.Revision = &n
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// RouteRequest is the body of the route create route.
type RouteRequest struct {
	VehicleID     *string `json:"vehicle_id"`
	DriverID      *string `json:"driver_id"`
	ScheduledDate *string `json:"scheduled_date"`
	Notes         *string `json:"notes"`
}

// RouteDraft is the parsed route write.
type RouteDraft struct {
	VehicleID     uuid.UUID
	DriverID      uuid.UUID
	ScheduledDate string
	Notes         *string
}

func (req *RouteRequest) Parse() (*RouteDraft, error) {
	v := &httpx.Validator{}
	d := &RouteDraft{}
	if req.VehicleID != nil {
		if id, ok := v.UUID("vehicle_id", req.VehicleID, true); ok {
			d.VehicleID = id
		}
	} else {
		v.Required("vehicle_id", "")
	}
	if req.DriverID != nil {
		if id, ok := v.UUID("driver_id", req.DriverID, true); ok {
			d.DriverID = id
		}
	} else {
		v.Required("driver_id", "")
	}
	if req.ScheduledDate != nil {
		date := strings.TrimSpace(*req.ScheduledDate)
		v.Check(datePattern.MatchString(date), "scheduled_date", "must be a date as YYYY-MM-DD")
		d.ScheduledDate = date
	} else {
		v.Required("scheduled_date", "")
	}
	if req.Notes != nil {
		notes := strings.TrimSpace(*req.Notes)
		v.Check(utf8.RuneCountInString(notes) <= 2000, "notes", "must be at most 2000 characters")
		d.Notes = &notes
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// AssignStopRequest is the body of the stop create route (an order joining a
// route). StopSequence is optional and defaults to the route's next position.
type AssignStopRequest struct {
	RouteID              *string         `json:"route_id"`
	OrderID              *string         `json:"order_id"`
	StopSequence         json.RawMessage `json:"stop_sequence"`
	DeliveryInstructions *string         `json:"delivery_instructions"`
}

// AssignStopDraft is the parsed stop create.
type AssignStopDraft struct {
	RouteID              uuid.UUID
	OrderID              uuid.UUID
	StopSequence         *int
	DeliveryInstructions *string
	// Geocoded is filled by the service before the transaction opens, so the
	// assign reads no HTTP path while the route is locked.
	Geocoded *LatLng
}

func (req *AssignStopRequest) Parse() (*AssignStopDraft, error) {
	v := &httpx.Validator{}
	d := &AssignStopDraft{}
	if req.RouteID != nil {
		if id, ok := v.UUID("route_id", req.RouteID, true); ok {
			d.RouteID = id
		}
	} else {
		v.Required("route_id", "")
	}
	if req.OrderID != nil {
		if id, ok := v.UUID("order_id", req.OrderID, true); ok {
			d.OrderID = id
		}
	} else {
		v.Required("order_id", "")
	}
	if n, ok := v.Int("stop_sequence", req.StopSequence, false); ok {
		v.Check(n >= 1, "stop_sequence", "must be 1 or more")
		seq := int(n)
		d.StopSequence = &seq
	}
	if req.DeliveryInstructions != nil {
		s := strings.TrimSpace(*req.DeliveryInstructions)
		v.Check(utf8.RuneCountInString(s) <= 2000, "delivery_instructions", "must be at most 2000 characters")
		d.DeliveryInstructions = &s
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// StopTransitionRequest is the body of the stop transitions route: the
// target status, the revision precondition and the proof of delivery a
// delivered or partial stop requires.
type StopTransitionRequest struct {
	To               *string         `json:"to"`
	Revision         json.RawMessage `json:"revision"`
	PODProofURL      *string         `json:"pod_proof_url"`
	PODSignedBy      *string         `json:"pod_signed_by"`
	SignatureDataURL *string         `json:"signature_data_url"`
}

// StopTransitionDraft is the parsed stop transition.
type StopTransitionDraft struct {
	To               StopStatus
	Revision         *int64
	PODProofURL      *string
	PODSignedBy      *string
	SignatureDataURL *string
}

func (req *StopTransitionRequest) Parse() (*StopTransitionDraft, error) {
	v := &httpx.Validator{}
	d := &StopTransitionDraft{}
	if req.To != nil {
		if s, ok := ParseStopStatus(strings.TrimSpace(*req.To)); ok {
			d.To = s
		} else {
			v.Check(false, "to", "must be one of: "+strings.Join(StopStatusNames(), ", "))
		}
	} else {
		v.Required("to", "")
	}
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		d.Revision = &n
	}
	if req.PODProofURL != nil {
		s := strings.TrimSpace(*req.PODProofURL)
		v.Check(len(s) <= 2048, "pod_proof_url", "must be at most 2048 characters")
		d.PODProofURL = &s
	}
	if req.PODSignedBy != nil {
		s := strings.TrimSpace(*req.PODSignedBy)
		v.Check(utf8.RuneCountInString(s) <= 255, "pod_signed_by", "must be at most 255 characters")
		d.PODSignedBy = &s
	}
	if req.SignatureDataURL != nil {
		s := strings.TrimSpace(*req.SignatureDataURL)
		v.Check(len(s) <= 2048, "signature_data_url", "must be at most 2048 characters")
		d.SignatureDataURL = &s
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// RouteTransitionRequest is the body of the route transitions route.
type RouteTransitionRequest struct {
	To       *string         `json:"to"`
	Revision json.RawMessage `json:"revision"`
}

// RouteTransitionDraft is the parsed route transition.
type RouteTransitionDraft struct {
	To       RouteStatus
	Revision *int64
}

func (req *RouteTransitionRequest) Parse() (*RouteTransitionDraft, error) {
	v := &httpx.Validator{}
	d := &RouteTransitionDraft{}
	if req.To != nil {
		if s, ok := ParseRouteStatus(strings.TrimSpace(*req.To)); ok {
			d.To = s
		} else {
			v.Check(false, "to", "must be one of: "+strings.Join(RouteStatusNames(), ", "))
		}
	} else {
		v.Required("to", "")
	}
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		d.Revision = &n
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// ReorderRequest is the body of the route reorder route.
type ReorderRequest struct {
	OrderedDeliveryIDs []string `json:"ordered_delivery_ids"`
}

// ReorderDraft is the parsed reorder.
type ReorderDraft struct {
	OrderedDeliveryIDs []uuid.UUID
}

func (req *ReorderRequest) Parse() (*ReorderDraft, error) {
	v := &httpx.Validator{}
	d := &ReorderDraft{}
	v.Check(req.OrderedDeliveryIDs != nil, "ordered_delivery_ids", "is required")
	v.Check(req.OrderedDeliveryIDs != nil && len(req.OrderedDeliveryIDs) > 0,
		"ordered_delivery_ids", "must hold at least one delivery id")
	seen := map[uuid.UUID]bool{}
	for i, raw := range req.OrderedDeliveryIDs {
		field := "ordered_delivery_ids[" + strconv.Itoa(i) + "]"
		id, ok := v.UUID(field, &raw, true)
		if !ok {
			continue
		}
		v.Check(!seen[id], field, "names the same delivery twice")
		seen[id] = true
		d.OrderedDeliveryIDs = append(d.OrderedDeliveryIDs, id)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// AdjustRequest is the body of the adjust-qty route; the driver's on-site
// quantity adjustments, quantities as decimal strings (ADR 0001 section 7a).
type AdjustRequest struct {
	AdjustedBy  *string          `json:"adjusted_by"`
	Adjustments []AdjustmentLine `json:"adjustments"`
}

// AdjustmentLine is one line of an adjustment request.
type AdjustmentLine struct {
	ProductID   *string         `json:"product_id"`
	OriginalQty json.RawMessage `json:"original_qty"`
	AdjustedQty json.RawMessage `json:"adjusted_qty"`
	ReasonCode  *string         `json:"reason_code"`
	Notes       *string         `json:"notes"`
}

// AdjustDraft is the parsed adjustment write.
type AdjustDraft struct {
	AdjustedBy  uuid.UUID
	Adjustments []Adjustment
}

// Adjustment is one parsed adjustment line.
type Adjustment struct {
	ProductID   uuid.UUID
	OriginalQty httpx.Quantity
	AdjustedQty httpx.Quantity
	ReasonCode  string
	Notes       *string
}

func (req *AdjustRequest) Parse() (*AdjustDraft, error) {
	v := &httpx.Validator{}
	d := &AdjustDraft{}
	if req.AdjustedBy != nil {
		if id, ok := v.UUID("adjusted_by", req.AdjustedBy, true); ok {
			d.AdjustedBy = id
		}
	} else {
		v.Required("adjusted_by", "")
	}
	v.Check(req.Adjustments != nil, "adjustments", "is required")
	v.Check(req.Adjustments != nil && len(req.Adjustments) > 0, "adjustments", "must hold at least one line")
	for i, line := range req.Adjustments {
		path := "adjustments[" + strconv.Itoa(i) + "]"
		var adj Adjustment
		if line.ProductID != nil {
			if id, ok := v.UUID(path+".product_id", line.ProductID, true); ok {
				adj.ProductID = id
			}
		} else {
			v.Required(path+".product_id", "")
		}
		adj.OriginalQty, _ = v.Quantity(path+".original_qty", line.OriginalQty, true)
		adj.AdjustedQty, _ = v.Quantity(path+".adjusted_qty", line.AdjustedQty, true)

		if line.ReasonCode != nil {
			if code, ok := ParseAdjustReasonCode(strings.TrimSpace(*line.ReasonCode)); ok {
				adj.ReasonCode = code
			} else {
				v.Check(false, path+".reason_code", "must be one of: "+strings.Join(AdjustReasonCodeNames(), ", "))
			}
		} else {
			v.Required(path+".reason_code", "")
		}
		if line.Notes != nil {
			notes := strings.TrimSpace(*line.Notes)
			v.Check(utf8.RuneCountInString(notes) <= 2000, path+".notes", "must be at most 2000 characters")
			adj.Notes = &notes
		}
		d.Adjustments = append(d.Adjustments, adj)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}
