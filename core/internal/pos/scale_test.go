// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The counter's scale link end to end (CP-19, ruling D8): the weigh in and
// weigh out flow against the simulated scale, the ticket with its
// consecutive number on the sale line, the manual weight entry with its
// reason and its Handbook 44 zero check, and the density conversion between
// the ton and the cubic yard.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/scale"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// withScale wires the fixture's counter to a simulated scale: the simulator
// on loopback TCP, the driver dialed to it, and the branch's device row the
// ticket names.
func withScale() func(*fixture) {
	return func(f *fixture) {
		sim := scale.NewSimulator()
		if _, err := sim.ListenTCP(); err != nil {
			f.t.Fatalf("simulator listen: %v", err)
		}
		drv, err := scale.DialTCP(context.Background(), sim.Addr())
		if err != nil {
			f.t.Fatalf("dial the simulator: %v", err)
		}
		f.scale = sim
		f.service.WithScale(drv)
		f.t.Cleanup(func() { drv.Close(); sim.Close() })
		mustExec(f.t, f.db, `INSERT INTO scale_devices (id, branch_id, name, protocol, endpoint, certificate_no, location_note)
			VALUES ($1, `+defaultBranch+`, 'Yard scale', 'SMA', 'sim://loopback', 'SIMULATED-COC', 'the bridge')`, f.scaleDeviceID)
	}
}

