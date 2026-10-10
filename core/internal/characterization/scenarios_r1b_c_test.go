// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

// R1-1b drafter C: the remaining delivery, POS and purchase-order routes.
// Every variable these groups extract is prefixed c_ so it can never collide
// with another drafter's. The groups run after every earlier group, so they
// depend only on the seed vars and the vars the earlier groups extract
// (myCustomer, myOrder, myCancelOrder, myPO, myLocation, ...), and they build
// their own vehicles, drivers, routes, tills, transactions and purchase orders
// for anything destructive.

const (
	// A well formed id that no row owns, for the not-found paths.
	cMissingID = "00000000-0000-4000-8000-0000000000c1"
	cPhoto     = "golden photo bytes"
)

func r1bCGroups() []groupDef {
	return concat(
		r1bCDeliveryGroups(),
		r1bCMaskedGroups(),
		r1bCPOSGroups(),
		r1bCPurchaseOrderGroups(),
	)
}

func r1bCDeliveryGroups() []groupDef {
	return []groupDef{
		{
			name: "delivery_fleet",
			steps: []stepDef{
				{
					name: "delivery.vehicle.create.c", method: "POST", path: "/api/v1/delivery/vehicles",
					body: map[string]any{
						"name": "R1C Truck", "vehicle_type": "box_truck", "license_plate": "R1C0001",
						"capacity_weight_lbs": 20000, "year": 2022, "make": "Freightliner", "model": "M2",
					},
					extract: map[string]string{"c_vehicle": "/id"},
				},
				{
					name: "delivery.vehicle.create.c_spare", method: "POST", path: "/api/v1/delivery/vehicles",
					body:    map[string]any{"name": "R1C Spare", "vehicle_type": "flatbed", "license_plate": "R1C0002"},
					extract: map[string]string{"c_vehicle_del": "/id"},
				},
				{name: "delivery.vehicle.create.bad_body", method: "POST", path: "/api/v1/delivery/vehicles", body: "not an object"},
				{name: "delivery.vehicle.create.uppercase_type", method: "POST", path: "/api/v1/delivery/vehicles",
					body: map[string]any{"name": "x", "vehicle_type": "BOX_TRUCK", "license_plate": "R1CUPPER"}},
				{name: "delivery.vehicle.create.unknown_field", method: "POST", path: "/api/v1/delivery/vehicles",
					body: map[string]any{"name": "x", "vehicle_type": "van", "license_plate": "R1CUNKNOWN", "smoke": "signal"}},
				{name: "delivery.vehicle.get.c", method: "GET", path: "/api/v1/delivery/vehicles/{c_vehicle}"},
				{name: "delivery.vehicle.get.not_found", method: "GET", path: "/api/v1/delivery/vehicles/" + cMissingID},
				{name: "delivery.vehicle.get.bad_id", method: "GET", path: "/api/v1/delivery/vehicles/not-a-uuid"},
				{name: "delivery.vehicle.update.no_precondition", method: "PUT", path: "/api/v1/delivery/vehicles/{c_vehicle}",
					body: map[string]any{"name": "R1C Truck Renamed", "vehicle_type": "box_truck", "license_plate": "R1C0001"}},
				{name: "delivery.vehicle.update.stale", method: "PUT", path: "/api/v1/delivery/vehicles/{c_vehicle}",
					headers: map[string]string{"If-Match": `"9"`},
					body:    map[string]any{"name": "R1C Truck Renamed", "vehicle_type": "box_truck", "license_plate": "R1C0001"}},
				{
					name: "delivery.vehicle.update", method: "PUT", path: "/api/v1/delivery/vehicles/{c_vehicle}",
					headers: map[string]string{"If-Match": `"1"`},
					body: map[string]any{
						"name": "R1C Truck Renamed", "vehicle_type": "box_truck", "license_plate": "R1C0001",
						"capacity_weight_lbs": 22000, "odometer_miles": 1200, "notes": "r1b c update",
					},
				},
				{name: "delivery.vehicle.update.bad_id", method: "PUT", path: "/api/v1/delivery/vehicles/not-a-uuid",
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"name": "x", "vehicle_type": "box_truck", "license_plate": "X"}},
				{name: "delivery.vehicle.update.bad_body", method: "PUT", path: "/api/v1/delivery/vehicles/{c_vehicle}", body: "not an object"},
				{name: "delivery.vehicle.update.not_found", method: "PUT", path: "/api/v1/delivery/vehicles/" + cMissingID,
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"name": "ghost", "vehicle_type": "box_truck", "license_plate": "GHOST"}},
				{
					name: "delivery.vehicle.photo", method: "POST", path: "/api/v1/delivery/vehicles/{c_vehicle}/photo",
					body: &multipartDef{FieldName: "photo", Filename: "truck.png", Content: cPhoto},
				},
				{
					name: "delivery.vehicle.photo.bad_type", method: "POST", path: "/api/v1/delivery/vehicles/{c_vehicle}/photo",
					body: &multipartDef{FieldName: "photo", Filename: "truck.txt", Content: cPhoto},
				},
				{
					name: "delivery.vehicle.photo.wrong_field", method: "POST", path: "/api/v1/delivery/vehicles/{c_vehicle}/photo",
					body: &multipartDef{FieldName: "file", Filename: "truck.png", Content: cPhoto},
				},
				{
					name: "delivery.vehicle.photo.bad_id", method: "POST", path: "/api/v1/delivery/vehicles/not-a-uuid/photo",
					body: &multipartDef{FieldName: "photo", Filename: "truck.png", Content: cPhoto},
				},
				{
					name: "delivery.vehicle.photo.not_found", method: "POST", path: "/api/v1/delivery/vehicles/" + cMissingID + "/photo",
					body: &multipartDef{FieldName: "photo", Filename: "truck.png", Content: cPhoto},
				},
				{
					name: "delivery.driver.create.c", method: "POST", path: "/api/v1/delivery/drivers",
					body: map[string]any{
						"name": "R1C Driver", "license_number": "R1C-DL-1", "phone_number": "250-555-0101",
						"cdl_class": "B", "email": "r1c.driver@example.com",
					},
					extract: map[string]string{"c_driver": "/id"},
				},
				{
					name: "delivery.driver.create.c_spare", method: "POST", path: "/api/v1/delivery/drivers",
					body:    map[string]any{"name": "R1C Spare Driver"},
					extract: map[string]string{"c_driver_del": "/id"},
				},
				{name: "delivery.driver.create.bad_body", method: "POST", path: "/api/v1/delivery/drivers", body: "not an object"},
				{name: "delivery.driver.list", method: "GET", path: "/api/v1/delivery/drivers?limit=200", sortEnvelopeItems: true},
				{name: "delivery.driver.get.not_found", method: "GET", path: "/api/v1/delivery/drivers/" + cMissingID},
				{name: "delivery.driver.get.bad_id", method: "GET", path: "/api/v1/delivery/drivers/not-a-uuid"},
				{name: "delivery.driver.update.no_precondition", method: "PUT", path: "/api/v1/delivery/drivers/{c_driver}",
					body: map[string]any{"name": "R1C Driver Renamed", "status": "active"}},
				{
					name: "delivery.driver.update", method: "PUT", path: "/api/v1/delivery/drivers/{c_driver}",
					headers: map[string]string{"If-Match": `"1"`},
					body: map[string]any{
						"name": "R1C Driver Renamed", "license_number": "R1C-DL-1", "phone_number": "250-555-0102",
						"status": "active", "cdl_class": "A",
					},
				},
				{name: "delivery.driver.update.uppercase_status", method: "PUT", path: "/api/v1/delivery/drivers/{c_driver}",
					headers: map[string]string{"If-Match": `"2"`},
					body:    map[string]any{"name": "R1C Driver Renamed", "status": "ACTIVE"}},
				{name: "delivery.driver.update.missing_status", method: "PUT", path: "/api/v1/delivery/drivers/{c_driver}",
					headers: map[string]string{"If-Match": `"2"`},
					body:    map[string]any{"name": "R1C Driver Renamed"}},
				{name: "delivery.driver.update.bad_id", method: "PUT", path: "/api/v1/delivery/drivers/not-a-uuid",
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"name": "x", "status": "active"}},
				{name: "delivery.driver.update.bad_body", method: "PUT", path: "/api/v1/delivery/drivers/{c_driver}", body: "not an object"},
				{name: "delivery.driver.update.not_found", method: "PUT", path: "/api/v1/delivery/drivers/" + cMissingID,
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"name": "ghost", "status": "active"}},
				{
					name: "delivery.driver.photo", method: "POST", path: "/api/v1/delivery/drivers/{c_driver}/photo",
					body: &multipartDef{FieldName: "photo", Filename: "driver.jpg", Content: cPhoto},
				},
				{
					name: "delivery.driver.photo.bad_type", method: "POST", path: "/api/v1/delivery/drivers/{c_driver}/photo",
					body: &multipartDef{FieldName: "photo", Filename: "driver.gif", Content: cPhoto},
				},
				{
					name: "delivery.driver.photo.bad_id", method: "POST", path: "/api/v1/delivery/drivers/not-a-uuid/photo",
					body: &multipartDef{FieldName: "photo", Filename: "driver.jpg", Content: cPhoto},
				},
				{
					name: "delivery.driver.photo.not_found", method: "POST", path: "/api/v1/delivery/drivers/" + cMissingID + "/photo",
					body: &multipartDef{FieldName: "photo", Filename: "driver.jpg", Content: cPhoto},
				},
				{name: "delivery.vehicle.get.c_after", method: "GET", path: "/api/v1/delivery/vehicles/{c_vehicle}"},
				{name: "delivery.driver.get.c_after", method: "GET", path: "/api/v1/delivery/drivers/{c_driver}"},
				{name: "delivery.vehicle.list", method: "GET", path: "/api/v1/delivery/vehicles?limit=200", sortEnvelopeItems: true},
				{name: "delivery.vehicle.list_unknown_param", method: "GET", path: "/api/v1/delivery/vehicles?flavour=vanilla"},
				{name: "delivery.vehicle.list_bad_limit", method: "GET", path: "/api/v1/delivery/vehicles?limit=0"},
				// Destructive steps run on the spare copies only.
				{name: "delivery.vehicle.delete.no_precondition", method: "DELETE", path: "/api/v1/delivery/vehicles/{c_vehicle_del}"},
				{name: "delivery.vehicle.delete", method: "DELETE", path: "/api/v1/delivery/vehicles/{c_vehicle_del}",
					headers: map[string]string{"If-Match": `"1"`}},
				{name: "delivery.vehicle.get.deleted", method: "GET", path: "/api/v1/delivery/vehicles/{c_vehicle_del}"},
				{name: "delivery.vehicle.delete.bad_id", method: "DELETE", path: "/api/v1/delivery/vehicles/not-a-uuid",
					headers: map[string]string{"If-Match": `"1"`}},
				{name: "delivery.vehicle.delete.not_found", method: "DELETE", path: "/api/v1/delivery/vehicles/" + cMissingID,
					headers: map[string]string{"If-Match": `"1"`}},
				{name: "delivery.driver.delete.no_precondition", method: "DELETE", path: "/api/v1/delivery/drivers/{c_driver_del}"},
				{name: "delivery.driver.delete", method: "DELETE", path: "/api/v1/delivery/drivers/{c_driver_del}",
					headers: map[string]string{"If-Match": `"1"`}},
				{name: "delivery.driver.get.deleted", method: "GET", path: "/api/v1/delivery/drivers/{c_driver_del}"},
				{name: "delivery.driver.delete.bad_id", method: "DELETE", path: "/api/v1/delivery/drivers/not-a-uuid",
					headers: map[string]string{"If-Match": `"1"`}},
				{name: "delivery.driver.delete.not_found", method: "DELETE", path: "/api/v1/delivery/drivers/" + cMissingID,
					headers: map[string]string{"If-Match": `"1"`}},
			},
		},
		{
			name: "delivery_routes",
			steps: []stepDef{
				{
					name: "delivery.route.create.c", method: "POST", path: "/api/v1/delivery/routes",
					body: map[string]any{
						"vehicle_id": "{c_vehicle}", "driver_id": "{c_driver}", "scheduled_date": "{today+2}",
						"notes": "r1b c main route",
					},
					extract: map[string]string{"c_route": "/id"},
				},
				{
					name: "delivery.route.create.c_empty", method: "POST", path: "/api/v1/delivery/routes",
					body: map[string]any{
						"vehicle_id": "{c_vehicle}", "driver_id": "{c_driver}", "scheduled_date": "{today+3}",
					},
					extract: map[string]string{"c_route_empty": "/id"},
				},
				{name: "delivery.route.create.bad_body", method: "POST", path: "/api/v1/delivery/routes", body: "not an object"},
				{name: "delivery.route.create.bad_date", method: "POST", path: "/api/v1/delivery/routes",
					body: map[string]any{"vehicle_id": "{c_vehicle}", "driver_id": "{c_driver}", "scheduled_date": "next tuesday"}},
				{name: "delivery.route.create.unknown_vehicle", method: "POST", path: "/api/v1/delivery/routes",
					body: map[string]any{"vehicle_id": cMissingID, "driver_id": "{c_driver}", "scheduled_date": "{today+2}"}},
				{name: "delivery.route.list_by_date", method: "GET", path: "/api/v1/delivery/routes?date={today+2}"},
				{name: "delivery.route.list_board_empty", method: "GET", path: "/api/v1/delivery/routes?date={today+2}&include=stops"},
				{name: "delivery.route.list_by_driver", method: "GET", path: "/api/v1/delivery/routes?driver_id={c_driver}&limit=200", sortEnvelopeItems: true},
				{name: "delivery.route.list_bad_driver", method: "GET", path: "/api/v1/delivery/routes?driver_id=not-a-uuid"},
				{name: "delivery.route.list_bad_date", method: "GET", path: "/api/v1/delivery/routes?date=next-tuesday"},
				{name: "delivery.route.list_unknown_param", method: "GET", path: "/api/v1/delivery/routes?flavour=vanilla"},
				{name: "delivery.route.list_bad_status", method: "GET", path: "/api/v1/delivery/routes?status=DRAFT"},
				{name: "delivery.route.list_status_filter", method: "GET", path: "/api/v1/delivery/routes?status=draft&date={today+2}"},
				{name: "delivery.route.get.c", method: "GET", path: "/api/v1/delivery/routes/{c_route}"},
				{name: "delivery.route.get.not_found", method: "GET", path: "/api/v1/delivery/routes/" + cMissingID},
				{name: "delivery.route.get.bad_id", method: "GET", path: "/api/v1/delivery/routes/not-a-uuid"},
				{name: "delivery.route.deliveries.empty", method: "GET", path: "/api/v1/delivery/routes/{c_route}/deliveries"},
				{name: "delivery.route.deliveries.not_found", method: "GET", path: "/api/v1/delivery/routes/" + cMissingID + "/deliveries"},
				{name: "delivery.route.deliveries.bad_id", method: "GET", path: "/api/v1/delivery/routes/not-a-uuid/deliveries"},
			},
		},
	}
}

