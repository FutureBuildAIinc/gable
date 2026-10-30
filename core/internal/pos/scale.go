// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The counter's scale link (CP-19, ruling D8): the weight ticket with its
// consecutive never reused number, the weigh in and weigh out flow whose
// net Gable computes as the difference of two gross readings (double draft:
// the net stays a software figure, never a scale tare operation, so the
// ticket says which kind of net it carries), the manual weight entry with
// its reason and its Handbook 44 zero check, and the density conversion
// that is the only bridge between the ton and the cubic yard.

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/scale"
	"github.com/gablelbm/gable/internal/units"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventLineWeighed is the counter's weight capture event, written last in
// the capture's transaction (ADR 0003).
const EventLineWeighed = "pos_transaction.line_weighed"

const (
	auditTicketWeighedIn  = "pos_scale_ticket.weighed_in"
	auditTicketWeighedOut = "pos_scale_ticket.weighed_out"
	auditTicketVoided     = "pos_scale_ticket.voided"
	auditLineWeightManual = "pos_transaction.line_weight_manual"
)

// TicketDirection is the weighment kind. Storage is uppercase; the wire is
// lowercase (ADR 0001 section 6).
type TicketDirection string

const (
	DirectionWeighIn  TicketDirection = "WEIGH_IN"
	DirectionWeighOut TicketDirection = "WEIGH_OUT"
	DirectionSingle   TicketDirection = "SINGLE"
)

// MarshalText writes the lowercase wire name.
func (d TicketDirection) MarshalText() ([]byte, error) {
	return []byte(strings.ToLower(string(d))), nil
}

// TicketStatus is the ticket's lifecycle: open, completed, or void. A void
// keeps the row and its number; a ticket is never deleted.
type TicketStatus string

const (
	TicketOpen      TicketStatus = "OPEN"
	TicketCompleted TicketStatus = "COMPLETED"
	TicketVoid      TicketStatus = "VOID"
)

// MarshalText writes the lowercase wire name.
func (s TicketStatus) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(s))), nil }

// WeightSourceName is where a weight came from: the scale, or a manual
// entry. Storage is uppercase; the wire is lowercase.
type WeightSourceName string

const (
	WeightFromScale  WeightSourceName = "SCALE"
	WeightFromManual WeightSourceName = "MANUAL"
)

// MarshalText writes the lowercase wire name.
func (s WeightSourceName) MarshalText() ([]byte, error) {
	return []byte(strings.ToLower(string(s))), nil
}

// The net basis of section 4.4 of the research: Gable's weigh in and weigh
// out flow computes the net as the difference of two gross readings (double
// draft), so it is not a scale tare operation and Handbook 44 treats it as
// two weighments; a single draft entry is one gross less a tare.
const (
	NetBasisSingleDraft = "single_draft"
	NetBasisDoubleDraft = "double_draft"
)

// ManualWeightReason is one of the closed list a manual weight entry names:
// the reason is what makes the entry auditable, so it is never free text.
type ManualWeightReason struct {
	Code      string `json:"code"`
	Label     string `json:"label"`
	NeedsNote bool   `json:"needs_note"`
}

// ManualWeightReasons is the closed list.
var ManualWeightReasons = []ManualWeightReason{
	{Code: "scale_offline", Label: "The scale is not answering"},
	{Code: "scale_error", Label: "The scale is refusing readings"},
	{Code: "preweighed", Label: "The load arrived with a certified weight"},
	{Code: "other", Label: "Another reason, explained in the note", NeedsNote: true},
}

func manualReasonOf(code string) (ManualWeightReason, bool) {
	for _, r := range ManualWeightReasons {
		if r.Code == code {
			return r, true
		}
	}
	return ManualWeightReason{}, false
}