// bulkProduct inserts a product sold and stocked in the given unit at the
// given price per unit, with stock on hand, answering its id.
func (f *fixture) bulkProduct(t *testing.T, uom, price string, densityLbPerCY string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if densityLbPerCY == "" {
		mustExec(t, f.db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost)
			VALUES ($1, $2, 'crushed base rock', $3, $4::numeric, 1)`, id, "BLK-"+uuid.NewString()[:8], uom, price)
	} else {
		mustExec(t, f.db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost,
			density_lb_per_cy, density_volume_state)
			VALUES ($1, $2, 'crushed base rock', $3, $4::numeric, 1, $5::numeric, 'LOOSE')`, id, "BLK-"+uuid.NewString()[:8], uom, price, densityLbPerCY)
	}
	mustExec(t, f.db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 10000, 0)`, id, f.yardID)
	return id
}

func (f *fixture) weighIn(vehicle string) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/scale/weigh-in", map[string]any{"register_id": f.register, "vehicle_ref": vehicle},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

func firstLineOf(t *testing.T, sale map[string]any) map[string]any {
	t.Helper()
	lines, ok := sale["lines"].([]any)
	if !ok || len(lines) == 0 {
		t.Fatalf("no lines on the sale: %v", sale)
	}
	return lines[0].(map[string]any)
}

func TestScaleWeighInWeighOutPutsTheTicketOnTheLine(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	productID := f.bulkProduct(t, "TON", "30.00", "")

	// The empty truck weighs in.
	f.scale.SetWeightStableMilli(32000000, scale.UnitPound)
	in := f.weighIn("TRUCK-7")
	if in.status != http.StatusCreated {
		t.Fatalf("weigh in = %d: %s", in.status, in.raw)
	}
	inNumber := str(t, in.body, "ticket_number")
	if !strings.HasPrefix(inNumber, "WT-") {
		t.Errorf("ticket number = %q", inNumber)
	}
	if got := str(t, in.body, "gross_lb"); got != "32000" {
		t.Errorf("gross = %q, want the scale's 32000 lb", got)
	}
	if got := str(t, in.body, "source"); got != "scale" {
		t.Errorf("source = %q", got)
	}
	if got := str(t, in.body, "scale_device_id"); got != f.scaleDeviceID.String() {
		t.Errorf("the ticket does not name the device that produced it: %q", got)
	}

	// The truck loads and weighs out against a cart line.
	saleID := f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	if added.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", added.status, added.raw)
	}
	lineID := str(t, firstLineOf(t, added.body), "id")
	f.scale.SetWeightStableMilli(44400000, scale.UnitPound)
	out := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
		"sale_id": saleID, "line_id": lineID, "weigh_in_ticket_id": str(t, in.body, "id"),
		"revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if out.status != http.StatusOK {
		t.Fatalf("weigh out = %d: %s", out.status, out.raw)
	}
	ticket := out.body["ticket"].(map[string]any)
	if got := str(t, ticket, "net_basis"); got != "double_draft" {
		t.Errorf("net basis = %q: the net is Gable's computation of two gross readings", got)
	}
	if got := str(t, ticket, "gross_lb"); got != "44400" {
		t.Errorf("gross out = %q", got)
	}
	if got := str(t, ticket, "tare_lb"); got != "32000" {
		t.Errorf("tare = %q, want the weigh in reading", got)
	}
	if got := str(t, ticket, "net_lb"); got != "12400" {
		t.Errorf("net = %q, want 44400 - 32000", got)
	}
	outNumber := str(t, ticket, "ticket_number")
	if outNumber == inNumber {
		t.Error("the weigh out reused the weigh in's ticket number")
	}

	// The line carries the net, the ticket number and the priced quantity:
	// 12400 lb at 2000 lb to the ton is 6.2 tons at thirty dollars.
	line := firstLineOf(t, out.body["sale"].(map[string]any))
	if got := str(t, line, "weight_net_lb"); got != "12400" {
		t.Errorf("line net = %q", got)
	}
	if got := str(t, line, "quantity"); got != "6.2" {
		t.Errorf("line quantity = %q, want 6.2 tons", got)
	}
	if got := str(t, line, "scale_ticket_number"); got != outNumber {
		t.Errorf("line ticket = %q, want %q", got, outNumber)
	}
	if got := str(t, line, "weight_source"); got != "scale" {
		t.Errorf("line source = %q", got)
	}
	if got := num(t, line, "line_total_cents"); got != 18600 {
		t.Errorf("line total = %d cents, want 18600 (6.2 tons at $30)", got)
	}

	// The weigh in ticket is completed; the sale completes for the money.
	cur := f.getSale(t, saleID)
	if r := f.completeSale(saleID, tender("cash", num(t, cur, "total_cents"))); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	inAfter := f.do("GET", "/api/v1/pos/scale/tickets/"+str(t, in.body, "id"), nil, "X-Test-Role", "cashier")
	if got := str(t, inAfter.body, "status"); got != "completed" {
		t.Errorf("weigh in status = %q, want completed", got)
	}
}

func TestScaleWeighOutByVehicleFindsTheOpenWeighIn(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	productID := f.bulkProduct(t, "TON", "30.00", "")
	f.scale.SetWeightStableMilli(30000000, scale.UnitPound)
	if r := f.weighIn("PLATE-99"); r.status != http.StatusCreated {
		t.Fatalf("weigh in = %d: %s", r.status, r.raw)
	}
	saleID := f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	lineID := str(t, firstLineOf(t, added.body), "id")
	f.scale.SetWeightStableMilli(42000000, scale.UnitPound)
	out := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
		"sale_id": saleID, "line_id": lineID, "vehicle_ref": "PLATE-99",
		"revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if out.status != http.StatusOK {
		t.Fatalf("weigh out by vehicle = %d: %s", out.status, out.raw)
	}
	if got := str(t, out.body["ticket"].(map[string]any), "net_lb"); got != "12000" {
		t.Errorf("net = %q, want 12000", got)
	}
}

func TestScaleReadingRefusals(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())

	// A load in motion is refused: the driver never hands the number over.
	// The stable command's wait is pinned short so the pinned motion answers
	// at once.
	f.scale.SetStableWait(150 * time.Millisecond)
	f.scale.SetMotion(true)
	if r := f.weighIn("TRUCK-M"); r.status != http.StatusConflict {
		t.Errorf("weigh in on a moving load = %d, want 409: %s", r.status, r.raw)
	} else if code, _, _ := errorOf(t, r); code != "scale_reading_refused" {
		t.Errorf("code = %q", code)
	}
	f.scale.SetMotion(false)

	// An overloaded scale is refused the same way.
	f.scale.SetStatus(scale.StatusOverCapacity)
	if r := f.weighIn("TRUCK-O"); r.status != http.StatusConflict {
		t.Errorf("weigh in over capacity = %d, want 409: %s", r.status, r.raw)
	}
	f.scale.SetStatus(scale.StatusNone)

	// A weigh out with no open weigh in says so plainly.
	f.scale.SetWeightStableMilli(40000000, scale.UnitPound)
	saleID := f.startSale(nil)
	productID := f.bulkProduct(t, "TON", "30.00", "")
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	r := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
		"sale_id": saleID, "line_id": str(t, firstLineOf(t, added.body), "id"), "vehicle_ref": "GHOST",
		"revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("weigh out with no weigh in = %d: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "no_open_weigh_in" {
		t.Errorf("code = %q", code)
	}
}

func TestScaleStatusRouteReadsTheScale(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	f.scale.SetWeightStableMilli(12400500, scale.UnitPound)
	r := f.do("GET", "/api/v1/pos/scale?register_id="+f.register, nil, "X-Test-Role", "cashier")
	if r.status != http.StatusOK {
		t.Fatalf("scale status = %d: %s", r.status, r.raw)
	}
	if configured, _ := r.body["configured"].(bool); !configured {
		t.Fatalf("not configured: %s", r.raw)
	}
	reading := r.body["reading"].(map[string]any)
	if got := str(t, reading, "weight_lb"); got != "12400.5" {
		t.Errorf("reading = %q", got)
	}
	if atZero, _ := reading["at_zero"].(bool); atZero {
		t.Error("a loaded scale reads at zero")
	}
}

func TestScaleNotConfiguredAnswersPlainly(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	r := f.weighIn("TRUCK-X")
	if r.status != http.StatusServiceUnavailable {
		t.Fatalf("weigh in with no scale = %d: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "scale_not_configured" {
		t.Errorf("code = %q", code)
	}
}

func TestManualWeightEntryWithAReason(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale()) // the scale reads zero: the deck is empty
	productID := f.bulkProduct(t, "TON", "30.00", "")
	saleID := f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	lineID := str(t, firstLineOf(t, added.body), "id")

	// No reason: refused. An invented reason: refused. The closed list only.
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/items/"+lineID+"/weight",
		map[string]any{"weight_lb": "12400", "revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest {
		t.Errorf("no reason = %d: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/pos/transactions/"+saleID+"/items/"+lineID+"/weight",
		map[string]any{"weight_lb": "12400", "reason": "felt_like_it", "revision": rev(t, added)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest {
		t.Errorf("invented reason = %d: %s", r.status, r.raw)
	}

	r = f.do("POST", "/api/v1/pos/transactions/"+saleID+"/items/"+lineID+"/weight",
		map[string]any{"weight_lb": "12400", "reason": "scale_offline", "revision": rev(t, added)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("manual weight = %d: %s", r.status, r.raw)
	}
	line := firstLineOf(t, r.body)
	if got := str(t, line, "weight_source"); got != "manual" {
		t.Errorf("source = %q", got)
	}
	if got := str(t, line, "manual_weight_reason"); got != "scale_offline" {
		t.Errorf("reason = %q", got)
	}
	if got := str(t, line, "quantity"); got != "6.2" {
		t.Errorf("quantity = %q", got)
	}
	if got := str(t, line, "scale_ticket_number"); !strings.HasPrefix(got, "WT-") {
		t.Errorf("a manual weight still takes a ticket number: %q", got)
	}

	// The ticket says manual, in the words the audit reads back.
	ticket := f.do("GET", "/api/v1/pos/scale/tickets/"+str(t, line, "scale_ticket_id"), nil, "X-Test-Role", "cashier")
	if got := str(t, ticket.body, "source"); got != "manual" {
		t.Errorf("ticket source = %q", got)
	}
	if got := str(t, ticket.body, "net_basis"); got != "single_draft" {
		t.Errorf("ticket basis = %q", got)
	}

	// The reason is what makes the entry auditable: the act's audit row
	// carries the closed list code and the ticket number, in the act's
	// transaction.
	var audits int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action = 'pos_transaction.line_weight_manual'
			AND entity_type = 'pos_transaction' AND entity_id = $1
			AND changes->>'reason' = 'scale_offline' AND changes->>'ticket_number' IS NOT NULL`, saleID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Errorf("%d manual weight audit rows, want 1 carrying the reason", audits)
	}
}