// r1bCMaskedGroups holds the groups that cannot be recorded byte-stable with
// today's normaliser, so they are deliberately NOT part of r1bCGroups(). They
// become recordable (append them after r1bCGroups(), in this order) once the
// harness gains the masks named below; each is needed because of a field the
// product derives from a per-run id or a per-run seed draw, never because of
// the product being unsteady for a fixed input.
//
//   - latitude and longitude on delivery rows: with no routing key, assigning
//     an order mints coordinates from bytes 0 and 1 of the ORDER UUID
//     (delivery.mockGeocode), and order ids are random per run. Needed on
//     every step that echoes a delivery: delivery.delivery.assign.first,
//     delivery.delivery.assign.second, delivery.delivery.get,
//     delivery.route.deliveries.two, delivery.route.deliveries.optimized,
//     delivery.route.deliveries.reordered and delivery.delivery.get.after_status
//     (the maskMockGeo stepDef flag: only values inside the mock geocoder's
//     band are masked, so a null or out of band coordinate still shows).
//   - the POD photo url: HandleUploadPODPhoto names the file
//     "<delivery id>-<8 random hex>.<ext>" and returns the url; the normaliser
//     masks full UUIDs only. Needed on delivery.delivery.pod_photo
//     and delivery.delivery.pod_photos
//     (mask the "-<8 hex>" suffix of /uploads/pod/ urls).
//   - vendor_id on the purchase order list: the demo seed assigns vendors to
//     its purchase orders from rand draws consumed inside map-iteration loops,
//     so rows that differ only by id and vendor tie on the sort key and the
//     placeholder numbering shifts between runs. Needed on
//     purchase_order.list (mask vendor_id, like
//     maskCustomerIdentity does for customers).
//
// Masks for the fields the product derives from per-run ids or draws (see
// the list above); each is recorded in the golden next to the step.
var (
	photoMask  = map[string]any{"photo_url": "<pod-photo-url>"}
	vendorMask = map[string]any{"vendor_id": "<vendor>"}
)