// ScaleTicket is one weight ticket on the wire: the consecutive number, the
// device that produced the reading, the gross, tare and net, the vehicle,
// the source and the basis, and the void state that replaces a delete.
type ScaleTicket struct {
	ID              uuid.UUID        `json:"id"`
	TicketNumber    string           `json:"ticket_number"`
	Revision        int64            `json:"revision"`
	BranchID        uuid.UUID        `json:"branch_id"`
	ScaleDeviceID   *uuid.UUID       `json:"scale_device_id"`
	VehicleRef      *string          `json:"vehicle_ref"`
	Direction       TicketDirection  `json:"direction"`
	Status          TicketStatus     `json:"status"`
	GrossLb         *httpx.Quantity  `json:"gross_lb"`
	TareLb          *httpx.Quantity  `json:"tare_lb"`
	NetLb           *httpx.Quantity  `json:"net_lb"`
	ReadingUnit     string           `json:"reading_unit"`
	Source          WeightSourceName `json:"source"`
	ManualReason    *string          `json:"manual_reason"`
	ManualNote      *string          `json:"manual_note"`
	NetBasis        *string          `json:"net_basis"`
	WeighInTicketID *uuid.UUID       `json:"weigh_in_ticket_id"`
	SaleLineID      *uuid.UUID       `json:"sale_line_id"`
	CapturedBy      string           `json:"captured_by"`
	CapturedAt      httpx.Timestamp  `json:"captured_at"`
	VoidReason      *string          `json:"void_reason"`
	CreatedAt       httpx.Timestamp  `json:"created_at"`
}

// ScaleDevice is the certified scale the counter reads; every reading that
// becomes money names it.
type ScaleDevice struct {
	ID            uuid.UUID `json:"id"`
	BranchID      uuid.UUID `json:"branch_id"`
	Name          string    `json:"name"`
	Protocol      string    `json:"protocol"`
	Endpoint      string    `json:"endpoint"`
	CertificateNo *string   `json:"certificate_no"`
	LocationNote  *string   `json:"location_note"`
}

// ScaleReadingWire is the scale's current indication on the wire.
type ScaleReadingWire struct {
	WeightLb *httpx.Quantity `json:"weight_lb"`
	Unit     string          `json:"unit"`
	Mode     string          `json:"mode"`
	Motion   bool            `json:"motion"`
	AtZero   bool            `json:"at_zero"`
}

// ScaleStatusWire is the scale status route's answer: whether the branch has
// a scale, the device, and the current reading or why it cannot be read.
type ScaleStatusWire struct {
	Configured bool              `json:"configured"`
	Device     *ScaleDevice      `json:"device"`
	Reading    *ScaleReadingWire `json:"reading"`
	Detail     string            `json:"detail,omitempty"`
}

// ScaleReader is the counter's read seam to the scale driver; scale.Driver
// satisfies it.
type ScaleReader interface {
	Read(ctx context.Context) (scale.Reading, error)
	ReadStable(ctx context.Context) (scale.Reading, error)
}

// WithScale wires the branch scale's driver.
func (s *Service) WithScale(d ScaleReader) *Service { s.scale = d; return s }

// scaleConflict is a 409 whose envelope code names the refusal.
func scaleConflict(code, message string) *httpx.Error {
	return &httpx.Error{Status: 409, Code: code, Message: message,
		Details: []httpx.FieldError{httpx.Blocker(code, message)}}
}

// scaleUnavailable is the 503 of a counter with no scale wired.
func scaleUnavailable(message string) *httpx.Error {
	return &httpx.Error{Status: 503, Code: "scale_not_configured", Message: message,
		Details: []httpx.FieldError{httpx.Blocker("scale_not_configured", message)}}
}

// milliToQuantity turns thousandths of a pound into the wire's scale 4
// quantity, exactly.
func milliToQuantity(milli int64) httpx.Quantity { return httpx.Quantity(milli * 10) }

// quantityToMilli turns the wire's scale 4 quantity into thousandths,
// exactly.
func quantityToMilli(q httpx.Quantity) int64 { return int64(q) / 10 }

// scaleForBranch resolves the branch's active device and the wired driver,
// or the plain refusal of a counter with no scale.
func (s *Service) scaleForBranch(ctx context.Context, branchID uuid.UUID) (*ScaleDevice, error) {
	device, err := s.repo.ActiveScaleDevice(ctx, branchID)
	if err != nil {
		return nil, err
	}
	if device == nil || s.scale == nil {
		return nil, scaleUnavailable("this counter runs no scale: the weight goes in by hand with a reason")
	}
	return device, nil
}