func TestManualWeightRefusedWhileTheScaleIsNotAtZero(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	productID := f.bulkProduct(t, "TON", "30.00", "")
	saleID := f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	lineID := str(t, firstLineOf(t, added.body), "id")

	// Handbook 44: a manual gross or net enters only with the indication at
	// zero, so a truck on the deck refuses the typed weight.
	f.scale.SetWeightStableMilli(9000000, scale.UnitPound)
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/items/"+lineID+"/weight",
		map[string]any{"weight_lb": "12400", "reason": "preweighed", "revision": rev(t, added)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("manual weight with a loaded scale = %d: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "scale_not_zero" {
		t.Errorf("code = %q", code)
	}
}

func TestManualReasonsAreAClosedList(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	r := f.do("GET", "/api/v1/pos/scale/manual-reasons", nil, "X-Test-Role", "cashier")
	if r.status != http.StatusOK {
		t.Fatalf("reasons = %d: %s", r.status, r.raw)
	}
	items, ok := r.body["items"].([]any)
	if !ok || len(items) < 3 {
		t.Fatalf("items: %s", r.raw)
	}
	for _, it := range items {
		code := it.(map[string]any)["code"].(string)
		switch code {
		case "scale_offline", "scale_error", "preweighed", "other":
		default:
			t.Errorf("invented reason %q on the wire", code)
		}
	}
}

func TestDensityConvertsTheNetToYards(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())

	// The priced quantity rounds once, half away from zero, at the wire's
	// scale 4: an exact quotient stays exact, an inexact one rounds (1 lb at
	// 2700 lb per yard is 0.0003703 yards, 0.0004, never a truncated
	// 0.0003), and a quotient landing exactly on a half at the fifth decimal
	// (1 lb at 4000 lb per yard is 0.00025) lands on the farther side,
	// 0.0003, not 0.0002.
	for _, tc := range []struct {
		name           string
		densityLbPerCY string
		grossInMilli   int64
		grossOutMilli  int64
		wantQuantity   string
		wantTotalCents int64
	}{
		{"an exact quotient", "2700", 30000000, 43500000, "5", 21000},            // 13500 lb net at 2700
		{"an inexact quotient", "2700", 30000000, 30001000, "0.0004", 0},         // 1 lb net
		{"a half at the fifth decimal", "4000", 30000000, 30001000, "0.0003", 0}, // 0.00025 yards
	} {
		t.Run(tc.name, func(t *testing.T) {
			productID := f.bulkProduct(t, "CY", "42.00", tc.densityLbPerCY)
			f.scale.SetWeightStableMilli(tc.grossInMilli, scale.UnitPound)
			in := f.weighIn("TRUCK-D-" + tc.name)
			saleID := f.startSale(nil)
			added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
			lineID := str(t, firstLineOf(t, added.body), "id")
			f.scale.SetWeightStableMilli(tc.grossOutMilli, scale.UnitPound)
			out := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
				"sale_id": saleID, "line_id": lineID, "weigh_in_ticket_id": str(t, in.body, "id"),
				"revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
			if out.status != http.StatusOK {
				t.Fatalf("weigh out = %d: %s", out.status, out.raw)
			}
			line := firstLineOf(t, out.body["sale"].(map[string]any))
			if got := str(t, line, "quantity"); got != tc.wantQuantity {
				t.Errorf("quantity = %q, want %s", got, tc.wantQuantity)
			}
			if tc.wantTotalCents != 0 {
				if got := num(t, line, "line_total_cents"); got != tc.wantTotalCents {
					t.Errorf("line total = %d, want %d", got, tc.wantTotalCents)
				}
			}
		})
	}
}