func r1bCMaskedGroups() []groupDef {
	return []groupDef{
		{
			name: "delivery_deliveries",
			steps: []stepDef{
				{
					name: "delivery.delivery.assign.first", method: "POST", path: "/api/v1/delivery/deliveries", maskMockGeo: true,
					body: map[string]any{
						"route_id": "{c_route}", "order_id": "{myOrder}", "stop_sequence": 1,
						"delivery_instructions": "r1b c first stop",
					},
					extract: map[string]string{"c_delivery": "/delivery/id"},
				},
				{
					name: "delivery.delivery.assign.second", method: "POST", path: "/api/v1/delivery/deliveries", maskMockGeo: true,
					body: map[string]any{
						"route_id": "{c_route}", "order_id": "{myCancelOrder}", "stop_sequence": 2,
					},
					extract: map[string]string{"c_delivery2": "/delivery/id"},
				},
				{name: "delivery.delivery.assign.bad_body", method: "POST", path: "/api/v1/delivery/deliveries", body: "not an object"},
				{name: "delivery.delivery.assign.unknown_route", method: "POST", path: "/api/v1/delivery/deliveries",
					body: map[string]any{"route_id": cMissingID, "order_id": "{myOrder}", "stop_sequence": 1}},
				{name: "delivery.delivery.assign.unknown_field", method: "POST", path: "/api/v1/delivery/deliveries",
					body: map[string]any{"route_id": "{c_route}", "order_id": "{myOrder}", "smoke": "signal"}},
				{name: "delivery.delivery.get", method: "GET", path: "/api/v1/delivery/deliveries/{c_delivery}", maskMockGeo: true},
				{name: "delivery.delivery.get.not_found", method: "GET", path: "/api/v1/delivery/deliveries/" + cMissingID},
				{name: "delivery.delivery.get.bad_id", method: "GET", path: "/api/v1/delivery/deliveries/not-a-uuid"},
				{name: "delivery.route.deliveries.two", method: "GET", path: "/api/v1/delivery/routes/{c_route}/deliveries", maskMockGeo: true},
				{
					name: "delivery.delivery.adjust_qty", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery}/adjust-qty", maskMockGeo: true,
					body: map[string]any{
						"adjusted_by": "{c_driver}",
						"adjustments": []map[string]any{
							{"product_id": "{product}", "original_qty": "10", "adjusted_qty": "8", "reason_code": "short_ship", "notes": "two short"},
						},
					},
				},
				{name: "delivery.delivery.adjust_qty.float_qty", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery}/adjust-qty",
					body: map[string]any{
						"adjusted_by": "{c_driver}",
						"adjustments": []map[string]any{
							{"product_id": "{product}", "original_qty": 10, "adjusted_qty": 8, "reason_code": "short_ship"},
						},
					}},
				{name: "delivery.delivery.adjust_qty.uppercase_reason", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery}/adjust-qty",
					body: map[string]any{
						"adjusted_by": "{c_driver}",
						"adjustments": []map[string]any{
							{"product_id": "{product}", "original_qty": "10", "adjusted_qty": "8", "reason_code": "SHORT_SHIP"},
						},
					}},
				{name: "delivery.route.list_board_stops", method: "GET", path: "/api/v1/delivery/routes?date={today+2}&include=stops", sortEnvelopeItems: true, maskMockGeo: true},
				{name: "delivery.delivery.adjust_qty.bad_id", method: "POST", path: "/api/v1/delivery/deliveries/not-a-uuid/adjust-qty",
					body: map[string]any{"adjustments": []map[string]any{}}},
				{name: "delivery.delivery.adjust_qty.bad_body", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery}/adjust-qty", body: "not an object"},
				{name: "delivery.delivery.adjust_qty.not_found", method: "POST", path: "/api/v1/delivery/deliveries/" + cMissingID + "/adjust-qty",
					body: map[string]any{"adjustments": []map[string]any{}}},
				{name: "delivery.delivery.pod_photos.empty", method: "GET", path: "/api/v1/delivery/deliveries/{c_delivery}/pod-photos"},
				{
					name: "delivery.delivery.pod_photo.bad_type", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery}/pod-photo",
					body: &multipartDef{FieldName: "photo", Filename: "pod.pdf", Content: cPhoto},
				},
				{
					name: "delivery.delivery.pod_photo.wrong_field", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery}/pod-photo",
					body: &multipartDef{FieldName: "file", Filename: "pod.png", Content: cPhoto},
				},
				{
					name: "delivery.delivery.pod_photo.bad_id", method: "POST", path: "/api/v1/delivery/deliveries/not-a-uuid/pod-photo",
					body: &multipartDef{FieldName: "photo", Filename: "pod.png", Content: cPhoto},
				},
				{
					name: "delivery.delivery.pod_photo.not_found", method: "POST", path: "/api/v1/delivery/deliveries/" + cMissingID + "/pod-photo",
					body: &multipartDef{FieldName: "photo", Filename: "pod.png", Content: cPhoto},
				},
				{name: "delivery.delivery.pod_photos.bad_id", method: "GET", path: "/api/v1/delivery/deliveries/not-a-uuid/pod-photos"},
				{name: "delivery.delivery.pod_photos.none", method: "GET", path: "/api/v1/delivery/deliveries/" + cMissingID + "/pod-photos"},
			},
		},
		{
			name: "delivery_pod_photo",
			steps: []stepDef{
				{
					name: "delivery.delivery.pod_photo", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery2}/pod-photo", maskFields: photoMask,
					body: &multipartDef{FieldName: "photo", Filename: "pod.png", Content: cPhoto},
				},
				{name: "delivery.delivery.pod_photos", method: "GET", path: "/api/v1/delivery/deliveries/{c_delivery2}/pod-photos", sortEnvelopeItems: true, maskFields: photoMask},
			},
		},
		{
			name: "delivery_route_lifecycle",
			steps: []stepDef{
				{name: "delivery.route.optimize.no_precondition", method: "POST", path: "/api/v1/delivery/routes/{c_route}/optimize"},
				{name: "delivery.route.optimize", method: "POST", path: "/api/v1/delivery/routes/{c_route}/optimize",
					headers: map[string]string{"If-Match": `"3"`}, maskMockGeo: true},
				{name: "delivery.route.optimize.empty", method: "POST", path: "/api/v1/delivery/routes/{c_route_empty}/optimize",
					headers: map[string]string{"If-Match": `"1"`}},
				{name: "delivery.route.optimize.bad_id", method: "POST", path: "/api/v1/delivery/routes/not-a-uuid/optimize"},
				{name: "delivery.route.deliveries.optimized", method: "GET", path: "/api/v1/delivery/routes/{c_route}/deliveries", maskMockGeo: true},
				{name: "delivery.route.reorder.partial", method: "POST", path: "/api/v1/delivery/routes/{c_route}/reorder", maskMockGeo: true,
					headers: map[string]string{"If-Match": `"4"`},
					body:    map[string]any{"ordered_delivery_ids": []any{"{c_delivery2}"}}},
				{
					name: "delivery.route.reorder", method: "POST", path: "/api/v1/delivery/routes/{c_route}/reorder", maskMockGeo: true,
					headers: map[string]string{"If-Match": `"4"`},
					body:    map[string]any{"ordered_delivery_ids": []any{"{c_delivery2}", "{c_delivery}"}},
				},
				{name: "delivery.route.reorder.bad_id", method: "POST", path: "/api/v1/delivery/routes/not-a-uuid/reorder",
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"ordered_delivery_ids": []string{}}},
				{name: "delivery.route.reorder.bad_body", method: "POST", path: "/api/v1/delivery/routes/{c_route}/reorder", body: "not an object"},
				{name: "delivery.route.reorder.unknown_delivery", method: "POST", path: "/api/v1/delivery/routes/{c_route}/reorder",
					headers: map[string]string{"If-Match": `"5"`},
					body:    map[string]any{"ordered_delivery_ids": []any{cMissingID}}},
				{name: "delivery.route.deliveries.reordered", method: "GET", path: "/api/v1/delivery/routes/{c_route}/deliveries", maskMockGeo: true},
				{name: "delivery.route.complete.no_precondition", method: "POST", path: "/api/v1/delivery/routes/{c_route}/transitions",
					body: map[string]any{"to": "completed"}},
				{name: "delivery.route.complete.pending", method: "POST", path: "/api/v1/delivery/routes/{c_route}/transitions",
					headers: map[string]string{"If-Match": `"5"`},
					body:    map[string]any{"to": "completed"}},
				{name: "delivery.route.complete.empty", method: "POST", path: "/api/v1/delivery/routes/{c_route_empty}/transitions",
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"to": "completed"}},
				{name: "delivery.route.complete.bad_id", method: "POST", path: "/api/v1/delivery/routes/not-a-uuid/transitions",
					body: map[string]any{"to": "completed"}},
				{name: "delivery.route.transition.invalid_target", method: "POST", path: "/api/v1/delivery/routes/{c_route}/transitions",
					headers: map[string]string{"If-Match": `"5"`},
					body:    map[string]any{"to": "scheduled"}},
				{name: "delivery.route.dispatch", method: "POST", path: "/api/v1/delivery/routes/{c_route}/transitions", maskMockGeo: true,
					headers: map[string]string{"If-Match": `"5"`},
					body:    map[string]any{"to": "in_transit"}},
				{name: "delivery.route.dispatch.again", method: "POST", path: "/api/v1/delivery/routes/{c_route}/transitions", maskMockGeo: true,
					headers: map[string]string{"If-Match": `"6"`},
					body:    map[string]any{"to": "in_transit"}},
				{name: "delivery.route.dispatch.bad_id", method: "POST", path: "/api/v1/delivery/routes/not-a-uuid/transitions",
					body: map[string]any{"to": "in_transit"}},
				{name: "delivery.route.dispatch.not_found", method: "POST", path: "/api/v1/delivery/routes/" + cMissingID + "/transitions",
					body: map[string]any{"to": "in_transit"}},
				// Delivered has its own group (delivery_delivered): it queues
				// the order's fulfilment request, so it runs on an order of
				// its own.
				{
					name: "delivery.delivery.status.no_precondition", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery}/transitions",
					body: map[string]any{"to": "partial"},
				},
				{
					name: "delivery.delivery.status.partial", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery}/transitions", maskMockGeo: true,
					body: map[string]any{
						"to": "partial", "revision": 2, "pod_proof_url": "/uploads/pod/golden.png", "pod_signed_by": "R1C Receiver",
						"signature_data_url": "data:image/png;base64,AAAA",
					},
				},
				{name: "delivery.delivery.status.missing_pod", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery2}/transitions",
					body: map[string]any{"to": "delivered", "revision": 1}},
				{name: "delivery.delivery.status.invalid", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery2}/transitions",
					body: map[string]any{"to": "teleported", "revision": 1}},
				{name: "delivery.delivery.status.bad_id", method: "POST", path: "/api/v1/delivery/deliveries/not-a-uuid/transitions",
					body: map[string]any{"to": "failed"}},
				{name: "delivery.delivery.status.bad_body", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery2}/transitions", body: "not an object"},
				{name: "delivery.delivery.status.not_found", method: "POST", path: "/api/v1/delivery/deliveries/" + cMissingID + "/transitions",
					body: map[string]any{"to": "failed"}},
				{name: "delivery.route.complete.one_pending", method: "POST", path: "/api/v1/delivery/routes/{c_route}/transitions",
					headers: map[string]string{"If-Match": `"6"`},
					body:    map[string]any{"to": "completed"}},
				{name: "delivery.delivery.status.failed", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery2}/transitions", maskMockGeo: true,
					body: map[string]any{"to": "failed", "revision": 1}},
				{name: "delivery.delivery.get.after_status", method: "GET", path: "/api/v1/delivery/deliveries/{c_delivery}", maskMockGeo: true},
				{name: "delivery.route.complete", method: "POST", path: "/api/v1/delivery/routes/{c_route}/transitions", maskMockGeo: true,
					headers: map[string]string{"If-Match": `"6"`},
					body:    map[string]any{"to": "completed"}},
				{name: "delivery.route.list_by_date.after", method: "GET", path: "/api/v1/delivery/routes?date={today+2}"},
				{name: "delivery.route.dispatch.completed", method: "POST", path: "/api/v1/delivery/routes/{c_route}/transitions",
					headers: map[string]string{"If-Match": `"6"`},
					body:    map[string]any{"to": "in_transit"}},
				{name: "delivery.route.dispatch.empty", method: "POST", path: "/api/v1/delivery/routes/{c_route_empty}/transitions", maskMockGeo: true,
					headers: map[string]string{"If-Match": `"1"`},
					body:    map[string]any{"to": "in_transit"}},
				// The empty route is in_transit and holds no stops; the
				// in_transit completion path keeps the route_empty guard
				// (PR 80 review round 1 P2-1: the guard was reachable but
				// pinned by nothing before this step).
				{name: "delivery.route.complete.empty_in_transit", method: "POST", path: "/api/v1/delivery/routes/{c_route_empty}/transitions",
					headers: map[string]string{"If-Match": `"2"`},
					body:    map[string]any{"to": "completed"}},
			},
		},
		// A second completion on an already-completed route: 409
		// invalid_state_transition (PR 80 review round 1 P2-2: the
		// to=completed entry claimed a delivery.route.complete.completed
		// golden that did not exist; the lifecycle never actually reaches
		// COMPLETED, so the step lives in a self-contained group that drives
		// its own route through dispatch, deliver and complete).
		{
			name: "delivery_route_complete_replay",
			steps: []stepDef{
				{
					name: "delivery.route.create.completed", method: "POST", path: "/api/v1/delivery/routes",
					body: map[string]any{
						"vehicle_id": "{c_vehicle}", "driver_id": "{c_driver}", "scheduled_date": "{today+2}",
						"notes": "r1b c replay route",
					},
					extract: map[string]string{"c_route_replay": "/id"},
				},
				{
					name: "delivery.delivery.assign.replay", method: "POST", path: "/api/v1/delivery/deliveries", maskMockGeo: true,
					body: map[string]any{
						"route_id": "{c_route_replay}", "order_id": "{myOrder}", "stop_sequence": 1,
						"delivery_instructions": "r1b c replay stop",
					},
					extract: map[string]string{"c_delivery_replay": "/delivery/id"},
				},
				{name: "delivery.route.dispatch.replay", method: "POST", path: "/api/v1/delivery/routes/{c_route_replay}/transitions", maskMockGeo: true,
					headers: map[string]string{"If-Match": `"2"`},
					body:    map[string]any{"to": "in_transit"}},
				{name: "delivery.delivery.status.delivered.replay", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery_replay}/transitions", maskMockGeo: true,
					body: map[string]any{
						"to": "delivered", "revision": 1, "pod_proof_url": "/uploads/pod/golden.png", "pod_signed_by": "R1C Receiver",
						"signature_data_url": "data:image/png;base64,AAAA",
					}},
				{name: "delivery.route.complete.replay", method: "POST", path: "/api/v1/delivery/routes/{c_route_replay}/transitions", maskMockGeo: true,
					headers: map[string]string{"If-Match": `"3"`},
					body:    map[string]any{"to": "completed"}},
				{name: "delivery.route.complete.completed", method: "POST", path: "/api/v1/delivery/routes/{c_route_replay}/transitions",
					headers: map[string]string{"If-Match": `"4"`},
					body:    map[string]any{"to": "completed"}},
			},
		},
		{
			name: "purchase_order_list",
			steps: []stepDef{
				{name: "purchase_order.list", method: "GET", path: "/api/v1/purchase-orders", sortPrimaryArray: true, maskFields: vendorMask},
			},
		},
	}
}

