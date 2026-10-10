// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventRecorder writes a domain event into the transactional outbox. The
// write joins the caller's transaction when there is one, so the event and
// the mutation are one fact (ADR 0003 section 3). *outbox.Writer satisfies
// it; nil records nothing (unit tests).
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// AuditLogger writes an audit row; *audit.Logger satisfies it.
type AuditLogger interface {
	Log(ctx context.Context, entry audit.Entry) error
}

// ExposureGate is the lumber-index pre-ship gate. RequireClearForOrder
// returns a non-nil error when the order's source quote has unresolved
// index exposure, blocking assignment to a delivery route.
type ExposureGate interface {
	RequireClearForOrder(ctx context.Context, orderID uuid.UUID) error
}

// FulfilmentQueue queues a completed delivery for billing (ADR 0005 5.5). The
// order module implements it; the call runs inside the transaction that writes
// the delivered status, so a completed delivery always has its request and
// delivery completion never builds an invoice itself.
type FulfilmentQueue interface {
	EnqueueFulfilment(ctx context.Context, deliveryID, orderID uuid.UUID) error
}

// OrderReader answers what the delivery module needs to know of an order.
type OrderReader interface {
	// OrderDeliveryType is "PICKUP" or "DELIVERY".
	OrderDeliveryType(ctx context.Context, orderID uuid.UUID) (string, error)
}

// DeliveryNotifierInterface allows injecting the notification system.
type DeliveryNotifierInterface interface {
	Notify(ctx context.Context, event DeliveryEvent)
}

// DeliveryEvent mirrors notification.DeliveryEvent to avoid import cycle.
type DeliveryEvent struct {
	EventType     string
	DeliveryID    string
	OrderNumber   string
	CustomerName  string
	CustomerPhone string
	CustomerEmail string
	ETA           string
	ReceiptURL    string
}

// Event types the module writes to the outbox, last in each transaction.
// No plan record or ADR names the delivery module's events, so the
// vocabulary follows the house `<entity>.<verb|target status>` style and is
// listed in the pull request: created/updated/deleted for the fleet master
// records, created and the target status for the two lifecycle documents,
// route.updated for the writes that change a route's content (reorder,
// optimize) and delivery.adjusted for a driver's quantity adjustment.
const (
	EventVehicleCreated = "vehicle.created"
	EventVehicleUpdated = "vehicle.updated"
	EventVehicleDeleted = "vehicle.deleted"
	EventDriverCreated  = "driver.created"
	EventDriverUpdated  = "driver.updated"
	EventDriverDeleted  = "driver.deleted"

	EventRouteCreated   = "route.created"
	EventRouteUpdated   = "route.updated"
	EventRouteInTransit = "route.in_transit"
	EventRouteCompleted = "route.completed"
	EventStopCreated    = "delivery.created"
	EventStopUpdated    = "delivery.updated"
	EventStopDelivered  = "delivery.delivered"
	EventStopFailed     = "delivery.failed"
	EventStopPartial    = "delivery.partial"
	EventStopAdjusted   = "delivery.adjusted"
)

// Precondition is the client's revision: the If-Match header and the body's
// revision, both optional here; the service refuses a write with neither
// (428) and one whose revision is behind (409).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

func (p Precondition) missing() bool { return p.IfMatch == "" && p.Revision == nil }

// PODUpdate is the proof of delivery a delivered or partial stop records.
type PODUpdate struct {
	ProofURL         string
	SignedBy         string
	SignatureDataURL string
	Time             time.Time
}

// Service is the delivery module's behaviour: every mutation is one
// transaction that writes the row, its audit row and its event, in that
// order, with the event last (ADR 0003 section 2). The optimizer's routing
// and geocoding calls run before the transaction opens, the same rule the
// tax provider follows (ADR 0005 section 3).
type Service struct {
	repo         Repository
	routing      *ORSClient // nil if OpenRouteService not configured (keyless dev/demo)
	notifier     DeliveryNotifierInterface
	fulfilment   FulfilmentQueue // nil if the order module is not wired
	orders       OrderReader     // nil if the order module is not wired
	tx           TxRunner        // nil runs each write unwrapped (unit tests)
	exposureGate ExposureGate    // nil if lumber-index gating not wired
	events       EventRecorder
	audit        AuditLogger
	logger       *slog.Logger
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo, logger: slog.Default()}
}

// WithRouting sets the OpenRouteService client for route optimization and
// geocoding.
func (s *Service) WithRouting(client *ORSClient, logger *slog.Logger) {
	s.routing = client
	s.logger = logger
}

func (s *Service) routingEnabled(ctx context.Context) bool {
	return s.routing != nil && s.routing.IsConfigured(ctx)
}

// WithNotifier sets the delivery notification service.
func (s *Service) WithNotifier(n DeliveryNotifierInterface) {
	s.notifier = n
}

// WithFulfilment wires the order module: a completed delivery queues its
// fulfilment request (ADR 0005 5.5), and a pickup order is refused a stop.
func (s *Service) WithFulfilment(q FulfilmentQueue, orders OrderReader) {
	s.fulfilment = q
	s.orders = orders
}

// WithTxRunner makes every module write one transaction.
func (s *Service) WithTxRunner(tx TxRunner) *Service { s.tx = tx; return s }