// readStable takes the ticket flow's reading, mapping the driver's refusal
// to the counter's refusal.
func (s *Service) readStable(ctx context.Context) (scale.Reading, error) {
	r, err := s.scale.ReadStable(ctx)
	if err != nil {
		return r, scaleConflict("scale_reading_refused", "the scale refuses the reading: "+err.Error())
	}
	return r, nil
}

// ScaleStatus is GET /pos/scale: the branch's device and its current
// indication. The reading shown here is never sold; only the ticket flow's
// stable read becomes money.
func (s *Service) ScaleStatus(ctx context.Context, registerID string) (*ScaleStatusWire, error) {
	branch, err := s.repo.GetRegisterBranch(ctx, registerID)
	if err != nil {
		return nil, err
	}
	if branch == nil {
		return nil, httpx.NotFound("register not found")
	}
	device, err := s.repo.ActiveScaleDevice(ctx, *branch)
	if err != nil {
		return nil, err
	}
	out := &ScaleStatusWire{Configured: device != nil && s.scale != nil, Device: device}
	if !out.Configured {
		out.Detail = "this counter runs no scale: the weight goes in by hand with a reason"
		return out, nil
	}
	r, err := s.scale.Read(ctx)
	if err != nil {
		out.Detail = "the scale is not answering: " + err.Error()
		return out, nil
	}
	mode := "gross"
	switch r.Mode {
	case scale.ModeTare:
		mode = "tare"
	case scale.ModeNet:
		mode = "net"
	}
	reading := &ScaleReadingWire{Unit: string(r.Unit), Mode: mode, Motion: r.Motion}
	if r.HasWeight {
		lb, err := r.PoundsMilli()
		if err != nil {
			out.Detail = err.Error()
			return out, nil
		}
		q := milliToQuantity(lb)
		reading.WeightLb = &q
		reading.AtZero = lb == 0
	}
	out.Reading = reading
	return out, nil
}

// WeighIn captures the empty truck's gross against its vehicle and opens the
// ticket the weigh out closes.
func (s *Service) WeighIn(ctx context.Context, registerID, vehicleRef string, actor string) (*ScaleTicket, error) {
	branch, err := s.repo.GetRegisterBranch(ctx, registerID)
	if err != nil {
		return nil, err
	}
	if branch == nil {
		return nil, httpx.NotFound("register not found")
	}
	vehicleRef = strings.TrimSpace(vehicleRef)
	if vehicleRef == "" {
		return nil, invalid("vehicle_ref", "is required: the ticket carries the vehicle's identification")
	}
	device, err := s.scaleForBranch(ctx, *branch)
	if err != nil {
		return nil, err
	}
	reading, err := s.readStable(ctx)
	if err != nil {
		return nil, err
	}
	lb, _ := reading.PoundsMilli()
	if lb <= 0 {
		return nil, scaleConflict("scale_reading_refused", "the scale reads zero: enter a weight above zero")
	}
	gross := milliToQuantity(lb)
	ticket := &ScaleTicket{
		BranchID: *branch, ScaleDeviceID: &device.ID, VehicleRef: &vehicleRef,
		Direction: DirectionWeighIn, Status: TicketOpen, GrossLb: &gross,
		ReadingUnit: string(reading.Unit), Source: WeightFromScale, CapturedBy: actor,
	}
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.InsertScaleTicket(ctx, ticket); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: auditTicketWeighedIn, EntityType: "pos_scale_ticket", EntityID: ticket.ID, UserID: actor,
				Changes: map[string]any{"ticket_number": ticket.TicketNumber, "vehicle_ref": vehicleRef,
					"gross_lb": gross.WireString(), "scale_device_id": device.ID}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ticket, nil
}