func r1bCPOSGroups() []groupDef {
	return []groupDef{
		{
			name: "pos_transactions",
			steps: []stepDef{
				// pos_registers holds only REG-01 (migration 027) and the API cannot
				// add one, so these groups share REG-01 with the pos group: the till
				// it opened is read here, closed in pos_till_close and reopened after,
				// so the register ends the run with an open till as it began.
				{
					name: "pos.till.current.c", method: "GET", path: "/api/v1/pos/till/current?register_id=REG-01",
					extract: map[string]string{"c_till": "/session/id"},
				},
				// Sales deduct stock at the unassigned location, so seed some there.
				{name: "pos.setup.stock.product", method: "POST", path: "/api/v1/inventory/adjust",
					body: map[string]any{"product_id": "{product}", "quantity": 50, "reason": "r1b c pos setup", "is_delta": true}},
				{name: "pos.setup.stock.sheet", method: "POST", path: "/api/v1/inventory/adjust",
					body: map[string]any{"product_id": "{productSheet}", "quantity": 10, "reason": "r1b c pos setup", "is_delta": true}},
				{name: "pos.till.open.c_duplicate", method: "POST", path: "/api/v1/pos/till/open",
					body: map[string]any{"register_id": "REG-01", "opening_float": 50.0}},
				{name: "pos.till.open.bad_body", method: "POST", path: "/api/v1/pos/till/open", body: "not an object"},
				{
					name: "pos.transaction.start.c", method: "POST", path: "/api/v1/pos/transactions",
					body:    map[string]any{"register_id": "REG-01", "customer_id": "{myCustomer}"},
					extract: map[string]string{"c_tx": "/id"},
				},
				{name: "pos.transaction.start.c_void", method: "POST", path: "/api/v1/pos/transactions",
					body:    map[string]any{"register_id": "REG-01"},
					extract: map[string]string{"c_tx_void": "/id"}},
				{name: "pos.transaction.start.c_voidpaid", method: "POST", path: "/api/v1/pos/transactions",
					body:    map[string]any{"register_id": "REG-01"},
					extract: map[string]string{"c_tx_voidpaid": "/id"}},
				{name: "pos.transaction.start.bad_body", method: "POST", path: "/api/v1/pos/transactions", body: "not an object"},
				{name: "pos.products.search", method: "GET", path: "/api/v1/pos/products/search?q=LUM"},
				{name: "pos.products.search.no_match", method: "GET", path: "/api/v1/pos/products/search?q=zzzzzzzz-no-such-sku"},
				{name: "pos.products.search.empty_query", method: "GET", path: "/api/v1/pos/products/search"},
				{
					name: "pos.item.add", method: "POST", path: "/api/v1/pos/transactions/{c_tx}/items",
					body:    map[string]any{"product_id": "{product}", "quantity": 3},
					extract: map[string]string{"c_item": "/line_items/0/id"},
				},
				{
					name: "pos.item.add.second", method: "POST", path: "/api/v1/pos/transactions/{c_tx}/items",
					body:    map[string]any{"product_id": "{productSheet}", "quantity": 2, "uom": "EA"},
					extract: map[string]string{"c_item2": "/line_items/1/id"},
				},
				{name: "pos.item.add.bad_tx_id", method: "POST", path: "/api/v1/pos/transactions/not-a-uuid/items",
					body: map[string]any{"product_id": "{product}", "quantity": 1}},
				{name: "pos.item.add.bad_body", method: "POST", path: "/api/v1/pos/transactions/{c_tx}/items", body: "not an object"},
				{name: "pos.item.add.unknown_product", method: "POST", path: "/api/v1/pos/transactions/{c_tx}/items",
					body: map[string]any{"product_id": cMissingID, "quantity": 1}},
				{name: "pos.item.remove", method: "DELETE", path: "/api/v1/pos/transactions/{c_tx}/items/{c_item2}"},
				{name: "pos.item.remove.bad_tx_id", method: "DELETE", path: "/api/v1/pos/transactions/not-a-uuid/items/{c_item}"},
				{name: "pos.item.remove.bad_item_id", method: "DELETE", path: "/api/v1/pos/transactions/{c_tx}/items/not-a-uuid"},
				{name: "pos.item.remove.unknown_item", method: "DELETE", path: "/api/v1/pos/transactions/{c_tx}/items/" + cMissingID},
				{name: "pos.transaction.get", method: "GET", path: "/api/v1/pos/transactions/{c_tx}"},
				{name: "pos.transaction.get.not_found", method: "GET", path: "/api/v1/pos/transactions/" + cMissingID},
				{name: "pos.transaction.get.bad_id", method: "GET", path: "/api/v1/pos/transactions/not-a-uuid"},
				{name: "pos.transaction.list", method: "GET", path: "/api/v1/pos/transactions?register_id=REG-01", sortPrimaryArray: true},
				{name: "pos.transaction.list.other_day", method: "GET", path: "/api/v1/pos/transactions?register_id=REG-01&date={today-30}"},
				{name: "pos.transaction.complete.no_tenders", method: "POST", path: "/api/v1/pos/transactions/{c_tx}/complete",
					body: map[string]any{"tenders": []map[string]any{}}},
				{name: "pos.transaction.complete.bad_id", method: "POST", path: "/api/v1/pos/transactions/not-a-uuid/complete",
					body: map[string]any{"tenders": []map[string]any{{"method": "CASH", "amount": 1.0}}}},
				{name: "pos.transaction.complete.bad_body", method: "POST", path: "/api/v1/pos/transactions/{c_tx}/complete", body: "not an object"},
				{name: "pos.transaction.complete.insufficient", method: "POST", path: "/api/v1/pos/transactions/{c_tx}/complete",
					body: map[string]any{"tenders": []map[string]any{{"method": "CASH", "amount": 0.01}}}},
				{name: "pos.transaction.complete.not_found", method: "POST", path: "/api/v1/pos/transactions/" + cMissingID + "/complete",
					body: map[string]any{"tenders": []map[string]any{{"method": "CASH", "amount": 1.0}}}},
				{name: "pos.transaction.complete", method: "POST", path: "/api/v1/pos/transactions/{c_tx}/complete",
					body: map[string]any{"tenders": []map[string]any{{"method": "CASH", "amount": 500.0}}}},
				{name: "pos.transaction.complete.again", method: "POST", path: "/api/v1/pos/transactions/{c_tx}/complete",
					body: map[string]any{"tenders": []map[string]any{{"method": "CASH", "amount": 500.0}}}},
				{name: "pos.item.add.after_complete", method: "POST", path: "/api/v1/pos/transactions/{c_tx}/items",
					body: map[string]any{"product_id": "{product}", "quantity": 1}},
				// An open sale with an item, voided before payment.
				{name: "pos.item.add.void_tx", method: "POST", path: "/api/v1/pos/transactions/{c_tx_void}/items",
					body: map[string]any{"product_id": "{productSheet}", "quantity": 1}},
				{name: "pos.transaction.void", method: "POST", path: "/api/v1/pos/transactions/{c_tx_void}/void"},
				{name: "pos.transaction.void.again", method: "POST", path: "/api/v1/pos/transactions/{c_tx_void}/void"},
				{name: "pos.transaction.void.bad_id", method: "POST", path: "/api/v1/pos/transactions/not-a-uuid/void"},
				{name: "pos.transaction.void.not_found", method: "POST", path: "/api/v1/pos/transactions/" + cMissingID + "/void"},
				// A paid sale, voided after payment (restores stock).
				{name: "pos.item.add.voidpaid_tx", method: "POST", path: "/api/v1/pos/transactions/{c_tx_voidpaid}/items",
					body: map[string]any{"product_id": "{product}", "quantity": 1}},
				{name: "pos.transaction.complete.voidpaid", method: "POST", path: "/api/v1/pos/transactions/{c_tx_voidpaid}/complete",
					body: map[string]any{"tenders": []map[string]any{{"method": "CASH", "amount": 100.0}}}},
				{name: "pos.transaction.void.completed", method: "POST", path: "/api/v1/pos/transactions/{c_tx_voidpaid}/void"},
				{name: "pos.transaction.get.after", method: "GET", path: "/api/v1/pos/transactions/{c_tx}"},
				{name: "pos.transaction.list.after", method: "GET", path: "/api/v1/pos/transactions?register_id=REG-01", sortPrimaryArray: true},
			},
		},
		{
			name: "pos_sync",
			steps: []stepDef{
				{
					name: "pos.sync", method: "POST", path: "/api/v1/pos/sync",
					body: map[string]any{
						"batch_id": "r1c-batch-1", "register_id": "REG-01",
						"items": []map[string]any{{
							"client_id":         "c1c1c1c1-0000-4000-8000-000000000001",
							"register_id":       "REG-01",
							"cashier_id":        "c1c1c1c1-0000-4000-8000-0000000000aa",
							"items":             []map[string]any{{"product_id": "{product}", "quantity": 1}},
							"tenders":           []map[string]any{{"method": "CASH", "amount": 200.0}},
							"client_created_at": "{today}T03:04:05Z",
						}},
					},
				},
				{
					name: "pos.sync.replay_duplicate", method: "POST", path: "/api/v1/pos/sync",
					body: map[string]any{
						"batch_id": "r1c-batch-1", "register_id": "REG-01",
						"items": []map[string]any{{
							"client_id":         "c1c1c1c1-0000-4000-8000-000000000001",
							"register_id":       "REG-01",
							"cashier_id":        "c1c1c1c1-0000-4000-8000-0000000000aa",
							"items":             []map[string]any{{"product_id": "{product}", "quantity": 1}},
							"tenders":           []map[string]any{{"method": "CASH", "amount": 200.0}},
							"client_created_at": "{today}T03:04:05Z",
						}},
					},
				},
				{
					name: "pos.sync.underpaid", method: "POST", path: "/api/v1/pos/sync",
					body: map[string]any{
						"batch_id": "r1c-batch-2", "register_id": "REG-01",
						"items": []map[string]any{{
							"client_id":         "c1c1c1c1-0000-4000-8000-000000000002",
							"cashier_id":        "c1c1c1c1-0000-4000-8000-0000000000aa",
							"items":             []map[string]any{{"product_id": "{product}", "quantity": 1}},
							"tenders":           []map[string]any{{"method": "CASH", "amount": 0.01}},
							"client_created_at": "{today}T03:04:06Z",
						}},
					},
				},
				{name: "pos.sync.no_batch_id", method: "POST", path: "/api/v1/pos/sync",
					body: map[string]any{"register_id": "REG-01", "items": []map[string]any{{"client_id": cMissingID}}}},
				{name: "pos.sync.no_items", method: "POST", path: "/api/v1/pos/sync",
					body: map[string]any{"batch_id": "r1c-batch-3", "items": []map[string]any{}}},
				{name: "pos.sync.bad_body", method: "POST", path: "/api/v1/pos/sync", body: "not an object"},
				{name: "pos.transaction.list.after_sync", method: "GET", path: "/api/v1/pos/transactions?register_id=REG-01", sortPrimaryArray: true},
			},
		},
		{
			name: "pos_returns",
			steps: []stepDef{
				{
					name: "pos.return.create", method: "POST", path: "/api/v1/pos/returns",
					body: map[string]any{
						"register_id": "REG-01", "refund_method": "CASH", "reason": "r1b c wrong size",
						"lines": []map[string]any{
							{"product_id": "{product}", "description": "returned lumber", "quantity": 2, "unit_price": 12.5},
						},
					},
					extract: map[string]string{"c_return": "/id"},
				},
				{
					name: "pos.return.create.account", method: "POST", path: "/api/v1/pos/returns",
					body: map[string]any{
						"register_id": "REG-01", "refund_method": "ACCOUNT", "customer_id": "{myCustomer}",
						"lines": []map[string]any{
							{"product_id": "{productSheet}", "quantity": 1, "unit_price": 30.0, "restock": false},
						},
					},
					extract: map[string]string{"c_return2": "/id"},
				},
				{name: "pos.return.create.account_no_customer", method: "POST", path: "/api/v1/pos/returns",
					body: map[string]any{"register_id": "REG-01", "refund_method": "ACCOUNT",
						"lines": []map[string]any{{"product_id": "{product}", "quantity": 1, "unit_price": 5.0}}}},
				{name: "pos.return.create.no_lines", method: "POST", path: "/api/v1/pos/returns",
					body: map[string]any{"register_id": "REG-01", "refund_method": "CASH", "lines": []map[string]any{}}},
				{name: "pos.return.create.bad_method", method: "POST", path: "/api/v1/pos/returns",
					body: map[string]any{"register_id": "REG-01", "refund_method": "BARTER",
						"lines": []map[string]any{{"product_id": "{product}", "quantity": 1, "unit_price": 5.0}}}},
				{name: "pos.return.create.zero_quantity", method: "POST", path: "/api/v1/pos/returns",
					body: map[string]any{"register_id": "REG-01", "refund_method": "CASH",
						"lines": []map[string]any{{"product_id": "{product}", "quantity": 0, "unit_price": 5.0}}}},
				{name: "pos.return.create.bad_body", method: "POST", path: "/api/v1/pos/returns", body: "not an object"},
				{name: "pos.return.get", method: "GET", path: "/api/v1/pos/returns/{c_return}"},
				{name: "pos.return.get.not_found", method: "GET", path: "/api/v1/pos/returns/" + cMissingID},
				{name: "pos.return.get.bad_id", method: "GET", path: "/api/v1/pos/returns/not-a-uuid"},
				{name: "pos.return.list", method: "GET", path: "/api/v1/pos/returns?register_id=REG-01", sortPrimaryArray: true},
				{name: "pos.return.list.other_day", method: "GET", path: "/api/v1/pos/returns?register_id=REG-01&date={today-30}"},
				{name: "pos.return.list.unknown_register", method: "GET", path: "/api/v1/pos/returns?register_id=REG-NONE"},
			},
		},
		{
			name: "pos_till_close",
			steps: []stepDef{
				{name: "pos.till.report", method: "GET", path: "/api/v1/pos/till/{c_till}/report"},
				{name: "pos.till.report.bad_id", method: "GET", path: "/api/v1/pos/till/not-a-uuid/report"},
				{name: "pos.till.report.not_found", method: "GET", path: "/api/v1/pos/till/" + cMissingID + "/report"},
				{name: "pos.till.zreport.before_close", method: "GET", path: "/api/v1/pos/till/{c_till}/zreport"},
				{name: "pos.till.zreport.bad_id", method: "GET", path: "/api/v1/pos/till/not-a-uuid/zreport"},
				{name: "pos.till.close.bad_id", method: "POST", path: "/api/v1/pos/till/not-a-uuid/close",
					body: map[string]any{"counted_by_method": map[string]any{"CASH": 1.0}}},
				{name: "pos.till.close.bad_body", method: "POST", path: "/api/v1/pos/till/{c_till}/close", body: "not an object"},
				{name: "pos.till.close.not_found", method: "POST", path: "/api/v1/pos/till/" + cMissingID + "/close",
					body: map[string]any{"counted_by_method": map[string]any{"CASH": 1.0}}},
				{
					name: "pos.till.close", method: "POST", path: "/api/v1/pos/till/{c_till}/close",
					body: map[string]any{"counted_by_method": map[string]any{"CASH": 200.0}, "notes": "r1b c close count"},
				},
				{name: "pos.till.close.again", method: "POST", path: "/api/v1/pos/till/{c_till}/close",
					body: map[string]any{"counted_by_method": map[string]any{"CASH": 200.0}}},
				{name: "pos.till.current.after_close", method: "GET", path: "/api/v1/pos/till/current?register_id=REG-01"},
				{
					name: "pos.till.reopen", method: "POST", path: "/api/v1/pos/till/open",
					body:    map[string]any{"register_id": "REG-01", "opening_float": 100.0},
					extract: map[string]string{"c_till_reopened": "/id"},
				},
				{name: "pos.till.report.after_close", method: "GET", path: "/api/v1/pos/till/{c_till}/report"},
				{name: "pos.till.zreport", method: "GET", path: "/api/v1/pos/till/{c_till}/zreport"},
				{name: "pos.till.zreport.not_found", method: "GET", path: "/api/v1/pos/till/" + cMissingID + "/zreport"},
				{name: "pos.zreports.list", method: "GET", path: "/api/v1/pos/zreports?register_id=REG-01"},
				{name: "pos.zreports.list.other_day", method: "GET", path: "/api/v1/pos/zreports?register_id=REG-01&date={today-30}"},
				{name: "pos.zreports.list.unknown_register", method: "GET", path: "/api/v1/pos/zreports?register_id=REG-NONE"},
			},
		},
	}
}