// WithExposureGate wires the lumber-index pre-ship gate.
func (s *Service) WithExposureGate(gate ExposureGate) {
	s.exposureGate = gate
}

// WithOutbox wires the event writes.
func (s *Service) WithOutbox(events EventRecorder) *Service { s.events = events; return s }

// WithAudit wires the audit rows.
func (s *Service) WithAudit(a AuditLogger) *Service { s.audit = a; return s }

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

// record writes the module's event as the transaction's last statement.
func (s *Service) record(ctx context.Context, eventType, entityType string, id uuid.UUID, data map[string]any) error {
	if s.events == nil {
		return nil
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: entityType, EntityID: id, Data: payload,
	})
}

func (s *Service) log(ctx context.Context, action, entityType string, id uuid.UUID, changes map[string]any) error {
	if s.audit == nil {
		return nil
	}
	return s.audit.Log(ctx, audit.Entry{Action: action, EntityType: entityType, EntityID: id, Changes: changes})
}

func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound(ErrNotFound.Error())
	}
	return err
}

// Fleet: vehicles

func (s *Service) ListVehicles(ctx context.Context, f FleetListFilter, wantTotal bool) ([]Vehicle, bool, *int64, error) {
	return s.repo.ListVehicles(ctx, f, wantTotal)
}