func TestVolumeLineWithoutDensityIsRefused(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	productID := f.bulkProduct(t, "CY", "42.00", "")
	f.scale.SetWeightStableMilli(30000000, scale.UnitPound)
	in := f.weighIn("TRUCK-N")
	saleID := f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	f.scale.SetWeightStableMilli(43500000, scale.UnitPound)
	r := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
		"sale_id": saleID, "line_id": str(t, firstLineOf(t, added.body), "id"), "weigh_in_ticket_id": str(t, in.body, "id"),
		"revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("weigh out onto a yard line with no density = %d: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "density_required" {
		t.Errorf("code = %q", code)
	}
}

func TestCountedLineRefusesAWeight(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	saleID := f.startSale(nil)
	added := f.addLine(saleID, f.productLine("2")) // the fixture's 2x4, counted in PCS
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/items/"+str(t, firstLineOf(t, added.body), "id")+"/weight",
		map[string]any{"weight_lb": "100", "reason": "preweighed", "revision": rev(t, added)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("weight on a counted line = %d: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "weight_unit_mismatch" {
		t.Errorf("code = %q", code)
	}
}

func TestScaleTicketVoidKeepsTheNumber(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	f.scale.SetWeightStableMilli(31000000, scale.UnitPound)
	in := f.weighIn("TRUCK-V")
	id := str(t, in.body, "id")
	number := str(t, in.body, "ticket_number")

	// A stale revision is refused; the real one voids and keeps the row.
	r := f.do("POST", "/api/v1/pos/scale/tickets/"+id+"/void",
		map[string]any{"reason": "the truck left without loading", "revision": 99}, "X-Test-Role", "cashier")
	if r.status != http.StatusConflict {
		t.Fatalf("void at a stale revision = %d: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/pos/scale/tickets/"+id+"/void",
		map[string]any{"reason": "the truck left without loading", "revision": rev(t, in)}, "X-Test-Role", "cashier")
	if r.status != http.StatusOK {
		t.Fatalf("void = %d: %s", r.status, r.raw)
	}
	if got := str(t, r.body, "status"); got != "void" {
		t.Errorf("status = %q", got)
	}
	if got := str(t, r.body, "ticket_number"); got != number {
		t.Errorf("a void keeps its number: %q", got)
	}
	// A voided ticket cannot weigh out.
	productID := f.bulkProduct(t, "TON", "30.00", "")
	saleID := f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	out := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
		"sale_id": saleID, "line_id": str(t, firstLineOf(t, added.body), "id"), "weigh_in_ticket_id": id,
		"revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if out.status != http.StatusConflict {
		t.Errorf("weigh out against a void ticket = %d: %s", out.status, out.raw)
	}
}

func TestScaleTicketListFiltersOpen(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	f.scale.SetWeightStableMilli(31000000, scale.UnitPound)
	f.weighIn("TRUCK-L1")
	f.weighIn("TRUCK-L2")
	r := f.do("GET", "/api/v1/pos/scale/tickets?register_id="+f.register+"&status=open", nil, "X-Test-Role", "cashier")
	if r.status != http.StatusOK {
		t.Fatalf("list = %d: %s", r.status, r.raw)
	}
	items, ok := r.body["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items: %s", r.raw)
	}
	first := str(t, items[0].(map[string]any), "ticket_number")
	second := str(t, items[1].(map[string]any), "ticket_number")
	if first >= second {
		t.Errorf("numbers out of order: %q then %q", first, second)
	}
	r = f.do("GET", "/api/v1/pos/scale/tickets?register_id="+f.register+"&status=open&vehicle_ref=TRUCK-L2", nil, "X-Test-Role", "cashier")
	if got := len(r.body["items"].([]any)); got != 1 {
		t.Errorf("vehicle filter: %d tickets", got)
	}
}