func r1bCPurchaseOrderGroups() []groupDef {
	return []groupDef{{
		name: "purchase_order_flow",
		steps: []stepDef{
			{name: "purchase_order.recommendations", method: "GET", path: "/api/v1/purchase-orders/recommendations"},
			{name: "purchase_order.source_summary", method: "GET", path: "/api/v1/purchase-orders/source-summary"},
			{name: "purchase_order.reorder_runs.before", method: "GET", path: "/api/v1/purchase-orders/reorder-runs"},
			{name: "purchase_order.refresh_targets.default_dry_run", method: "POST", path: "/api/v1/purchase-orders/refresh-reorder-targets"},
			{name: "purchase_order.refresh_targets.dry_run_lookback", method: "POST", path: "/api/v1/purchase-orders/refresh-reorder-targets",
				body: map[string]any{"dry_run": true, "lookback_days": 30}},
			{name: "purchase_order.refresh_targets.bad_body", method: "POST", path: "/api/v1/purchase-orders/refresh-reorder-targets", body: "not an object"},
			{name: "purchase_order.reorder_runs.after_refresh", method: "GET", path: "/api/v1/purchase-orders/reorder-runs"},
			{
				name: "purchase_order.create.c", method: "POST", path: "/api/v1/purchase-orders",
				body: map[string]any{
					"vendor_id": "{vendor}",
					"lines": []map[string]any{
						{"product_id": "{productSheet}", "description": "r1b c sheet goods", "quantity": 10, "cost": 20.0},
						{"product_id": "{product}", "description": "r1b c lumber", "quantity": 5, "cost": 8.0},
					},
				},
				extract: map[string]string{"c_po": "/id", "c_po_line": "/lines/0/id"},
			},
			{
				name: "purchase_order.create.c_draft", method: "POST", path: "/api/v1/purchase-orders",
				body: map[string]any{
					"vendor_id": "{vendor}",
					"lines": []map[string]any{
						{"product_id": "{product}", "description": "r1b c draft only", "quantity": 1, "cost": 9.0},
					},
				},
				extract: map[string]string{"c_po_draft": "/id", "c_po_draft_line": "/lines/0/id"},
			},
			{name: "purchase_order.get.c", method: "GET", path: "/api/v1/purchase-orders/{c_po}"},
			{name: "purchase_order.get.not_found", method: "GET", path: "/api/v1/purchase-orders/" + cMissingID},
			{name: "purchase_order.get.bad_id", method: "GET", path: "/api/v1/purchase-orders/not-a-uuid"},
			{name: "purchase_order.freight.list.empty", method: "GET", path: "/api/v1/purchase-orders/{c_po}/freight"},
			{name: "purchase_order.freight.list.bad_id", method: "GET", path: "/api/v1/purchase-orders/not-a-uuid/freight"},
			{name: "purchase_order.receive.draft", method: "POST", path: "/api/v1/purchase-orders/{c_po_draft}/receive",
				body: map[string]any{"lines": []map[string]any{{"line_id": "{c_po_draft_line}", "qty_received": 1, "location_id": "{myLocation}"}}}},
			{name: "purchase_order.submit.bad_id", method: "POST", path: "/api/v1/purchase-orders/not-a-uuid/submit"},
			{name: "purchase_order.submit.not_found", method: "POST", path: "/api/v1/purchase-orders/" + cMissingID + "/submit"},
			{name: "purchase_order.submit", method: "POST", path: "/api/v1/purchase-orders/{c_po}/submit"},
			{name: "purchase_order.receive.bad_id", method: "POST", path: "/api/v1/purchase-orders/not-a-uuid/receive",
				body: map[string]any{"lines": []map[string]any{}}},
			{name: "purchase_order.receive.bad_body", method: "POST", path: "/api/v1/purchase-orders/{c_po}/receive", body: "not an object"},
			{name: "purchase_order.receive.not_found", method: "POST", path: "/api/v1/purchase-orders/" + cMissingID + "/receive",
				body: map[string]any{"lines": []map[string]any{}}},
			{name: "purchase_order.receive.bad_line_id", method: "POST", path: "/api/v1/purchase-orders/{c_po}/receive",
				body: map[string]any{"lines": []map[string]any{{"line_id": "nope", "qty_received": 1, "location_id": "{myLocation}"}}}},
			{name: "purchase_order.receive.unknown_line", method: "POST", path: "/api/v1/purchase-orders/{c_po}/receive",
				body: map[string]any{"lines": []map[string]any{{"line_id": cMissingID, "qty_received": 1, "location_id": "{myLocation}"}}}},
			{name: "purchase_order.freight.upload.before_receive", method: "POST", path: "/api/v1/purchase-orders/{c_po}/freight",
				body: &multipartDef{FieldName: "file", Filename: "freight.txt", Content: "Carrier ACME Freight invoice F-1 total 120.00"}},
			{name: "purchase_order.receive.partial", method: "POST", path: "/api/v1/purchase-orders/{c_po}/receive",
				body: map[string]any{"lines": []map[string]any{{"line_id": "{c_po_line}", "qty_received": 4, "location_id": "{myLocation}"}}}},
			{name: "purchase_order.get.after_partial", method: "GET", path: "/api/v1/purchase-orders/{c_po}"},
			{name: "purchase_order.freight.upload", method: "POST", path: "/api/v1/purchase-orders/{c_po}/freight",
				body: &multipartDef{FieldName: "file", Filename: "freight.txt", Content: "Carrier ACME Freight invoice F-1 total 120.00"}},
			{name: "purchase_order.freight.upload.wrong_field", method: "POST", path: "/api/v1/purchase-orders/{c_po}/freight",
				body: &multipartDef{FieldName: "invoice", Filename: "freight.txt", Content: "x"}},
			{name: "purchase_order.freight.upload.bad_id", method: "POST", path: "/api/v1/purchase-orders/not-a-uuid/freight",
				body: &multipartDef{FieldName: "file", Filename: "freight.txt", Content: "x"}},
			{name: "purchase_order.freight.upload.not_found", method: "POST", path: "/api/v1/purchase-orders/" + cMissingID + "/freight",
				body: &multipartDef{FieldName: "file", Filename: "freight.txt", Content: "x"}},
			{name: "purchase_order.freight.list", method: "GET", path: "/api/v1/purchase-orders/{c_po}/freight"},
			{name: "purchase_order.freight.apply.unknown_charge", method: "POST", path: "/api/v1/purchase-orders/{c_po}/freight/" + cMissingID + "/apply"},
			{name: "purchase_order.freight.apply.bad_po_id", method: "POST", path: "/api/v1/purchase-orders/not-a-uuid/freight/" + cMissingID + "/apply"},
			{name: "purchase_order.freight.apply.bad_charge_id", method: "POST", path: "/api/v1/purchase-orders/{c_po}/freight/not-a-uuid/apply"},
			{name: "purchase_order.receive.rest", method: "POST", path: "/api/v1/purchase-orders/{c_po}/receive",
				body: map[string]any{"lines": []map[string]any{{"line_id": "{c_po_line}", "qty_received": 6, "location_id": "{myLocation}"}}}},
			{name: "purchase_order.get.after_receive", method: "GET", path: "/api/v1/purchase-orders/{c_po}"},
			{name: "purchase_order.receive.already_received", method: "POST", path: "/api/v1/purchase-orders/{c_po}/receive",
				body: map[string]any{"lines": []map[string]any{{"line_id": "{c_po_line}", "qty_received": 1, "location_id": "{myLocation}"}}}},
			{name: "purchase_order.source_summary.after", method: "GET", path: "/api/v1/purchase-orders/source-summary"},
			// Last: the reorder check may raise purchase orders of its own.
			{name: "purchase_order.reorder_check", method: "POST", path: "/api/v1/purchase-orders/reorder-check"},
			{name: "purchase_order.reorder_runs.after_check", method: "GET", path: "/api/v1/purchase-orders/reorder-runs"},
			{name: "purchase_order.source_summary.after_check", method: "GET", path: "/api/v1/purchase-orders/source-summary"},
		},
	}}
}