func (s *Service) GetVehicle(ctx context.Context, id uuid.UUID) (*Vehicle, error) {
	v, err := s.repo.GetVehicle(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return v, nil
}

func (s *Service) CreateVehicle(ctx context.Context, d *VehicleDraft, actor string) (*Vehicle, error) {
	var out *Vehicle
	err := s.inTx(ctx, func(ctx context.Context) error {
		v := &Vehicle{
			Name: d.Name, VehicleType: d.VehicleType, LicensePlate: d.LicensePlate,
			CapacityWeightLbs: d.CapacityWeightLbs, VIN: d.VIN, Year: d.Year,
			Make: d.Make, Model: d.Model,
			InsuranceExpiry: d.InsuranceExpiry, NextServiceDate: d.NextServiceDate,
			OdometerMiles: d.OdometerMiles, Notes: d.Notes,
		}
		if err := s.repo.CreateVehicle(ctx, v); err != nil {
			return err
		}
		got, err := s.repo.GetVehicle(ctx, v.ID)
		if err != nil {
			return notFound(err)
		}
		out = got
		if err := s.log(ctx, EventVehicleCreated, "vehicle", out.ID,
			map[string]any{"name": out.Name, "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventVehicleCreated, "vehicle", out.ID,
			map[string]any{"name": out.Name, "revision": out.Revision})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) UpdateVehicle(ctx context.Context, id uuid.UUID, d *VehicleDraft, pre Precondition, actor string) (*Vehicle, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Vehicle
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockVehicle(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetVehicle(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		next := *cur
		next.Name, next.VehicleType, next.LicensePlate = d.Name, d.VehicleType, d.LicensePlate
		next.CapacityWeightLbs, next.VIN, next.Year = d.CapacityWeightLbs, d.VIN, d.Year
		next.Make, next.Model = d.Make, d.Model
		next.InsuranceExpiry, next.NextServiceDate = d.InsuranceExpiry, d.NextServiceDate
		next.OdometerMiles, next.Notes = d.OdometerMiles, d.Notes
		if err := s.repo.UpdateVehicle(ctx, &next); err != nil {
			return err
		}
		got, err := s.repo.GetVehicle(ctx, id)
		if err != nil {
			return notFound(err)
		}
		out = got
		if err := s.log(ctx, EventVehicleUpdated, "vehicle", id,
			map[string]any{"name": out.Name, "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventVehicleUpdated, "vehicle", id,
			map[string]any{"name": out.Name, "revision": out.Revision})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) DeleteVehicle(ctx context.Context, id uuid.UUID, pre Precondition, actor string) error {
	if pre.missing() {
		return httpx.PreconditionRequired("this write needs If-Match")
	}
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockVehicle(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetVehicle(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		if err := s.repo.DeleteVehicle(ctx, id); err != nil {
			return err
		}
		if err := s.log(ctx, EventVehicleDeleted, "vehicle", id,
			map[string]any{"name": cur.Name, "revision": cur.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventVehicleDeleted, "vehicle", id,
			map[string]any{"name": cur.Name, "revision": cur.Revision})
	})
}

// SetVehiclePhoto attaches a photo to a vehicle. The photo routes take no
// precondition: a photo is evidence appended to a record, and requiring a
// revision would fail the second of a run of uploads; the revision still
// moves and the new ETag is returned.
func (s *Service) SetVehiclePhoto(ctx context.Context, id uuid.UUID, url string, actor string) (*Vehicle, error) {
	var out *Vehicle
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.SetVehiclePhoto(ctx, id, url); err != nil {
			return notFound(err)
		}
		if out2, err := s.repo.GetVehicle(ctx, id); err != nil {
			return notFound(err)
		} else {
			out = out2
		}
		if err := s.log(ctx, EventVehicleUpdated, "vehicle", id,
			map[string]any{"part": "photo", "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventVehicleUpdated, "vehicle", id,
			map[string]any{"part": "photo", "revision": out.Revision})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Fleet: drivers

func (s *Service) ListDrivers(ctx context.Context, f FleetListFilter, wantTotal bool) ([]Driver, bool, *int64, error) {
	return s.repo.ListDrivers(ctx, f, wantTotal)
}

func (s *Service) GetDriver(ctx context.Context, id uuid.UUID) (*Driver, error) {
	d, err := s.repo.GetDriver(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return d, nil
}

func (s *Service) CreateDriver(ctx context.Context, d *DriverDraft, actor string) (*Driver, error) {
	var out *Driver
	err := s.inTx(ctx, func(ctx context.Context) error {
		driver := &Driver{
			Name: d.Name, LicenseNumber: d.LicenseNumber, Status: d.Status,
			PhoneNumber: d.PhoneNumber, CDLClass: d.CDLClass,
			CDLExpiry: d.CDLExpiry, HireDate: d.HireDate, Email: d.Email,
		}
		if driver.Status == "" {
			driver.Status = DriverStatusActive
		}
		if err := s.repo.CreateDriver(ctx, driver); err != nil {
			return err
		}
		got, err := s.repo.GetDriver(ctx, driver.ID)
		if err != nil {
			return notFound(err)
		}
		out = got
		if err := s.log(ctx, EventDriverCreated, "driver", out.ID,
			map[string]any{"name": out.Name, "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventDriverCreated, "driver", out.ID,
			map[string]any{"name": out.Name, "revision": out.Revision})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) UpdateDriver(ctx context.Context, id uuid.UUID, d *DriverDraft, pre Precondition, actor string) (*Driver, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Driver
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockDriver(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetDriver(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		next := *cur
		next.Name, next.LicenseNumber = d.Name, d.LicenseNumber
		next.Status, next.PhoneNumber = d.Status, d.PhoneNumber
		next.CDLClass, next.CDLExpiry, next.HireDate, next.Email = d.CDLClass, d.CDLExpiry, d.HireDate, d.Email
		if err := s.repo.UpdateDriver(ctx, &next); err != nil {
			return err
		}
		got, err := s.repo.GetDriver(ctx, id)
		if err != nil {
			return notFound(err)
		}
		out = got
		if err := s.log(ctx, EventDriverUpdated, "driver", id,
			map[string]any{"name": out.Name, "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventDriverUpdated, "driver", id,
			map[string]any{"name": out.Name, "revision": out.Revision})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) DeleteDriver(ctx context.Context, id uuid.UUID, pre Precondition, actor string) error {
	if pre.missing() {
		return httpx.PreconditionRequired("this write needs If-Match")
	}
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockDriver(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetDriver(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		if err := s.repo.DeleteDriver(ctx, id); err != nil {
			return err
		}
		if err := s.log(ctx, EventDriverDeleted, "driver", id,
			map[string]any{"name": cur.Name, "revision": cur.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventDriverDeleted, "driver", id,
			map[string]any{"name": cur.Name, "revision": cur.Revision})
	})
}

// SetDriverPhoto attaches a photo to a driver; see SetVehiclePhoto for why
// it takes no precondition.
func (s *Service) SetDriverPhoto(ctx context.Context, id uuid.UUID, url string, actor string) (*Driver, error) {
	var out *Driver
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.SetDriverPhoto(ctx, id, url); err != nil {
			return notFound(err)
		}
		if out2, err := s.repo.GetDriver(ctx, id); err != nil {
			return notFound(err)
		} else {
			out = out2
		}
		if err := s.log(ctx, EventDriverUpdated, "driver", id,
			map[string]any{"part": "photo", "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventDriverUpdated, "driver", id,
			map[string]any{"part": "photo", "revision": out.Revision})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Routes

func (s *Service) ListRoutes(ctx context.Context, f RouteListFilter, wantStops, wantTotal bool) ([]Route, bool, *int64, error) {
	items, more, total, err := s.repo.ListRoutes(ctx, f, wantTotal)
	if err != nil {
		return nil, false, nil, err
	}
	if wantStops && len(items) > 0 {
		ids := make([]uuid.UUID, 0, len(items))
		for i := range items {
			ids = append(ids, items[i].ID)
		}
		stops, err := s.repo.ListStopsForRoutes(ctx, ids)
		if err != nil {
			return nil, false, nil, err
		}
		for i := range items {
			items[i].Stops = stops[items[i].ID]
			if items[i].Stops == nil {
				items[i].Stops = []Stop{}
			}
		}
	}
	return items, more, total, nil
}

func (s *Service) GetRoute(ctx context.Context, id uuid.UUID) (*Route, error) {
	route, err := s.repo.GetRoute(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return route, nil
}

func (s *Service) CreateRoute(ctx context.Context, d *RouteDraft, actor string) (*Route, error) {
	var out *Route
	err := s.inTx(ctx, func(ctx context.Context) error {
		vehicleID := d.VehicleID
		driverID := d.DriverID
		route := &Route{
			VehicleID: &vehicleID, DriverID: &driverID,
			ScheduledDate: d.ScheduledDate, Status: RouteStatusDraft, Notes: d.Notes,
		}
		if err := s.repo.CreateRoute(ctx, route); err != nil {
			return err
		}
		got, err := s.repo.GetRoute(ctx, route.ID)
		if err != nil {
			return notFound(err)
		}
		out = got
		if err := s.log(ctx, EventRouteCreated, "route", out.ID,
			map[string]any{"scheduled_date": out.ScheduledDate, "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventRouteCreated, "route", out.ID,
			map[string]any{"scheduled_date": out.ScheduledDate, "status": "draft", "revision": out.Revision})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TransitionRoute moves a route through its lifecycle: dispatch (to
// in_transit, from draft or scheduled) and complete (to completed, when
// every stop is terminal and there is at least one).
func (s *Service) TransitionRoute(ctx context.Context, id uuid.UUID, d *RouteTransitionDraft, pre Precondition, actor string) (*Route, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Route
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRoute(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetRoute(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		var event string
		switch d.To {
		case RouteStatusInTransit:
			if cur.Status != RouteStatusDraft && cur.Status != RouteStatusScheduled {
				return httpx.InvalidStateTransition("cannot dispatch a route in status "+string(cur.Status),
					httpx.Blocker("invalid_state", "only a draft or scheduled route can be dispatched"))
			}
			event = EventRouteInTransit
		case RouteStatusCompleted:
			stops, _, err := s.repo.ListDeliveriesByRoute(ctx, id, StopListFilter{Limit: 200})
			if err != nil {
				return err
			}
			if len(stops) == 0 {
				return httpx.InvalidStateTransition("a route with no stops cannot be completed",
					httpx.Blocker("route_empty", "the route holds no stops"))
			}
			for _, stop := range stops {
				if stop.Status != StopStatusDelivered && stop.Status != StopStatusFailed && stop.Status != StopStatusPartial {
					return httpx.InvalidStateTransition("cannot complete a route with a stop still "+string(stop.Status),
						httpx.Blocker("stop_not_terminal", "every stop must be delivered, failed or partial before the route completes"))
				}
			}
			if cur.Status == RouteStatusCancelled {
				return httpx.InvalidStateTransition("cannot complete a cancelled route",
					httpx.Blocker("invalid_state", "a cancelled route is terminal"))
			}
			event = EventRouteCompleted
		default:
			return httpx.InvalidStateTransition("a route cannot move to "+string(d.To),
				httpx.Blocker("invalid_state", "this module dispatches and completes routes only"))
		}
		if err := s.repo.UpdateRouteStatus(ctx, id, d.To); err != nil {
			return err
		}
		got, err := s.repo.GetRoute(ctx, id)
		if err != nil {
			return notFound(err)
		}
		out = got
		if err := s.log(ctx, event, "route", id,
			map[string]any{"from_status": string(cur.Status), "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, event, "route", id,
			map[string]any{"from_status": routeStatusNames[cur.Status], "status": routeStatusNames[d.To], "revision": out.Revision})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReorderStops renumbers a route's stops to the request's order. The list
// must name every stop of the route exactly once.
func (s *Service) ReorderStops(ctx context.Context, routeID uuid.UUID, d *ReorderDraft, pre Precondition, actor string) (*Route, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Route
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRoute(ctx, routeID); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetRoute(ctx, routeID)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		stops, _, err := s.repo.ListDeliveriesByRoute(ctx, routeID, StopListFilter{Limit: 200})
		if err != nil {
			return err
		}
		onRoute := map[uuid.UUID]bool{}
		for _, stop := range stops {
			onRoute[stop.ID] = true
		}
		if len(d.OrderedDeliveryIDs) != len(stops) {
			return httpx.BadRequest("the reorder list must name every stop of the route exactly once",
				httpx.FieldError{Field: "ordered_delivery_ids",
					Message: fmt.Sprintf("the route holds %d stops and the list names %d", len(stops), len(d.OrderedDeliveryIDs))})
		}
		for i, id := range d.OrderedDeliveryIDs {
			if !onRoute[id] {
				return httpx.BadRequest("the reorder list names a stop that is not on this route",
					httpx.FieldError{Field: fmt.Sprintf("ordered_delivery_ids[%d]", i), Message: "this delivery is not a stop of the route"})
			}
		}
		if err := s.repo.ReorderRouteDeliveries(ctx, routeID, d.OrderedDeliveryIDs); err != nil {
			return err
		}
		if err := s.repo.TouchRoute(ctx, routeID); err != nil {
			return err
		}
		got, err := s.repo.GetRoute(ctx, routeID)
		if err != nil {
			return notFound(err)
		}
		out = got
		if err := s.log(ctx, EventRouteUpdated, "route", routeID,
			map[string]any{"part": "stops", "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventRouteUpdated, "route", routeID,
			map[string]any{"part": "stops", "revision": out.Revision})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OptimizeRoute reorders the route's stops through the routing service and
// persists the new order and the per-stop ETAs. The routing and geocoding
// calls run before the transaction opens; inside it the route's revision is
// re-checked, so a concurrent write answers 409 and the client re-optimizes.
func (s *Service) OptimizeRoute(ctx context.Context, routeID uuid.UUID, pre Precondition, actor string) (*Route, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	cur, err := s.repo.GetRoute(ctx, routeID)
	if err != nil {
		return nil, notFound(err)
	}
	if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
		return nil, err
	}
	deliveries, _, err := s.repo.ListDeliveriesByRoute(ctx, routeID, StopListFilter{Limit: 200})
	if err != nil {
		return nil, err
	}
	if len(deliveries) == 0 {
		return cur, nil
	}

	var stops []LatLng
	stopDeliveries := make([]*Stop, 0, len(deliveries)) // stopDeliveries[i] owns stops[i]
	geocodeCache := make(map[uuid.UUID]*LatLng)         // dedup repeat geocodes within one run
	for i := range deliveries {
		d := &deliveries[i]
		if d.Latitude == nil || d.Longitude == nil {
			coord, cached := geocodeCache[d.OrderID]
			if !cached {
				coord = s.geocodeDeliveryOnDemand(ctx, d)
				geocodeCache[d.OrderID] = coord
			} else if coord != nil {
				// Same order already geocoded this run, reuse it, persisting
				// to this delivery row too rather than re-hitting the geocoder.
				if err := s.repo.SetDeliveryLatLng(ctx, d.ID, coord.Lat, coord.Lng); err != nil {
					s.logger.Warn("OptimizeRoute: failed to persist cached geocode", "delivery_id", d.ID, "error", err)
				}
			}
			if coord == nil {
				continue // still no coords — excluded from optimization, not dropped from the route
			}
			d.Latitude, d.Longitude = &coord.Lat, &coord.Lng
		}
		stops = append(stops, LatLng{Lat: *d.Latitude, Lng: *d.Longitude})
		stopDeliveries = append(stopDeliveries, d)
	}
	if len(stops) == 0 {
		s.logger.Warn("OptimizeRoute: no geocoded stops to optimize", "route_id", routeID)
		return cur, nil
	}

	var result *RouteOptimizationResult
	if s.routingEnabled(ctx) {
		origin := s.resolveBranchOrigin(ctx, routeID, stops)
		result, err = s.routing.OptimizeRoute(ctx, origin, stops)
		if err != nil {
			s.logger.Warn("ORS optimization failed, using mock fallback", "error", err)
			result = MockOptimizeRoute(stops)
		}
	} else {
		result = MockOptimizeRoute(stops)
	}

	// Map optimized stop indices (into stops, aligned with stopDeliveries)
	// back to delivery IDs, then append any deliveries the optimizer didn't
	// return so every stop survives the reorder.
	var reorderedIDs []uuid.UUID
	seen := make(map[uuid.UUID]bool, len(deliveries))
	for _, stopIdx := range result.OptimizedOrder {
		if stopIdx < 0 || stopIdx >= len(stopDeliveries) {
			continue
		}
		id := stopDeliveries[stopIdx].ID
		if seen[id] {
			continue
		}
		reorderedIDs = append(reorderedIDs, id)
		seen[id] = true
	}
	for i := range deliveries {
		if !seen[deliveries[i].ID] {
			reorderedIDs = append(reorderedIDs, deliveries[i].ID)
			seen[deliveries[i].ID] = true
		}
	}

	etas := make(map[uuid.UUID]time.Time, len(result.Legs))
	for _, leg := range result.Legs {
		if leg.StopIndex < 0 || leg.StopIndex >= len(stopDeliveries) || leg.ETA == "" {
			continue
		}
		eta, parseErr := time.Parse(time.RFC3339, leg.ETA)
		if parseErr != nil {
			continue
		}
		etas[stopDeliveries[leg.StopIndex].ID] = eta
	}

	var out *Route
	txErr := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRoute(ctx, routeID); err != nil {
			return notFound(err)
		}
		fresh, err := s.repo.GetRoute(ctx, routeID)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(fresh.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		if len(reorderedIDs) > 0 {
			if err := s.repo.ReorderRouteDeliveries(ctx, routeID, reorderedIDs); err != nil {
				return err
			}
			if err := s.repo.TouchRoute(ctx, routeID); err != nil {
				return err
			}
		}
		for id, eta := range etas {
			if err := s.repo.SetDeliveryETA(ctx, id, eta); err != nil {
				s.logger.Warn("OptimizeRoute: failed to persist ETA", "delivery_id", id, "error", err)
			}
		}
		got, err := s.repo.GetRoute(ctx, routeID)
		if err != nil {
			return notFound(err)
		}
		out = got
		if err := s.log(ctx, EventRouteUpdated, "route", routeID,
			map[string]any{"part": "stops", "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventRouteUpdated, "route", routeID,
			map[string]any{"part": "stops", "revision": out.Revision})
	})
	if txErr != nil {
		return nil, txErr
	}
	s.logger.Info("Route optimized",
		"route_id", routeID,
		"stops", len(stops),
		"total_duration_mins", result.TotalDurationMins,
	)
	return out, nil
}

// resolveBranchOrigin returns the coordinates the optimizer should use as the
// vehicle start/end for a route: the route's branch location. Branch
// coordinates are backfilled lazily. Every failure path falls back to the
// centroid of the route's stops. Only called on the keyed path.
func (s *Service) resolveBranchOrigin(ctx context.Context, routeID uuid.UUID, stops []LatLng) LatLng {
	fallback := centroid(stops)

	branchID, err := s.repo.GetRouteBranchID(ctx, routeID)
	if err != nil {
		s.logger.Warn("route origin: could not resolve branch, routing from stop centroid", "route_id", routeID, "error", err)
		return fallback
	}
	origin, err := s.repo.GetBranchOrigin(ctx, branchID)
	if err != nil {
		s.logger.Warn("route origin: could not load branch, routing from stop centroid", "branch_id", branchID, "error", err)
		return fallback
	}
	if origin.Latitude != nil && origin.Longitude != nil {
		return LatLng{Lat: *origin.Latitude, Lng: *origin.Longitude}
	}
	if origin.Address == "" {
		s.logger.Warn("route origin: branch has no address to geocode, routing from stop centroid", "branch_id", branchID)
		return fallback
	}
	gc, gErr := s.routing.Geocode(ctx, origin.Address)
	if gErr != nil {
		s.logger.Warn("route origin: branch geocode failed, routing from stop centroid",
			"branch_id", branchID, "address", origin.Address, "error", gErr)
		return fallback
	}
	if err := s.repo.SetBranchLatLng(ctx, branchID, gc.LatLng.Lat, gc.LatLng.Lng); err != nil {
		s.logger.Warn("route origin: failed to persist branch geocode", "branch_id", branchID, "error", err)
	}
	s.logger.Info("route origin: backfilled branch coordinates",
		"branch_id", branchID, "address", origin.Address, "confidence", gc.Confidence)
	return gc.LatLng
}

// centroid returns the average of the given coordinates.
func centroid(points []LatLng) LatLng {
	if len(points) == 0 {
		return LatLng{}
	}
	var sumLat, sumLng float64
	for _, p := range points {
		sumLat += p.Lat
		sumLng += p.Lng
	}
	n := float64(len(points))
	return LatLng{Lat: sumLat / n, Lng: sumLng / n}
}

// geocodeOrderStop resolves a delivery stop's coordinates for an order. With
// an ORS key configured it geocodes the order's customer address; with no key
// (dev/demo) it returns deterministic mock coordinates so stops still appear
// on the map. Returns nil only when a keyed geocode is attempted and fails.
func (s *Service) geocodeOrderStop(ctx context.Context, orderID uuid.UUID) *LatLng {
	if !s.routingEnabled(ctx) {
		ll := mockGeocode(orderID)
		return &ll
	}

	address, err := s.repo.GetOrderDeliveryAddress(ctx, orderID)
	if err != nil || address == "" {
		s.logger.Warn("geocode: no delivery address for order; leaving stop uncoordinated",
			"order_id", orderID, "error", err)
		return nil
	}
	gc, err := s.routing.Geocode(ctx, address)
	if err != nil {
		s.logger.Error("geocode: failed to resolve delivery address; leaving stop uncoordinated",
			"order_id", orderID, "address", address, "error", err)
		return nil
	}
	if gc.LowConfidence() {
		s.logger.Warn("geocode: low-confidence delivery match — review before dispatch",
			"order_id", orderID, "address", address, "matched", gc.Label,
			"confidence", gc.Confidence, "match_type", gc.MatchType)
	}
	return &gc.LatLng
}

// geocodeDeliveryOnDemand geocodes a delivery that is missing coordinates at
// optimize time and best-effort persists the result so the stop appears on
// the map afterwards.
func (s *Service) geocodeDeliveryOnDemand(ctx context.Context, d *Stop) *LatLng {
	coord := s.geocodeOrderStop(ctx, d.OrderID)
	if coord == nil {
		return nil
	}
	if err := s.repo.SetDeliveryLatLng(ctx, d.ID, coord.Lat, coord.Lng); err != nil {
		s.logger.Warn("OptimizeRoute: failed to persist on-demand geocode", "delivery_id", d.ID, "error", err)
	}
	return coord
}

// Stops

// routeVehicle returns the vehicle a route points at, or nil when the
// route has no vehicle assigned (a legacy row, or a fresh row awaiting
// dispatch). The assign's capacity warning treats a vehicle-less route
// as not eligible for the warning rather than as an error.
func (s *Service) routeVehicle(ctx context.Context, route *Route) (*Vehicle, error) {
	if route.VehicleID == nil {
		return nil, nil
	}
	return s.repo.GetVehicle(ctx, *route.VehicleID)
}

// ListDeliveries lists a route's stops: the route itself is read behind the
// wall first, so a caller held to another branch gets the same 404 reading
// the route's stops as reading the route.
func (s *Service) ListDeliveries(ctx context.Context, routeID uuid.UUID, f StopListFilter) ([]Stop, bool, error) {
	if _, err := s.repo.GetRoute(ctx, routeID); err != nil {
		return nil, false, notFound(err)
	}
	return s.repo.ListDeliveriesByRoute(ctx, routeID, f)
}

// CountDeliveries counts a route's stops behind the same wall.
func (s *Service) CountDeliveries(ctx context.Context, routeID uuid.UUID) (int64, error) {
	if _, err := s.repo.GetRoute(ctx, routeID); err != nil {
		return 0, notFound(err)
	}
	return s.repo.CountDeliveriesByRoute(ctx, routeID)
}

func (s *Service) GetDelivery(ctx context.Context, id uuid.UUID) (*Stop, error) {
	d, err := s.repo.GetDelivery(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return d, nil
}

// AssignOrderToRoute adds an order as a stop on a route. A pickup
// (will-call) order is never routed (ADR 0005 5.5), an order with unresolved
// lumber-index exposure is refused, and a route the caller cannot see behind
// the branch wall is the same 404 as reading it. Inside the transaction the
// route row is locked FOR UPDATE so the read of its status and the read of
// the next stop sequence race no other writer; a route already completed or
// cancelled is refused with 409 invalid_state_transition, and the route's
// revision moves on a successful assign.
func (s *Service) AssignOrderToRoute(ctx context.Context, d *AssignStopDraft, actor string) (*Stop, *CapacityWarning, error) {
	// The order's branch is checked through the wall up front (PR 70 review
// round 1 P3-4): a cross-branch caller must see the same 404 as reading a
// cross-branch route, not a 500 from the post-insert walled read.
	if _, err := s.repo.GetOrderBranchID(ctx, d.OrderID); err != nil {
		return nil, nil, notFound(err)
	}
	if s.orders != nil {
		dt, err := s.orders.OrderDeliveryType(ctx, d.OrderID)
		if err != nil {
			return nil, nil, err
		}
		if dt == "PICKUP" {
			return nil, nil, &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
				Message: "a pickup order is never routed",
				Details: []httpx.FieldError{httpx.Blocker("pickup_order", "this is a will-call order: the customer collects it at the branch, so it takes no route or stop")}}
		}
	}
	if s.exposureGate != nil {
		if err := s.exposureGate.RequireClearForOrder(ctx, d.OrderID); err != nil {
			return nil, nil, err
		}
	}
	route, err := s.repo.GetRoute(ctx, d.RouteID)
	if err != nil {
		return nil, nil, notFound(err)
	}

	// The capacity warning is soft: the assignment still happens.
	var warning *CapacityWarning
	vehicle, err := s.routeVehicle(ctx, route)
	if err == nil && vehicle != nil && vehicle.CapacityWeightLbs != nil && *vehicle.CapacityWeightLbs > 0 {
		currentLoad, _ := s.repo.GetRouteLoadWeight(ctx, d.RouteID)
		orderWeight, _ := s.repo.GetOrderEstimatedWeight(ctx, d.OrderID)
		totalAfter := currentLoad + orderWeight
		if totalAfter > float64(*vehicle.CapacityWeightLbs) {
			warning = &CapacityWarning{
				VehicleCapacityLbs: float64(*vehicle.CapacityWeightLbs),
				CurrentLoadLbs:     currentLoad,
				OrderWeightLbs:     orderWeight,
				TotalAfterLbs:      totalAfter,
			}
		}
	}

	// The geocode is an HTTP call on the keyed path, so it runs before the
	// transaction opens (the same rule as the tax provider, ADR 0005
	// section 3).
	if coord := s.geocodeOrderStop(ctx, d.OrderID); coord != nil {
		d.Geocoded = coord
	}

	var out *Stop
	txErr := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRoute(ctx, d.RouteID); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetRoute(ctx, d.RouteID)
		if err != nil {
			return notFound(err)
		}
		if cur.Status == RouteStatusCompleted || cur.Status == RouteStatusCancelled {
			return httpx.InvalidStateTransition("cannot assign to a route already "+string(cur.Status),
				httpx.Blocker("invalid_state", "a route in a terminal status takes no more stops"))
		}
		stop := &Stop{
			RouteID: &d.RouteID,
			OrderID: d.OrderID,
			// The default position starts at 1, the least the input parse
			// accepts from a client, so an empty route's first stop is 1 and
			// not the column's legacy 0.
			StopSequence:         1,
			Status:               StopStatusPending,
			DeliveryInstructions: d.DeliveryInstructions,
		}
		if d.Geocoded != nil {
			stop.Latitude, stop.Longitude = &d.Geocoded.Lat, &d.Geocoded.Lng
		}
		if d.StopSequence != nil {
			stop.StopSequence = *d.StopSequence
		} else {
			stops, _, err := s.repo.ListDeliveriesByRoute(ctx, d.RouteID, StopListFilter{Limit: 200})
			if err != nil {
				return err
			}
			for _, existing := range stops {
				if existing.StopSequence >= stop.StopSequence {
					stop.StopSequence = existing.StopSequence + 1
				}
			}
		}
		if err := s.repo.CreateDelivery(ctx, stop); err != nil {
			return err
		}
		// An assign changes the route document (its stops), so its
		// revision moves and a client holding a stale one sees 409.
		if err := s.repo.TouchRoute(ctx, d.RouteID); err != nil {
			return err
		}
		got, err := s.repo.GetDelivery(ctx, stop.ID)
		if err != nil {
			return notFound(err)
		}
		out = got
		if err := s.log(ctx, EventStopCreated, "delivery", out.ID,
			map[string]any{"route_id": d.RouteID, "order_id": d.OrderID, "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventStopCreated, "delivery", out.ID,
			map[string]any{"route_id": d.RouteID, "order_id": d.OrderID, "status": "pending", "revision": out.Revision})
	})
	if txErr != nil {
		return nil, nil, txErr
	}
	return out, warning, nil
}

// TransitionStop completes a stop: delivered, failed or partial, with the
// proof of delivery a delivered or partial stop requires. A delivered stop
// queues its order's fulfilment request inside the same transaction (ADR
// 0005 5.5).
func (s *Service) TransitionStop(ctx context.Context, id uuid.UUID, d *StopTransitionDraft, pre Precondition, actor string) (*Stop, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Stop
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockDelivery(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetDelivery(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		if d.To != StopStatusDelivered && d.To != StopStatusFailed && d.To != StopStatusPartial {
			return httpx.InvalidStateTransition("a stop cannot move to "+string(d.To),
				httpx.Blocker("invalid_state", "this module completes stops: delivered, failed or partial"))
		}
		if cur.Status != StopStatusPending && cur.Status != StopStatusOutForDelivery {
			return httpx.InvalidStateTransition("cannot complete a stop already "+string(cur.Status),
				httpx.Blocker("invalid_state", "the stop is already in a terminal status"))
		}
		var pod *PODUpdate
		if d.To == StopStatusDelivered || d.To == StopStatusPartial {
			v := &httpx.Validator{}
			if d.PODProofURL == nil {
				v.Check(false, "pod_proof_url", "is required to complete a delivery")
			}
			if d.PODSignedBy == nil {
				v.Check(false, "pod_signed_by", "is required to complete a delivery")
			}
			if err := v.Err(); err != nil {
				return err
			}
			pod = &PODUpdate{ProofURL: *d.PODProofURL, SignedBy: *d.PODSignedBy, Time: time.Now().UTC()}
			if d.SignatureDataURL != nil {
				pod.SignatureDataURL = *d.SignatureDataURL
			}
		}
		if err := s.repo.UpdateDeliveryStatus(ctx, id, d.To, pod); err != nil {
			return err
		}
		if d.To == StopStatusDelivered && s.fulfilment != nil {
			if err := s.fulfilment.EnqueueFulfilment(ctx, id, cur.OrderID); err != nil {
				return err
			}
		}
		got, err := s.repo.GetDelivery(ctx, id)
		if err != nil {
			return notFound(err)
		}
		out = got
		event := EventStopDelivered
		switch d.To {
		case StopStatusFailed:
			event = EventStopFailed
		case StopStatusPartial:
			event = EventStopPartial
		}
		if err := s.log(ctx, event, "delivery", id,
			map[string]any{"from_status": string(cur.Status), "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, event, "delivery", id,
			map[string]any{"from_status": stopStatusNames[cur.Status], "status": stopStatusNames[d.To], "revision": out.Revision})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AdjustDeliveryQuantity records a driver's on-site quantity adjustments
// (short ships, damage): one row per line in the delivery's transaction, an
// audit row and an event, and the stop's revision moves so a client holding
// the document sees the change. Like the photo attaches, it takes no
// precondition: it records evidence of a fact already lived.
func (s *Service) AdjustDeliveryQuantity(ctx context.Context, stopID uuid.UUID, d *AdjustDraft, actor string) (*Stop, error) {
	var out *Stop
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockDelivery(ctx, stopID); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetDelivery(ctx, stopID)
		if err != nil {
			return notFound(err)
		}
		if err := s.repo.InsertQtyAdjustments(ctx, stopID, d.AdjustedBy, d.Adjustments); err != nil {
			return err
		}
		if err := s.repo.TouchDelivery(ctx, stopID); err != nil {
			return err
		}
		got, err := s.repo.GetDelivery(ctx, stopID)
		if err != nil {
			return notFound(err)
		}
		out = got
		if err := s.log(ctx, EventStopAdjusted, "delivery", stopID,
			map[string]any{"lines": len(d.Adjustments), "revision": out.Revision, "actor": actor,
				"order_id": cur.OrderID}); err != nil {
			return err
		}
		return s.record(ctx, EventStopAdjusted, "delivery", stopID,
			map[string]any{"lines": len(d.Adjustments), "revision": out.Revision})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UploadPODPhoto attaches a proof-of-delivery photo to a stop; see
// SetVehiclePhoto for why the photo routes take no precondition.
func (s *Service) UploadPODPhoto(ctx context.Context, stopID uuid.UUID, photoURL, photoType string, actor string) (*PODPhoto, *Stop, error) {
	var photo *PODPhoto
	var out *Stop
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockDelivery(ctx, stopID); err != nil {
			return notFound(err)
		}
		p := &PODPhoto{DeliveryID: stopID, PhotoURL: photoURL, PhotoType: photoType}
		if err := s.repo.SavePODPhoto(ctx, p); err != nil {
			return err
		}
		photo = p
		if err := s.repo.TouchDelivery(ctx, stopID); err != nil {
			return err
		}
		if out2, err := s.repo.GetDelivery(ctx, stopID); err != nil {
			return notFound(err)
		} else {
			out = out2
		}
		if err := s.log(ctx, EventStopUpdated, "delivery", stopID,
			map[string]any{"part": "pod_photos", "revision": out.Revision, "actor": actor}); err != nil {
			return err
		}
		return s.record(ctx, EventStopUpdated, "delivery", stopID,
			map[string]any{"part": "pod_photos", "revision": out.Revision})
	})
	if err != nil {
		return nil, nil, err
	}
	return photo, out, nil
}

// GetPODPhotos lists a stop's proof-of-delivery photos.
func (s *Service) GetPODPhotos(ctx context.Context, deliveryID uuid.UUID, f PhotoListFilter) ([]PODPhoto, bool, error) {
	return s.repo.GetPODPhotos(ctx, deliveryID, f)
}