// The line amount is the priced quantity through the one extension rule
// (ADR 0005 section 2.4): the weight's pounds become the quantity at scale
// 4, and the cents are the exact product rounded once, half away from zero.
// A one pound net at fifty dollars the ton is two and a half cents, and the
// half cent lands on the farther side of zero: 3, never a truncated 2.
func TestScaleWeighedLineAmountRoundsHalfAwayToTheCent(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	productID := f.bulkProduct(t, "TON", "50.00", "")
	f.scale.SetWeightStableMilli(32000000, scale.UnitPound)
	in := f.weighIn("TRUCK-R")
	saleID := f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	lineID := str(t, firstLineOf(t, added.body), "id")
	f.scale.SetWeightStableMilli(32001000, scale.UnitPound) // one pound of rock
	out := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
		"sale_id": saleID, "line_id": lineID, "weigh_in_ticket_id": str(t, in.body, "id"),
		"revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if out.status != http.StatusOK {
		t.Fatalf("weigh out = %d: %s", out.status, out.raw)
	}
	line := firstLineOf(t, out.body["sale"].(map[string]any))
	if got := str(t, line, "quantity"); got != "0.0005" {
		t.Fatalf("quantity = %q, want 0.0005 tons (one pound at 2000 to the ton)", got)
	}
	if got := num(t, line, "line_total_cents"); got != 3 {
		t.Errorf("line total = %d cents, want 3 (0.0005 tons at $50 is 2.5 cents, half away from zero)", got)
	}
}