// r1bCDeliveredGroups runs last of every group (see allGroups): completing a
// delivery as DELIVERED creates an invoice, and an invoice for any customer
// moves the statements, aging and ledger reads that groups after it record,
// so nothing may run after it.
func r1bCDeliveredGroups() []groupDef {
	return []groupDef{
		{
			// DELIVERED is the one terminal state that bills: completing a
			// delivery with proof creates the order's invoice. It runs on an
			// order, route and delivery of its own so the shared order's
			// invoice (made at fulfilment) is not touched. The refused
			// attempts first pin the POD check; the success then pins the
			// status constant, the delivery read back and the invoice made.
			name: "delivery_delivered",
			steps: []stepDef{
				{
					name: "order.create.c_delivered", method: "POST", path: "/api/v1/orders",
					body: map[string]any{
						"customer_id":   "{myCustomer}",
						"delivery_type": "delivery",
						"lines":         []map[string]any{{"product_id": "{product}", "quantity": "2"}},
					},
					extract: map[string]string{"c_order_dl": "/id"},
				},
				{
					name: "delivery.route.create.c_delivered", method: "POST", path: "/api/v1/delivery/routes",
					body: map[string]any{
						"vehicle_id": "{c_vehicle}", "driver_id": "{c_driver}", "scheduled_date": "{today+4}",
						"notes": "r1b c delivered route",
					},
					extract: map[string]string{"c_route_dl": "/id"},
				},
				{
					name: "delivery.delivery.assign.c_delivered", method: "POST", path: "/api/v1/delivery/deliveries", maskMockGeo: true,
					body: map[string]any{
						"route_id": "{c_route_dl}", "order_id": "{c_order_dl}", "stop_sequence": 1,
					},
					extract: map[string]string{"c_delivery_dl": "/delivery/id"},
				},
				{name: "invoice.for_order.before", sql: `SELECT count(*) AS invoices FROM invoices WHERE order_id = '{c_order_dl}'::uuid`},
				{name: "delivery.delivery.status.delivered_missing_pod", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery_dl}/transitions",
					body: map[string]any{"to": "delivered", "revision": 1}},
				{name: "invoice.for_order.after_refusal", sql: `SELECT count(*) AS invoices FROM invoices WHERE order_id = '{c_order_dl}'::uuid`},
				{name: "fulfilment.request.after_refusal", sql: `SELECT count(*) AS requests FROM order_fulfillment_requests WHERE order_id = '{c_order_dl}'::uuid`},
				{
					name: "delivery.delivery.status.delivered", method: "POST", path: "/api/v1/delivery/deliveries/{c_delivery_dl}/transitions", maskMockGeo: true,
					body: map[string]any{
						"to": "delivered", "revision": 1, "pod_proof_url": "/uploads/pod/golden-delivered.png", "pod_signed_by": "R1C Receiver",
						"signature_data_url": "data:image/png;base64,AAAA",
					},
				},
				{name: "fulfilment.request.after_delivered", sql: `SELECT attempts, COALESCE(last_error, '') AS last_error, (parked_at IS NOT NULL) AS parked FROM order_fulfillment_requests WHERE order_id = '{c_order_dl}'::uuid`},
				{name: "delivery.delivery.get.after_delivered", method: "GET", path: "/api/v1/delivery/deliveries/{c_delivery_dl}", maskMockGeo: true},
				{
					name: "invoice.for_order.after_delivered",
					sql: `SELECT status, total_amount::text AS total_amount, tax_amount::text AS tax_amount,
					             (SELECT count(*) FROM invoice_lines l WHERE l.invoice_id = i.id) AS lines
					      FROM invoices i WHERE order_id = '{c_order_dl}'::uuid ORDER BY created_at, id`,
				},
			},
		},
	}
}
