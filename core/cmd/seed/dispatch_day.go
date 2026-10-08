// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

// Dispatch-day fixture for the AI_LM (`gable-ai-lm`) demo.
//
// The rest of this seed produces a believable ERP snapshot — months of orders,
// invoices, POs, routes — but nothing AI_LM can plan a day from. AI_LM pulls a
// day of work with GET /api/integration/orders?date=YYYY-MM-DD&status=CONFIRMED
// and, in analyzeOrder, marks an order Routable only when it has BOTH a
// latitude and a longitude. Its packing solver, in resolveGeometry, needs the
// product's L/W/H to place anything at all. Before this file the demo database
// had zero orders with a scheduled_delivery_date, zero deliveries with
// coordinates and zero products with geometry, so a live demo produced an empty
// plan and no error — the worst possible outcome in front of an audience.
//
// This file closes exactly those three gaps, and nothing else:
//
//   1. applyProductGeometry  — L/W/H + stackable on the SKUs that have a real
//      unit shape, left NULL on the ones that genuinely do not.
//   2. seedDispatchDay       — one cluster of CONFIRMED orders all carrying
//      scheduled_delivery_date, one per customer, spread around the Okanagan.
//   3. …with a `deliveries` row per order carrying the stop's coordinates.
//
// Everything here runs inside the DEMO_SEED gate in main.go and is re-runnable:
// orders/order_lines/deliveries are truncated by resetTransactionalData at the
// start of every run, and the geometry pass is a plain UPDATE keyed by SKU.

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// dispatchDateEnv pins the fixture to a fixed day. Unset, the fixture lands on
// TODAY so the demo works whenever it is opened; set it (YYYY-MM-DD) when a
// demo needs a stable, pre-rehearsed date — e.g. DEMO_DISPATCH_DATE=2026-09-14.
const dispatchDateEnv = "DEMO_DISPATCH_DATE"