// A weigh in ticket weighs out once: a second weigh out against the same
// ticket is refused and the second line keeps no weight, even when the two
// sales are weighed one after the other rather than raced.
func TestScaleWeighInTicketRefusesASecondWeighOut(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	productID := f.bulkProduct(t, "TON", "30.00", "")
	f.scale.SetWeightStableMilli(30000000, scale.UnitPound)
	in := f.weighIn("TRUCK-2X")

	saleOne := f.startSale(nil)
	addedOne := f.addLine(saleOne, map[string]any{"product_id": productID.String(), "quantity": "1"})
	f.scale.SetWeightStableMilli(42000000, scale.UnitPound)
	out := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
		"sale_id": saleOne, "line_id": str(t, firstLineOf(t, addedOne.body), "id"),
		"weigh_in_ticket_id": str(t, in.body, "id"), "revision": rev(t, addedOne)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if out.status != http.StatusOK {
		t.Fatalf("the first weigh out = %d: %s", out.status, out.raw)
	}

	// A second sale, a second line, the same weigh in ticket: refused.
	saleTwo := f.startSale(nil)
	addedTwo := f.addLine(saleTwo, map[string]any{"product_id": productID.String(), "quantity": "1"})
	second := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
		"sale_id": saleTwo, "line_id": str(t, firstLineOf(t, addedTwo.body), "id"),
		"weigh_in_ticket_id": str(t, in.body, "id"), "revision": rev(t, addedTwo)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if second.status != http.StatusConflict {
		t.Fatalf("the second weigh out on one ticket = %d: %s", second.status, second.raw)
	}
	if code, _, _ := errorOf(t, second); code != "invalid_state_transition" {
		t.Errorf("code = %q, want invalid_state_transition", code)
	}
	two := f.getSale(t, saleTwo)
	if firstLineOf(t, two)["weight_source"] != nil {
		t.Errorf("the refused weigh out left a weight on the second line: %v", two["lines"])
	}
	var tickets int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM scale_tickets WHERE weigh_in_ticket_id = $1`, str(t, in.body, "id")).Scan(&tickets); err != nil {
		t.Fatal(err)
	}
	if tickets != 1 {
		t.Errorf("%d weigh out tickets name the weigh in, want 1", tickets)
	}
}

// Three contenders at pool size 4: the weigh out onto one sale completes
// once, and every weigh in number is taken once.
func TestScaleWeighOutConcurrency(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db, withScale())
	productID := f.bulkProduct(t, "TON", "30.00", "")
	f.scale.SetWeightStableMilli(30000000, scale.UnitPound)
	in := f.weighIn("TRUCK-C")
	saleID := f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	lineID := str(t, firstLineOf(t, added.body), "id")
	revision := rev(t, added)
	f.scale.SetWeightStableMilli(41000000, scale.UnitPound)

	var wg sync.WaitGroup
	statuses := make(chan int, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
				"sale_id": saleID, "line_id": lineID, "weigh_in_ticket_id": str(t, in.body, "id"),
				"revision": revision}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
			statuses <- r.status
		}()
	}
	wg.Wait()
	close(statuses)
	ok, conflict := 0, 0
	for s := range statuses {
		switch s {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Errorf("contender answered %d", s)
		}
	}
	if ok != 1 || conflict != 2 {
		t.Errorf("one weigh out wins: got %d ok and %d conflicts", ok, conflict)
	}
	var lines, tickets int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM pos_line_items WHERE transaction_id = $1::uuid),
			(SELECT count(*) FROM scale_tickets WHERE weigh_in_ticket_id = $2::uuid)`,
		saleID, str(t, in.body, "id")).Scan(&lines, &tickets); err != nil {
		t.Fatal(err)
	}
	if lines != 1 || tickets != 1 {
		t.Errorf("one line, one weigh out ticket: got %d lines and %d tickets", lines, tickets)
	}
}