// WeighOutInput is the weigh out's request: the sale and line the net lands
// on, and the weigh in it nets against, by ticket or by vehicle.
type WeighOutInput struct {
	SaleID          uuid.UUID
	LineID          uuid.UUID
	WeighInTicketID *uuid.UUID
	VehicleRef      string
}

// WeighOut captures the loaded truck's gross, computes the net against the
// weigh in's gross (double draft: Gable's computation, recorded on the
// ticket), completes the weigh in, writes the weigh out ticket and puts the
// weight and its ticket number on the sale line, repriced at the weighed
// quantity.
func (s *Service) WeighOut(ctx context.Context, in WeighOutInput, ifMatch string, bodyRevision *int64, actor string) (*Sale, *ScaleTicket, error) {
	if err := s.holdSaleBranch(ctx, in.SaleID); err != nil {
		return nil, nil, err
	}
	var out *Sale
	var ticket *ScaleTicket
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, in.SaleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, in.SaleID)
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(sale.Revision, ifMatch, bodyRevision); err != nil {
			return err
		}
		if sale.Status != StatusOpen && sale.Status != StatusHeld {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot weigh onto a %s sale", sale.Status.Status()))
		}
		device, err := s.scaleForBranch(ctx, sale.BranchID)
		if err != nil {
			return err
		}

		// The weigh in, locked: by ticket, or the vehicle's latest open one.
		var weighIn *ScaleTicket
		if in.WeighInTicketID != nil {
			weighIn, err = s.repo.LockScaleTicket(ctx, *in.WeighInTicketID)
		} else {
			weighIn, err = s.repo.FindOpenWeighIn(ctx, sale.BranchID, strings.TrimSpace(in.VehicleRef))
			if err == nil && weighIn != nil {
				weighIn, err = s.repo.LockScaleTicket(ctx, weighIn.ID)
			}
		}
		if err != nil {
			return err
		}
		if weighIn == nil {
			return scaleConflict("no_open_weigh_in", "no open weigh in names that truck: weigh the empty truck first")
		}
		if weighIn.BranchID != sale.BranchID {
			return httpx.NotFound("weigh in ticket not found")
		}
		if weighIn.Direction != DirectionWeighIn {
			return httpx.InvalidStateTransition("that ticket is not a weigh in")
		}
		if weighIn.Status == TicketVoid {
			return httpx.InvalidStateTransition("the weigh in ticket is void")
		}
		if weighIn.Status != TicketOpen {
			return httpx.InvalidStateTransition("the weigh in ticket is already weighed out")
		}

		reading, err := s.readStable(ctx)
		if err != nil {
			return err
		}
		grossOutMilli, _ := reading.PoundsMilli()
		if grossOutMilli <= 0 {
			return scaleConflict("scale_reading_refused", "the scale reads zero: enter a weight above zero")
		}
		grossInMilli := quantityToMilli(*weighIn.GrossLb)
		netMilli := grossOutMilli - grossInMilli
		if netMilli <= 0 {
			return scaleConflict("net_not_positive",
				"the loaded truck weighs no more than the empty one: weigh the load again")
		}

		grossOut := milliToQuantity(grossOutMilli)
		net := milliToQuantity(netMilli)
		basis := NetBasisDoubleDraft
		ticket = &ScaleTicket{
			BranchID: sale.BranchID, ScaleDeviceID: &device.ID, VehicleRef: weighIn.VehicleRef,
			Direction: DirectionWeighOut, Status: TicketCompleted,
			GrossLb: &grossOut, TareLb: weighIn.GrossLb, NetLb: &net,
			ReadingUnit: string(reading.Unit), Source: WeightFromScale,
			NetBasis: &basis, WeighInTicketID: &weighIn.ID, CapturedBy: actor,
		}
		if err := s.repo.InsertScaleTicket(ctx, ticket); err != nil {
			return err
		}
		block := LineWeight{
			Source: WeightFromScale, GrossLb: &grossOut, TareLb: weighIn.GrossLb, NetLb: &net,
			NetBasis: &basis, ScaleDeviceID: &device.ID,
			ScaleTicketID: &ticket.ID, ScaleTicketNumber: &ticket.TicketNumber, CapturedBy: actor,
		}
		// The ticket names the line only after the line's re-add: the
		// attach removes and re-inserts the row, so an earlier link would
		// chase a deleted row.
		if err := s.attachWeight(ctx, sale, in.LineID, netMilli, block, actor); err != nil {
			return err
		}
		if err := s.repo.LinkScaleTicketLine(ctx, ticket.ID, in.LineID); err != nil {
			return err
		}
		ticket.SaleLineID = &in.LineID
		if err := s.repo.CompleteScaleTicket(ctx, weighIn.ID, weighIn.Revision); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: auditTicketWeighedOut, EntityType: "pos_scale_ticket", EntityID: ticket.ID, UserID: actor,
				Changes: map[string]any{"ticket_number": ticket.TicketNumber, "weigh_in_ticket": weighIn.TicketNumber,
					"gross_lb": grossOut.WireString(), "tare_lb": weighIn.GrossLb.WireString(), "net_lb": net.WireString(),
					"net_basis": basis, "sale_id": sale.ID, "line_id": in.LineID, "scale_device_id": device.ID}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if err := s.recordEvent(ctx, outbox.Event{Type: EventLineWeighed, EntityType: "pos_transaction", EntityID: sale.ID,
			BranchID: &sale.BranchID, Data: eventJSON(map[string]any{
				"line_id": in.LineID, "ticket_number": ticket.TicketNumber, "net_lb": net.WireString(),
				"source": "scale"})}); err != nil {
			return err
		}
		out, err = s.GetSale(ctx, sale.ID)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return out, ticket, nil
}