// dispatchDate resolves the day the fixture schedules its deliveries for.
// An unparseable override is a loud fallback to today rather than a hard fail:
// the seed's job is to leave a demoable database behind, and a typo in an env
// var should not leave one empty.
func dispatchDate() time.Time {
	if raw := strings.TrimSpace(os.Getenv(dispatchDateEnv)); raw != "" {
		if d, err := time.Parse("2006-01-02", raw); err == nil {
			return d
		}
		log.Printf("Seed: %s=%q is not YYYY-MM-DD — falling back to today", dispatchDateEnv, raw)
	}
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// geometry is one SKU's canonical unit shape, in inches, as the PIM would hold
// it. HasDims=false means "the PIM has no geometry for this SKU" — the row is
// written as SQL NULL, which is materially different from a 0.0 dimension:
// AI_LM's resolveGeometry reports FALLBACK for NULL and skips the article in
// the packer, whereas a zero would have it pack a phantom zero-size box.
type geometry struct {
	SKU     string
	HasDims bool
	L, W, H float64
	// Stackable answers "may other cargo be laid on top of this article?".
	// AI_LM's tier packer enforces it in both directions: a non-stackable
	// article is never banded more than one unit high, and no later tier is
	// built over it. Honest values matter — claiming a crated door stacks
	// produces a load plan the yard cannot physically build.
	Stackable bool
}

func dims(sku string, l, w, h float64, stackable bool) geometry {
	return geometry{SKU: sku, HasDims: true, L: l, W: w, H: h, Stackable: stackable}
}

// noGeometry marks a SKU whose *selling unit* is not a discrete rigid article
// you set down on a truck bed: loose connectors and flashings that travel in a
// bucket, tube goods, and random-length stock priced by the linear foot (a
// "unit" of LUMB24RLN3 is one foot of an arbitrary board — it has no shape).
// These are the rows that exercise AI_LM's FALLBACK path in the demo, and they
// are left NULL because that is the truth, not to save typing.
func noGeometry(sku string) geometry { return geometry{SKU: sku} }

// productGeometry is the PIM digital twin for the demo catalog. Dimensional
// lumber uses DRESSED (S4S) sizes — a 2x4 is 1.5″ × 3.5″, not 2″ × 4″ — because
// the packer places real boards, not nominal ones.
var productGeometry = []geometry{
	// --- Dimensional lumber (length × actual width × actual thickness) ---
	dims("LUM-248-PREM", 96, 3.5, 1.5, true),
	dims("LUM-2410-PREM", 120, 3.5, 1.5, true),
	dims("LUM-2412-PREM", 144, 3.5, 1.5, true),
	dims("LUM-2492-STUD", 92.625, 3.5, 1.5, true), // precut stud: 92-5/8″
	dims("LUM-2610-PREM", 120, 5.5, 1.5, true),
	dims("LUM-2612-PREM", 144, 5.5, 1.5, true),
	dims("LUM-2616-PREM", 192, 5.5, 1.5, true),
	dims("LUM-2810-NO2", 120, 7.25, 1.5, true),
	dims("LUM-2816-NO2", 192, 7.25, 1.5, true),
	dims("LUM-21012-NO2", 144, 9.25, 1.5, true),
	dims("LUM-21216-NO2", 192, 11.25, 1.5, true),
	dims("LUM-448-PT", 96, 3.5, 3.5, true),
	dims("LUM-4410-PT", 120, 3.5, 3.5, true),
	// 6x6 PT timbers ride as a single banded course; a yard does not tier
	// other material over them (AI_LM's own packer docs name this article).
	dims("LUM-6612-PT", 144, 5.5, 5.5, false),
	dims("LUM-448-WRC", 96, 3.5, 3.5, true),
	dims("LUM-166-WRC", 72, 5.5, 0.625, true),

	// --- Sheet goods: 4×8 sheets, thickness is the real one ---
	dims("PLY-34-CDX", 96, 48, 0.75, true),
	dims("PLY-12-CDX", 96, 48, 0.5, true),
	dims("OSB-34-TG", 96, 48, 0.75, true),
	dims("OSB-12", 96, 48, 0.4375, true), // 7/16″ OSB
	dims("DW-12-REG", 96, 48, 0.5, true),
	dims("DW-58-FC", 96, 48, 0.625, true),

	// --- Roofing ---
	dims("RF-ARCH-30", 40, 13.25, 5, true),
	dims("RF-SH-BLK", 40, 13.25, 5, true),
	dims("RF-SH-WW", 40, 13.25, 5, true),
	dims("RF-FELT-15", 36, 12, 12, true), // rolls: bounding box, not the cylinder
	dims("RF-ICE-65", 36, 10, 10, true),
	dims("RF-ICE-WTR", 36, 10, 10, true),
	dims("RF-DRIP-10", 120, 4, 2, true),
	dims("RF-EDGE-WHT", 120, 4, 2, true),
	dims("RF-RIDGE-4", 48, 12, 2, true),
	dims("RF-START", 40, 7.5, 3, true),
	dims("RF-NAIL-125", 12, 8, 6, true),
	noGeometry("RF-FLASH-STEP"),
	noGeometry("RF-FLASH-PIPE"),

	// --- Fastener cartons (the carton is the handled unit) ---
	dims("NAIL-16D-50", 16, 12, 10, true),
	dims("NAIL-10D-50", 16, 12, 10, true),
	dims("SCR-DECK-3-5", 9, 6, 5, true),

	// --- Loose connectors: no unit geometry recorded ---
	noGeometry("HANGER-26"),
	noGeometry("HANGER-28"),
	noGeometry("HANGER-210"),
	noGeometry("TIE-H1"),
	noGeometry("SIMP-LUS28"),

	// --- Insulation: compressed bags ---
	dims("INS-R13-15", 48, 16, 20, true),
	dims("INS-R19-15", 48, 16, 24, true),
	dims("INS-R30-24", 48, 20, 26, true),

	// --- Millwork ---
	dims("DR-INT-3080-6P", 80, 30, 1.375, true),
	dims("DR-INT-3280-6P", 80, 32, 1.375, true),
	dims("DR-INT-3680-6P", 80, 36, 1.375, true),
	// Crated pre-hung steel exterior door: it travels upright in its crate and
	// nothing goes on top of it.
	dims("DR-EXT-3680-STL", 82, 38, 6, false),
	dims("MLD-BASE-MDF", 192, 3.25, 0.625, true),
	dims("MLD-CASE-MDF", 168, 2.25, 0.5, true),

	// --- Cornice / exterior trim ---
	dims("CORN2006", 120, 6, 6, true),
	dims("CORN2009", 120, 0.75, 3, true),
	noGeometry("CORNCLEAR"), // caulk tube
	dims("CORNCTRM412+", 144, 4, 0.4375, true),
	dims("CORNCTRMG12+", 144, 6, 0.4375, true),
	dims("CORNHMOLD14", 144, 1, 0.25, true),
	dims("CORNPOLY18", 36, 12, 12, true),
	dims("CORNSFT1212", 144, 12, 0.25, true),
	dims("CORNSFT1612", 144, 16, 0.25, true),
	dims("CORNSFT1612V", 144, 16, 0.25, true),
	dims("CORNSFT2408V", 96, 24, 0.25, true),
	dims("CORNSHTGR49", 108, 48, 0.125, true),
	dims("CORNTAPE", 6, 5, 5, true),

	// --- Random-length stock, priced by the linear foot: a unit has no shape ---
	noGeometry("LUMB14RLN3+"),
	noGeometry("LUMB24RLN3"),
	noGeometry("LUMBUT24RL"),
}

// applyProductGeometry writes the PIM digital twin onto the catalog. It is a
// separate pass from the product upsert in main.go on purpose: that upsert's
// ON CONFLICT clause does not touch the geometry columns, so this stays
// idempotent across re-runs and never fights it.
//
// A SKU with HasDims=false is UPDATEd to NULL rather than skipped, so a re-run
// after editing this table converges instead of leaving stale dimensions on a
// SKU that has since been reclassified as dimensionless.
func applyProductGeometry(db *sql.DB) (withDims, withoutDims int) {
	for _, g := range productGeometry {
		var (
			l, w, h   any
			stackable any
			source    any
		)
		if g.HasDims {
			l, w, h = g.L, g.W, g.H
			stackable = g.Stackable
			// 'parametric' is the provenance AI_LM's integration layer already
			// infers for a dimensioned row; recording it explicitly keeps the
			// wire value stable if that inference ever changes.
			source = "parametric"
		}
		res, err := db.Exec(`UPDATE products
			SET length_in=$2, width_in=$3, height_in=$4, stackable=$5, geometry_source=$6, updated_at=NOW()
			WHERE sku=$1`, g.SKU, l, w, h, stackable, source)
		if err != nil {
			log.Printf("applyProductGeometry %s: %v", g.SKU, err)
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			log.Printf("applyProductGeometry: no product with sku %q — geometry table has drifted from the catalog", g.SKU)
			continue
		}
		if g.HasDims {
			withDims++
		} else {
			withoutDims++
		}
	}
	return withDims, withoutDims
}

// dispatchLine is one order line in the fixture.
type dispatchLine struct {
	SKU string
	Qty int
}

// dispatchStop is one CONFIRMED order scheduled for the dispatch day, together
// with the coordinates its delivery stop carries.
//
// On addresses: the street names throughout this seed are INVENTED (see the
// address rules on the customers block in main.go) and no postal codes are
// stored, because the originals were real Okanagan streets paired with real
// postal codes. That rule is unchanged here — a stop is identified by the
// customer's existing invented address plus a coordinate. The coordinates are
// real Okanagan points (municipal/neighbourhood centres), which is fine: a
// lat/lng identifies a location, not a person, and routing that runs on
// fictional coordinates proves nothing.
type dispatchStop struct {
	Customer     string
	Lat, Lng     float64
	Instructions string
	Lines        []dispatchLine
}

// dispatchStops is the day's work: thirteen stops, one per customer, fanned
// around the depot at Kelowna Main (49.8863, -119.4666).
//
// The quantities are chosen, not random, and they are chosen against AI_LM's
// MEASURED behaviour — every bound below was read off a real ingest→assign→
// pack→review→push cycle against this fixture, not assumed. Five of AI_LM's
// rules shape them:
//
//  1. STOP COUNT DRIVES THE FLEET. sweepAssign (gable-ai-lm's
//     internal/workflow/assign.go) fills trucks largest-capacity-first and caps
//     each at maxStopsPerTruck=3. Thirteen stops therefore need five trucks,
//     which is what puts four different ratings on the board — 30 000 / 24 000 /
//     24 000 lb flatbeds, the 18 000 lb boom truck and the 16 000 lb box truck.
//     Stop count, not weight, is the robust lever here; see (3) for why.
//
//  2. ONE TRUCK STILL FILLS EARLY, ON WEIGHT. Okanagan Homes' drywall drop
//     (8 090 lb) does not fit on the boom truck behind Lake Country and
//     Predator Ridge (80% of 18 000 = 14 400 lb), so it rolls to the box truck
//     and takes Knox Mountain with it. That is a real capacity decision in the
//     assignment step rather than a stop-count formality.
//
//  3. THE PER-AXLE MODEL CAPS A LOAD FAR BELOW ITS PAYLOAD RATING. AI_LM's
//     solver puts the steer axle at position 0 — the same coordinate the bed
//     starts at — so cargo near the front of the deck loads the steer axle
//     almost fully (load.distributeToAxles interpolates linearly to the drive
//     axle at 240 in). Measured across the five trucks in this fixture, 48-56%
//     of cargo weight lands on the steer axle. With a flatbed's 14 000 lb tare
//     split by axle rating (5 091 lb of it on the steer), a 24 000 lb-rated
//     flatbed FAILS its 12 000 lb steer axle at roughly 12 500 lb of cargo —
//     barely half its payload — and Push refuses on any axle FAIL. Each flatbed
//     here therefore carries 7 900-10 200 lb and every axle reads PASS. That
//     ceiling is AI_LM's to reconsider (the solver marks per-axle output
//     Advisory yet the push gate treats it as a hard refusal); the fixture stays
//     under it so the demo can complete the cycle.
//
//     The same ceiling is why weight is NOT used to force the fifth truck onto
//     the board: sweepAssign only calls a flatbed full at 19 200 lb, which no
//     flatbed can carry without failing its steer axle first. Stop count (1) and
//     the boom truck's smaller cap (2) are the only levers that survive both.
//
//  4. TOTAL CUBE, NOT PAYLOAD, IS WHAT ACTUALLY FILLS A DECK. The assignment
//     step budgets a truck at 65% of its bed envelope (load.UsableBedVolumeCuFt
//     — 998 ft³ on a flatbed), but the tier packer that places the boards only
//     achieves 20-35% because bundles are banded at most 30 in tall and each
//     shelf row is as deep as its longest board. Sized to the assignment budget
//     a truck packs past its 96 in bed height and starts dropping SKUs as "truck
//     full". Every load here lands at 47-85.5 in of load height with 0 unplaced.
//
//  5. NON-STACKABLE ARTICLES SEAL THE LOAD. Two stops carry one — Mission
//     Hill's crated exterior door and Lake Country's 6x6 PT timbers. AI_LM's
//     packer seals both the level and the tier under a non-stackable article,
//     so such a stop must satisfy two things at once. First, it must be stop 1
//     on its truck (closest to the depot): packing runs LIFO in reverse route
//     order, so a non-stackable article on a later stop seals the bed and
//     strands every earlier stop as Unplaced. Second, its whole order must fit
//     on ONE level of the bed footprint, because the article seals the level the
//     moment it is placed and, being the heaviest article in its tier, it is
//     placed first. Both stops are therefore deliberately SMALL: a finish
//     package and a timber drop. That makes the rule visible in a demo instead
//     of destructive.
//
// One more rule, by omission: NO dispatch-day order references a SKU without
// geometry, even though eleven such SKUs remain in the catalog above. AI_LM's
// packer reports a dimensionless article as Unplaced "(no geometry)", and
// blockingCapacityReasons folds ANY Unplaced entry into "SKU(s) did not fit and
// were dropped", which Push refuses outright. A single box of joist hangers on
// a truck therefore blocks the whole plan from reaching GableLBM. Keeping the
// day's lines dimensioned is what lets the demo finish; the NULL-geometry SKUs
// stay in the catalog because that is the truth about them, and AI_LM
// distinguishing "no geometry recorded" from "would not fit" is the fix.
var dispatchStops = []dispatchStop{
	// --- West side: Mission Hill -> Westbank -> Peachland (flatbed, 30 000 lb) --
	// Small finish package: it rides on top and the crated door seals the load.
	{
		Customer: "Mission Hill Custom", Lat: 49.8404, Lng: -119.6003,
		Instructions: "Finish package — unload in the garage, door stays crated until the painter signs off.",
		Lines: []dispatchLine{
			{"DR-EXT-3680-STL", 1}, // non-stackable: seals the top tier
			{"DR-INT-3680-6P", 6},
			{"MLD-BASE-MDF", 40},
			{"MLD-CASE-MDF", 40},
		},
	},
	{
		Customer: "Westbank Decks & Fence", Lat: 49.8318, Lng: -119.6187,
		Instructions: "Deck package — drop on the driveway, homeowner on site after 09:00.",
		Lines: []dispatchLine{
			{"LUM-448-PT", 40},
			{"LUM-4410-PT", 16},
			{"LUM-2810-NO2", 48},
			{"LUM-166-WRC", 160},
			{"SCR-DECK-3-5", 12},
			{"LUM-2612-PREM", 32},
		},
	},
	{
		Customer: "Peachland Framing Crew", Lat: 49.7742, Lng: -119.7361,
		Instructions: "Warehouse framing — boom over the fence to the slab, crew will spot.",
		Lines: []dispatchLine{
			{"LUM-2492-STUD", 160},
			{"LUM-2616-PREM", 60},
			{"OSB-34-TG", 24},
			{"LUM-21216-NO2", 16},
			{"NAIL-16D-50", 2},
		},
	},

	// --- South + east outliers: Summerland -> Big White -> Rutland (24 000 lb) --
	{
		Customer: "Summerland Roofers", Lat: 49.6005, Lng: -119.6702,
		Instructions: "Reroof — shingles to the driveway, felt and ice-and-water to the garage.",
		Lines: []dispatchLine{
			{"RF-ARCH-30", 48},
			{"RF-FELT-15", 6},
			{"RF-ICE-65", 8},
			{"RF-DRIP-10", 12},
			{"RF-START", 8},
			{"RF-NAIL-125", 8},
			{"PLY-12-CDX", 8},
		},
	},
	{
		Customer: "Big White Cabin Co", Lat: 49.7208, Lng: -118.9403,
		Instructions: "Mountain run — chains on above the village turnoff, gate code with the site super.",
		Lines: []dispatchLine{
			{"LUM-2410-PREM", 50},
			{"PLY-34-CDX", 10},
			{"DW-12-REG", 6},
			{"LUM-448-WRC", 16},
			{"MLD-BASE-MDF", 16},
		},
	},
	{
		Customer: "Kelbrook Construction", Lat: 49.8990, Lng: -119.3830,
		Instructions: "Framing package — stage on the north side, forklift on site.",
		Lines: []dispatchLine{
			{"LUM-248-PREM", 150},
			{"LUM-2610-PREM", 40},
			{"LUM-21012-NO2", 8},
			{"OSB-12", 12},
			{"LUM-2816-NO2", 4},
			{"NAIL-16D-50", 2},
		},
	},

	// --- In-town Kelowna -> Vernon -> Lake Country (24 000 lb flatbed) ---------
	{
		Customer: "Glenmore Heritage Reno", Lat: 49.9105, Lng: -119.4460,
		Instructions: "Heritage reno — narrow lane behind the house, hand-bomb the millwork.",
		Lines: []dispatchLine{
			{"DR-INT-3080-6P", 6},
			{"DW-12-REG", 24},
			{"MLD-BASE-MDF", 50},
			{"MLD-CASE-MDF", 60},
			{"PLY-12-CDX", 16},
			{"LUM-2492-STUD", 60},
		},
	},
	{
		Customer: "Vernon Valley Construction", Lat: 50.2671, Lng: -119.2720,
		Instructions: "Formwork — deliver before the 13:00 pour, call the super from the gate.",
		Lines: []dispatchLine{
			{"PLY-34-CDX", 26},
			{"LUM-2412-PREM", 60},
			{"LUM-448-PT", 16},
			{"OSB-34-TG", 6},
			{"LUM-2612-PREM", 5},
		},
	},
	{
		Customer: "Okanagan DIY Owner", Lat: 50.0793, Lng: -119.3960,
		Instructions: "Retail shed kit — leave beside the garage, COD collected at the counter.",
		Lines: []dispatchLine{
			{"LUM-248-PREM", 40},
			{"OSB-12", 8},
			{"RF-SH-BLK", 2},
			{"SCR-DECK-3-5", 4},
		},
	},

	// --- North: the boom truck (18 000 lb) takes the two timber/deck stops -----
	// Small timber drop: it rides on top and the 6x6 course seals the load.
	{
		Customer: "Lake Country Builders", Lat: 50.0503, Lng: -119.4098,
		Instructions: "Cottage build — timbers off first, keep them off the grass.",
		Lines: []dispatchLine{
			{"LUM-6612-PT", 12}, // non-stackable: seals the top tier
			{"LUM-21012-NO2", 24},
			{"LUM-2610-PREM", 60},
			{"LUM-166-WRC", 120},
		},
	},
	{
		Customer: "Predator Ridge Renos", Lat: 50.2004, Lng: -119.3702,
		Instructions: "Deck reno — narrow lane, back in from the upper cul-de-sac.",
		Lines: []dispatchLine{
			{"LUM-448-PT", 40},
			{"LUM-4410-PT", 30},
			{"LUM-2810-NO2", 40},
			{"PLY-34-CDX", 12},
			{"LUM-166-WRC", 100},
			{"SCR-DECK-3-5", 8},
			{"LUM-2612-PREM", 15},
		},
	},

	// --- The box truck (16 000 lb) picks up what the boom truck could not ------
	// Okanagan Homes is the heaviest stop of the day: it overflows the boom
	// truck's 14 400 lb working cap and pulls the box truck into the plan.
	{
		Customer: "Okanagan Homes Ltd", Lat: 49.9204, Lng: -119.4585,
		Instructions: "Building B drywall + insulation — loading bay, liftgate required.",
		Lines: []dispatchLine{
			{"DW-12-REG", 64},
			{"DW-58-FC", 24},
			{"LUM-2492-STUD", 160},
			{"INS-R19-15", 6}, // bulky, low density: the truck's cube, not its payload
			{"PLY-34-CDX", 10},
			{"LUM-2412-PREM", 20},
			{"CORNSFT1612V", 6},
		},
	},
	{
		Customer: "Knox Mountain Landscapes", Lat: 49.9019, Lng: -119.4881,
		Instructions: "Community garden beds — park on the gravel, no vehicles on the path.",
		Lines: []dispatchLine{
			{"LUM-448-PT", 12},
			{"LUM-166-WRC", 60},
			{"LUM-2810-NO2", 6},
			{"SCR-DECK-3-5", 6},
			{"CORNPOLY18", 2},
		},
	},
}

// seedDispatchDay writes the dispatch-day fixture: one CONFIRMED order per stop
// carrying scheduled_delivery_date, its lines, and a `deliveries` row holding
// the stop's coordinates.
//
// The delivery row is inserted with route_id NULL — deliveries.route_id is
// nullable (migration 009) and a NULL there is precisely the state this fixture
// wants: "this stop has been geocoded but is not on a route yet". That is what
// AI_LM exists to fix, and it is what its integration query reads: a LATERAL
// pick of one geocoded delivery per order, route or no route. Hanging these on
// a placeholder DRAFT route instead would put a phantom route on GableLBM's
// dispatch board and, worse, AI_LM's push (ReplaceDeliveryRoute) deletes DRAFT
// and SCHEDULED routes for the same vehicle+date, so a placeholder could be
// silently swallowed mid-demo.
func seedDispatchDay(
	db *sql.DB,
	customerIDs map[string]uuid.UUID,
	custToBranch map[uuid.UUID]uuid.UUID,
	custSalesperson map[uuid.UUID]string,
	skuToID map[string]uuid.UUID,
	productPrices map[string]float64,
	productWeights map[string]float64,
	fallbackBranch uuid.UUID,
) {
	date := dispatchDate()
	// Taken a couple of days before the run so the order looks like it was
	// booked and then scheduled, not conjured on the morning of.
	createdAt := date.AddDate(0, 0, -2)

	orders, stops, lines := 0, 0, 0
	var dayWeight float64

	for _, s := range dispatchStops {
		custID, ok := customerIDs[s.Customer]
		if !ok {
			log.Printf("seedDispatchDay: no customer %q — skipping stop", s.Customer)
			continue
		}
		branchID := custToBranch[custID]
		if branchID == uuid.Nil {
			branchID = fallbackBranch
		}

		orderID := uuid.New()
		var spID any
		if sp, ok := custSalesperson[custID]; ok && sp != "" {
			spID = sp
		}
		if _, err := db.Exec(`INSERT INTO orders
			(id, customer_id, branch_id, total_amount, status, salesperson_id, created_at, scheduled_delivery_date)
			VALUES ($1,$2,$3,0,'CONFIRMED',$4,$5,$6::date)`,
			orderID, custID, branchID, spID, createdAt, date.Format("2006-01-02")); err != nil {
			log.Printf("seedDispatchDay: order for %s: %v", s.Customer, err)
			continue
		}
		orders++

		var total, weight float64
		for _, ln := range s.Lines {
			pid, ok := skuToID[ln.SKU]
			if !ok {
				log.Printf("seedDispatchDay: %s references unknown sku %q", s.Customer, ln.SKU)
				continue
			}
			price := productPrices[ln.SKU]
			total += float64(ln.Qty) * price
			weight += float64(ln.Qty) * productWeights[ln.SKU]
			if _, err := db.Exec(`INSERT INTO order_lines (order_id, product_id, quantity, price_each)
				VALUES ($1,$2,$3,$4)`, orderID, pid, ln.Qty, price); err != nil {
				log.Printf("seedDispatchDay: line %s for %s: %v", ln.SKU, s.Customer, err)
				continue
			}
			lines++
		}
		db.Exec(`UPDATE orders SET total_amount=$1 WHERE id=$2`, total, orderID)
		dayWeight += weight

		// stop_sequence 0 = not yet sequenced. AI_LM's push writes the real
		// sequence onto its own delivery rows.
		if _, err := db.Exec(`INSERT INTO deliveries
			(order_id, stop_sequence, status, latitude, longitude, delivery_instructions, created_at)
			VALUES ($1, 0, 'PENDING', $2, $3, $4, $5)`,
			orderID, s.Lat, s.Lng, s.Instructions, createdAt); err != nil {
			log.Printf("seedDispatchDay: delivery for %s: %v", s.Customer, err)
			continue
		}
		stops++
	}

	fmt.Printf("Seed: Dispatch day %s — %d CONFIRMED orders, %d geocoded stops, %d lines, %.0f lbs of freight\n",
		date.Format("2006-01-02"), orders, stops, lines, dayWeight)
	fmt.Printf("Seed:   AI_LM: POST /api/v1/workflow/plans {\"date\":%q} (override with %s=YYYY-MM-DD)\n",
		date.Format("2006-01-02"), dispatchDateEnv)
}