// The quantity of a weighed line is the scale's, so the plus button refuses
// it: re-weigh instead of editing the number the ticket proves.
func TestWeighedLineRefusesAQuantityEdit(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	productID := f.bulkProduct(t, "TON", "30.00", "")
	f.scale.SetWeightStableMilli(30000000, scale.UnitPound)
	in := f.weighIn("TRUCK-Q")
	saleID := f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	lineID := str(t, firstLineOf(t, added.body), "id")
	f.scale.SetWeightStableMilli(41000000, scale.UnitPound)
	out := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
		"sale_id": saleID, "line_id": lineID, "weigh_in_ticket_id": str(t, in.body, "id"),
		"revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if out.status != http.StatusOK {
		t.Fatalf("weigh out = %d: %s", out.status, out.raw)
	}
	cur := f.getSale(t, saleID)
	r := f.do("PATCH", "/api/v1/pos/transactions/"+saleID+"/items/"+lineID,
		map[string]any{"quantity": "9", "revision": num(t, cur, "revision")}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest && r.status != http.StatusConflict {
		t.Errorf("quantity edit of a weighed line = %d, want a refusal: %s", r.status, r.raw)
	}
}

// The printed receipt carries the ticket: the consecutive number, the gross,
// tare and net, and the Handbook 44 marking when the weight was typed.
func TestWeighedSaleReceiptPrintsTheTicket(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale(), withRealThermalReceipts())
	productID := f.bulkProduct(t, "TON", "30.00", "")
	f.scale.SetWeightStableMilli(32000000, scale.UnitPound)
	in := f.weighIn("TRUCK-R")
	saleID := f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	f.scale.SetWeightStableMilli(44400000, scale.UnitPound)
	out := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
		"sale_id": saleID, "line_id": str(t, firstLineOf(t, added.body), "id"), "weigh_in_ticket_id": str(t, in.body, "id"),
		"revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if out.status != http.StatusOK {
		t.Fatalf("weigh out = %d: %s", out.status, out.raw)
	}
	ticketNumber := str(t, out.body["ticket"].(map[string]any), "ticket_number")
	cur := f.getSale(t, saleID)
	if r := f.completeSale(saleID, tender("cash", num(t, cur, "total_cents"))); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	printed := f.printReceipt(saleID, "layout=thermal80")
	if printed.status != http.StatusOK {
		t.Fatalf("print = %d: %s", printed.status, printed.raw)
	}
	for _, want := range []string{ticketNumber, "G 44400", "T 32000", "N 12400"} {
		if !strings.Contains(string(printed.raw), want) {
			t.Errorf("the receipt misses %q", want)
		}
	}
	if strings.Contains(string(printed.raw), "MAN WT") {
		t.Error("a scale reading prints no manual marking")
	}
}

func TestManualWeightReceiptPrintsTheMarking(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withRealThermalReceipts())
	productID := f.bulkProduct(t, "TON", "30.00", "")
	saleID := f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/items/"+str(t, firstLineOf(t, added.body), "id")+"/weight",
		map[string]any{"weight_lb": "12400", "reason": "scale_offline", "revision": rev(t, added)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("manual weight = %d: %s", r.status, r.raw)
	}
	ticketNumber := str(t, firstLineOf(t, r.body), "scale_ticket_number")
	cur := f.getSale(t, saleID)
	if c := f.completeSale(saleID, tender("cash", num(t, cur, "total_cents"))); c.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", c.status, c.raw)
	}
	printed := f.printReceipt(saleID, "layout=thermal80")
	if printed.status != http.StatusOK {
		t.Fatalf("print = %d: %s", printed.status, printed.raw)
	}
	for _, want := range []string{"MAN WT", ticketNumber, "N 12400"} {
		if !strings.Contains(string(printed.raw), want) {
			t.Errorf("the receipt misses %q", want)
		}
	}
}