// ManualWeightInput is the typed weight of a manual entry.
type ManualWeightInput struct {
	WeightLb httpx.Quantity
	TareLb   *httpx.Quantity
	Reason   string
	Note     string
}

// EnterManualWeight records a weight typed by hand: the reason from the
// closed list, the Handbook 44 zero check when the scale answers, the manual
// ticket and the block on the line with the marking the documents print.
func (s *Service) EnterManualWeight(ctx context.Context, saleID, lineID uuid.UUID, in ManualWeightInput,
	ifMatch string, bodyRevision *int64, actor string) (*Sale, error) {
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return nil, err
	}
	reason, ok := manualReasonOf(in.Reason)
	if !ok {
		return nil, invalid("reason", "is not one of the manual weight reasons")
	}
	if reason.NeedsNote && strings.TrimSpace(in.Note) == "" {
		return nil, invalid("note", "is required when the reason is other")
	}
	if in.WeightLb <= 0 {
		return nil, invalid("weight_lb", "must be above zero: enter a weight above zero")
	}
	var out *Sale
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(sale.Revision, ifMatch, bodyRevision); err != nil {
			return err
		}
		if sale.Status != StatusOpen && sale.Status != StatusHeld {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot weigh onto a %s sale", sale.Status.Status()))
		}

		// Handbook 44: a manual gross or net enters only with the scale's
		// indication at zero. A scale that cannot be read cannot be checked;
		// the reason code is the clerk's attestation for exactly that case.
		if device, derr := s.repo.ActiveScaleDevice(ctx, sale.BranchID); derr != nil {
			return derr
		} else if device != nil && s.scale != nil {
			if r, rerr := s.scale.Read(ctx); rerr == nil && r.HasWeight && r.WeightMilli != 0 {
				return scaleConflict("scale_not_zero",
					"the scale does not read zero: a manual weight enters only with the scale at zero")
			}
		}

		gross := in.WeightLb
		tare := httpx.Quantity(0)
		if in.TareLb != nil {
			if *in.TareLb < 0 {
				return invalid("tare_lb", "must not be negative")
			}
			tare = *in.TareLb
		}
		net := gross - tare
		if net <= 0 {
			return invalid("weight_lb", "less the tare must leave a positive net")
		}
		basis := NetBasisSingleDraft
		ticket := &ScaleTicket{
			BranchID: sale.BranchID, VehicleRef: nil,
			Direction: DirectionSingle, Status: TicketCompleted,
			GrossLb: &gross, TareLb: &tare, NetLb: &net,
			ReadingUnit: "lb", Source: WeightFromManual,
			ManualReason: &in.Reason, NetBasis: &basis, CapturedBy: actor,
		}
		if note := strings.TrimSpace(in.Note); note != "" {
			ticket.ManualNote = &note
		}
		if err := s.repo.InsertScaleTicket(ctx, ticket); err != nil {
			return err
		}
		block := LineWeight{
			Source: WeightFromManual, GrossLb: &gross, TareLb: &tare, NetLb: &net,
			NetBasis: &basis, ScaleTicketID: &ticket.ID, ScaleTicketNumber: &ticket.TicketNumber,
			ManualReason: &in.Reason, CapturedBy: actor,
		}
		if err := s.attachWeight(ctx, sale, lineID, quantityToMilli(net), block, actor); err != nil {
			return err
		}
		if err := s.repo.LinkScaleTicketLine(ctx, ticket.ID, lineID); err != nil {
			return err
		}
		ticket.SaleLineID = &lineID
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: auditLineWeightManual, EntityType: "pos_transaction", EntityID: sale.ID, UserID: actor,
				Changes: map[string]any{"line_id": lineID, "ticket_number": ticket.TicketNumber,
					"gross_lb": gross.WireString(), "tare_lb": tare.WireString(), "net_lb": net.WireString(),
					"reason": in.Reason, "note": strings.TrimSpace(in.Note)}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if err := s.recordEvent(ctx, outbox.Event{Type: EventLineWeighed, EntityType: "pos_transaction", EntityID: sale.ID,
			BranchID: &sale.BranchID, Data: eventJSON(map[string]any{
				"line_id": lineID, "ticket_number": ticket.TicketNumber, "net_lb": net.WireString(),
				"source": "manual", "reason": in.Reason})}); err != nil {
			return err
		}
		out, err = s.GetSale(ctx, saleID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LineWeight is the block a weighed line carries.
type LineWeight struct {
	Source            WeightSourceName
	GrossLb           *httpx.Quantity
	TareLb            *httpx.Quantity
	NetLb             *httpx.Quantity
	NetBasis          *string
	ScaleDeviceID     *uuid.UUID
	ScaleTicketID     *uuid.UUID
	ScaleTicketNumber *string
	ManualReason      *string
	CapturedBy        string
}

// attachWeight writes the weighed quantity onto a cart line, repriced as a
// quantity change prices it, and writes the weight block, inside the
// caller's transaction.
func (s *Service) attachWeight(ctx context.Context, sale *Sale, lineID uuid.UUID, netMilli int64, block LineWeight, actor string) error {
	existing, err := s.repo.GetLines(ctx, sale.ID)
	if err != nil {
		return err
	}
	var old *salesdoc.Line
	for i := range existing {
		if existing[i].ID == lineID {
			old = &existing[i]
		}
	}
	if old == nil {
		return httpx.NotFound("line not found on this sale")
	}
	if old.LineType != salesdoc.LineProduct || old.ProductID == nil || old.ParentLineID != nil {
		return invalid("line_id", "is not a product line: only a product line takes a weight")
	}
	if old.WeightSource != nil {
		return scaleConflict("line_already_weighed", "the line already carries a weight: remove it and weigh again")
	}
	if old.UOM == nil {
		return invalid("line_id", "has no unit to weigh in")
	}

	refs, err := s.repo.LookupProducts(ctx, []uuid.UUID{*old.ProductID})
	if err != nil {
		return err
	}
	ref, ok := refs[old.ProductID.String()]
	if !ok {
		return invalid(lineField("product_id"), "no such product")
	}
	catalogue, err := s.repo.LookupCatalogue(ctx, []string{*old.UOM})
	if err != nil {
		return err
	}
	qty, err := quantityFromNet(netMilli, *old.UOM, &ref, catalogue)
	if err != nil {
		return err
	}

	if sale.CustomerID == nil {
		walkInID, _, err := s.repo.WalkInCustomer(ctx)
		if err != nil {
			return err
		}
		sale.WalkInID = walkInID
	}
	line, _, err := s.priceLine(ctx, sale, parsedFromLine(old, qty), actor)
	if err != nil {
		return err
	}
	line.ID = old.ID
	line.PriceAdjustedBy = old.PriceAdjustedBy
	lines, err := s.explodeAndExtend(ctx, []salesdoc.Line{line})
	if err != nil {
		return err
	}
	var places []int
	for i := range existing {
		if existing[i].ID == old.ID || (existing[i].ParentLineID != nil && *existing[i].ParentLineID == old.ID) {
			places = append(places, existing[i].Position)
		}
	}
	for i := range lines {
		if i < len(places) {
			lines[i].Position = places[i]
		} else {
			lines[i].Position = places[len(places)-1] + (i - len(places) + 1)
		}
	}
	if err := s.repo.RemoveLine(ctx, sale.ID, old.ID); err != nil {
		return err
	}
	if err := s.repo.AddLines(ctx, sale.ID, lines); err != nil {
		return err
	}
	if err := s.repo.SetLineWeight(ctx, lineID, block); err != nil {
		return err
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action: auditLineQuantityChanged, EntityType: "pos_transaction", EntityID: sale.ID, UserID: actor,
			Changes: map[string]any{"line_id": old.ID, "product_id": old.ProductID,
				"from_quantity": old.Quantity.WireString(), "to_quantity": qty.WireString(),
				"weighed": true}}); err != nil {
			return fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	return s.recalcTotals(ctx, sale)
}

// quantityFromNet turns the net weight into the line's quantity in its sale
// unit: the catalogue's exact ratios for a weight unit, the product's
// density for a volume unit (the only bridge across the dimension boundary),
// and a plain refusal for anything else.
func quantityFromNet(netMilli int64, uom string, ref *salesdoc.ProductRef, catalogue map[string]units.CatalogueUnit) (httpx.Quantity, error) {
	u, known := catalogue[uom]
	if !known {
		return 0, invalid(lineField("uom"), uom+" is not a unit of the catalogue")
	}
	netLb := new(big.Rat).SetFrac64(netMilli, 1000)
	var qty *big.Rat
	switch u.Dimension {
	case units.DimWeight:
		// refPerUnit is the pounds one of this unit is (LBS 1, CWT 100,
		// TON 2000).
		lbPerUnit, ok := catalogueRefPerUnit(u)
		if !ok {
			return 0, scaleConflict("weight_unit_mismatch", uom+" has no standard weight")
		}
		qty = new(big.Rat).Quo(netLb, lbPerUnit)
	case units.DimVolume:
		if ref.DensityLbPerCY == nil || *ref.DensityLbPerCY <= 0 {
			return 0, scaleConflict("density_required",
				"the product has no density: a weight cannot be priced by volume without the pounds per cubic yard")
		}
		density := new(big.Rat).SetFrac64(int64(*ref.DensityLbPerCY), 10000)
		cfPerUnit, ok := catalogueRefPerUnit(u)
		if !ok {
			return 0, scaleConflict("weight_unit_mismatch", uom+" has no standard volume")
		}
		// yards = pounds / density; the unit is yards x (its cubic feet / 27).
		yards := new(big.Rat).Quo(netLb, density)
		qty = new(big.Rat).Mul(yards, new(big.Rat).Quo(cfPerUnit, new(big.Rat).SetInt64(27)))
	default:
		return 0, scaleConflict("weight_unit_mismatch",
			"a "+strings.ToLower(string(u.Dimension))+" unit cannot take a weight: sell by a weight or volume unit")
	}
	return roundRatToQuantity(qty)
}

// catalogueRefPerUnit is the catalogue row's standard size as a rational:
// how much of the dimension's reference unit one of this unit is.
func catalogueRefPerUnit(u units.CatalogueUnit) (*big.Rat, bool) {
	if !u.HasStdSize {
		return nil, false
	}
	return new(big.Rat).SetFrac64(int64(u.StdRefQty), int64(u.StdUnitQty)), true
}

// roundRatToQuantity rounds a rational to the wire's scale 4, half away
// from zero, refusing a figure past the quantity bound. The ticket carries
// the exact reading; this is the priced quantity.
func roundRatToQuantity(r *big.Rat) (httpx.Quantity, error) {
	scaled := new(big.Rat).Mul(r, big.NewRat(10000, 1))
	num, den := scaled.Num(), scaled.Denom()
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if rem.Sign() != 0 {
		twice := new(big.Int).Abs(new(big.Int).Mul(rem, big.NewInt(2)))
		if twice.Cmp(den) >= 0 {
			if q.Sign() >= 0 {
				q.Add(q, big.NewInt(1))
			} else {
				q.Sub(q, big.NewInt(1))
			}
		}
	}
	if !q.IsInt64() {
		return 0, scaleConflict("weight_unit_mismatch", "the weighed quantity is past the quantity bound")
	}
	v := q.Int64()
	if v > int64(httpx.QuantityMax) || v < -int64(httpx.QuantityMax) {
		return 0, scaleConflict("weight_unit_mismatch", "the weighed quantity is past the quantity bound of 99999999.9999")
	}
	return httpx.Quantity(v), nil
}

// ListScaleTickets is the counter's ticket list, newest first.
func (s *Service) ListScaleTickets(ctx context.Context, registerID, status, vehicleRef string) ([]ScaleTicket, error) {
	branch, err := s.repo.GetRegisterBranch(ctx, registerID)
	if err != nil {
		return nil, err
	}
	if branch == nil {
		return nil, httpx.NotFound("register not found")
	}
	switch status {
	case "", "open", "completed", "void":
	default:
		return nil, invalid("status", "is not a ticket status")
	}
	return s.repo.ListScaleTickets(ctx, *branch, status, strings.TrimSpace(vehicleRef))
}

// GetScaleTicket reads one ticket, held to the caller's branch.
func (s *Service) GetScaleTicket(ctx context.Context, id uuid.UUID) (*ScaleTicket, error) {
	ticket, err := s.repo.GetScaleTicket(ctx, id)
	if err != nil {
		return nil, err
	}
	if ticket == nil {
		return nil, httpx.NotFound("scale ticket not found")
	}
	if err := s.holdRecordBranch(ctx, "scale ticket", &ticket.BranchID, ticket.ID); err != nil {
		return nil, err
	}
	return ticket, nil
}

// VoidScaleTicket voids a ticket with its reason, keeping the row and its
// number: the sequence is never reused and a void is never a delete. A
// ticket on a sale line is the line's priced weight and cannot be voided;
// the line goes first.
func (s *Service) VoidScaleTicket(ctx context.Context, id uuid.UUID, reason, ifMatch string, bodyRevision *int64, actor string) (*ScaleTicket, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, invalid("reason", "is required: a void says why")
	}
	var out *ScaleTicket
	err := s.inTx(ctx, func(ctx context.Context) error {
		ticket, err := s.repo.LockScaleTicket(ctx, id)
		if err != nil {
			return err
		}
		if ticket == nil {
			return httpx.NotFound("scale ticket not found")
		}
		if err := s.holdRecordBranch(ctx, "scale ticket", &ticket.BranchID, ticket.ID); err != nil {
			return err
		}
		if err := httpx.CheckRevision(ticket.Revision, ifMatch, bodyRevision); err != nil {
			return err
		}
		if ticket.Status == TicketVoid {
			return httpx.InvalidStateTransition("the ticket is already void")
		}
		if ticket.SaleLineID != nil {
			return scaleConflict("ticket_on_line",
				"the ticket prices a sale line: remove the line before voiding its ticket")
		}
		if err := s.repo.VoidScaleTicket(ctx, ticket.ID, reason, ticket.Revision); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: auditTicketVoided, EntityType: "pos_scale_ticket", EntityID: ticket.ID, UserID: actor,
				Changes: map[string]any{"ticket_number": ticket.TicketNumber, "reason": reason}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		out, err = s.repo.GetScaleTicket(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
