// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import (
	"database/sql"
	"testing"
)

// R1-1b platform groups: the cross-cutting wiring the server mounts around
// every module, pinned through the real binary. They run after the clock
// group, so their writes cannot move any earlier golden.
//
//   - machine_key: a scoped key (quotes:read) reads a quote and is refused a
//     write, and the refusal leaves its audit row. This pins the server's
//     machine key wiring (AUTH_MODE=dev mounts the machine-key core
//     standalone), not only the middleware package's own tests.
//   - events: the events feed after a real quote exposure acknowledgement.
//   - idempotency: the replay of one POST carrying an Idempotency-Key.

var eventsMask = map[string]any{"quote_short_id": "<masked>", "salesperson_name": "<masked>"}

func r1bPlatformGroups() []groupDef {
	bearer := func() map[string]string { return map[string]string{"Authorization": "Bearer {machineKey}"} }

	return []groupDef{{
		name: "machine_key",
		steps: []stepDef{
			{
				name:   "machine_key.create",
				method: "POST",
				path:   "/api/v1/admin/keys",
				body:   map[string]any{"name": "golden-scoped-key", "scopes": []any{"quotes:read"}},
				extract: map[string]string{
					"machineKey":   "/api_key",
					"machineKeyID": "/key/id",
				},
			},
			// quotes:read admits GET on the quotes module.
			{name: "machine_key.read_quote", method: "GET", path: "/api/v1/quotes/{myQuote}", headers: bearer()},
			// A write needs quotes:write: refused with the ADR envelope, and
			// the refusal is audited against the key's id.
			{
				name:    "machine_key.write_quote_refused",
				method:  "POST",
				path:    "/api/v1/quotes/{myQuote}/transitions",
				headers: bearer(),
				body:    map[string]any{"to": "draft"},
			},
			// The audit row of that refusal. No route reads audit_log (the
			// users listing unions only user attributed rows, and a key is
			// never a user), so the step probes the table itself: the row
			// names the key as actor and carries the scope it lacked.
			{
				name: "machine_key.refusal_audit_row",
				sql: `SELECT action, entity_type, entity_id::text AS entity_id, actor_kind,
				             actor_id, user_id, changes
				      FROM audit_log
				      WHERE entity_type = 'api_key' AND entity_id = '{machineKeyID}'::uuid
				      ORDER BY created_at, id`,
			},
			// The units module of C3-2A-units joins the same vocabulary
			// (ADR 0006 section 2.3): units:read reads the catalogue, a
			// write needs units:write, and the refusal is audited the same
			// way.
			{
				name:   "machine_key.units_key",
				method: "POST",
				path:   "/api/v1/admin/keys",
				body:   map[string]any{"name": "golden-units-key", "scopes": []any{"units:read"}},
				extract: map[string]string{
					"unitsKey":   "/api_key",
					"unitsKeyID": "/key/id",
				},
			},
			{name: "machine_key.units_read",
				method:  "GET",
				path:    "/api/v1/units/EA",
				headers: map[string]string{"Authorization": "Bearer {unitsKey}"},
			},
			{
				name:    "machine_key.units_write_refused",
				method:  "POST",
				path:    "/api/v1/units",
				headers: map[string]string{"Authorization": "Bearer {unitsKey}"},
				body:    map[string]any{"code": "SKID", "name": "Skid", "dimension": "count"},
			},
			{
				name: "machine_key.units_refusal_audit_row",
				sql: `SELECT action, entity_type, entity_id::text AS entity_id, actor_kind,
				             actor_id, user_id, changes
				      FROM audit_log
				      WHERE entity_type = 'api_key' AND entity_id = '{unitsKeyID}'::uuid
				      ORDER BY created_at, id`,
			},
			// Key management is user-only whatever the scopes.
			{name: "machine_key.user_only_refused", method: "GET", path: "/api/v1/admin/keys", headers: bearer()},
			// The finer admin scopes (ADR 0009): a settings key reaches the
			// settings area, and the coarse admin scopes no longer do.
			{
				name:   "machine_key.finer_settings_key.create",
				method: "POST",
				path:   "/api/v1/admin/keys",
				body:   map[string]any{"name": "golden-settings-key", "scopes": []any{"admin:settings"}},
				extract: map[string]string{
					"settingsKey": "/api_key",
				},
			},
			{name: "machine_key.finer_settings_key.reads_settings", method: "GET", path: "/api/v1/admin/settings/ai",
				headers: map[string]string{"Authorization": "Bearer {settingsKey}"}},
			{name: "machine_key.finer_settings_key_refused_on_staff", method: "GET", path: "/api/v1/admin/staff",
				headers: map[string]string{"Authorization": "Bearer {settingsKey}"}},
			{
				name:   "machine_key.coarse_admin_key.create",
				method: "POST",
				path:   "/api/v1/admin/keys",
				body:   map[string]any{"name": "golden-coarse-key", "scopes": []any{"admin:read"}},
				extract: map[string]string{
					"coarseKey":   "/api_key",
					"coarseKeyID": "/key/id",
				},
			},
			{name: "machine_key.coarse_admin_key_refused_on_settings", method: "GET", path: "/api/v1/admin/settings/ai",
				headers: map[string]string{"Authorization": "Bearer {coarseKey}"}},
			// The refusal's audit row names the finer scope it lacked.
			{
				name: "machine_key.coarse_admin_key.refusal_audit_row",
				sql: `SELECT action, changes->>'scope' AS refused_scope
				      FROM audit_log
				      WHERE entity_type = 'api_key' AND entity_id = '{coarseKeyID}'::uuid
				      ORDER BY created_at, id`,
			},
			// A key-shaped token that is not a key.
			{
				name:    "machine_key.unknown_key",
				method:  "GET",
				path:    "/api/v1/quotes/{myQuote}",
				headers: map[string]string{"Authorization": "Bearer sk" + "_live_notarealkeynotarealkeynotarealkey00"},
			},
		},
	}, {
		name: "events",
		steps: []stepDef{
			// The feed before the fixture quote: the exposure scans of the
			// earlier groups wrote outbox rows. The first page is small so
			// the golden stays short; the types filter, the empty page, the
			// unknown parameter and the broken cursor pin the feed's strict
			// query posture and its always-present next_cursor. Two values in
			// an exposure payload vary per run (the first characters of a
			// random quote uuid, a name drawn at seed time) and are masked;
			// customer_name is a constant and stays pinned.
			{name: "events.list", method: "GET", path: "/api/v1/events?limit=2", maskFields: eventsMask},
			{name: "events.list.types", method: "GET", path: "/api/v1/events?limit=2&types=quote.exposure.ack_required", maskFields: eventsMask},
			{name: "events.list.empty", method: "GET", path: "/api/v1/events?types=nothing.matches.this"},
			{name: "events.list.bad_param", method: "GET", path: "/api/v1/events?status=sent"},
			{name: "events.list.bad_cursor", method: "GET", path: "/api/v1/events?cursor=not-a-cursor"},
			// A machine key holding no events scope: the auth layer refuses it
			// with the wire envelope (the 403 every route shares).
			{
				name:    "events.key.create",
				method:  "POST",
				path:    "/api/v1/admin/keys",
				body:    map[string]any{"name": "golden-events-denied", "scopes": []any{"quotes:read"}},
				extract: map[string]string{"eventsKey": "/api_key"},
			},
			{
				name:    "events.list.forbidden",
				method:  "GET",
				path:    "/api/v1/events",
				headers: map[string]string{"Authorization": "Bearer {eventsKey}"},
			},
			{
				name:   "events.fixture_quote_exposure",
				method: "GET",
				path:   "/api/v1/quotes/{eventsQuote}/exposure",
				setup:  seedAckRequiredQuote,
			},
			{
				name:   "events.acknowledge",
				method: "POST",
				path:   "/api/v1/quotes/{eventsQuote}/exposure/acknowledge",
				body:   map[string]any{"method": "EMAIL", "notes": "customer confirmed over email"},
			},
			// The acknowledgement committed its outbox event with the ledger
			// row; the feed serves it. Filtered to the one type so the
			// answer does not depend on unrelated events' timing.
			{name: "events.list_acknowledged", method: "GET", path: "/api/v1/events?types=quote.exposure.acknowledged"},
			{name: "events.list_unknown_type", method: "GET", path: "/api/v1/events?types=Not.A.Type"},
			{name: "events.list_unknown_param", method: "GET", path: "/api/v1/events?bogus=1"},
		},
	}, {
		name: "idempotency",
		steps: []stepDef{
			{
				name:           "idempotency.first",
				method:         "POST",
				path:           "/api/v1/vendors",
				headers:        map[string]string{"Idempotency-Key": "golden-idem-key-1"},
				body:           map[string]any{"name": "Golden Idempotent Vendor", "payment_terms": "NET15"},
				captureHeaders: []string{"Idempotency-Replayed"},
			},
			// Same key and body: the stored answer is replayed (same id, the
			// replay header set) and no second vendor is created.
			{
				name:           "idempotency.replay",
				method:         "POST",
				path:           "/api/v1/vendors",
				headers:        map[string]string{"Idempotency-Key": "golden-idem-key-1"},
				body:           map[string]any{"name": "Golden Idempotent Vendor", "payment_terms": "NET15"},
				captureHeaders: []string{"Idempotency-Replayed"},
			},
			// Same key, different body: refused.
			{
				name:           "idempotency.key_reused",
				method:         "POST",
				path:           "/api/v1/vendors",
				headers:        map[string]string{"Idempotency-Key": "golden-idem-key-1"},
				body:           map[string]any{"name": "Golden Other Vendor", "payment_terms": "NET30"},
				captureHeaders: []string{"Idempotency-Replayed"},
			},
			{name: "idempotency.vendor_count", method: "GET", path: "/api/v1/vendors?limit=1"},
			// The middleware's own answers on a route that is not a vendor
			// write: a stored 200 under a key, the same key reused with
			// another body (422), and a malformed key.
			{
				name: "idempotency.scan_first", method: "POST", path: "/api/v1/vision/scan",
				body:    map[string]any{"blueprint_text": "Wall: 2x4 studs at 16in OC"},
				headers: map[string]string{"Idempotency-Key": "golden-idempotency-key-1"},
			},
			{
				name: "idempotency.scan_key_reused", method: "POST", path: "/api/v1/vision/scan",
				body:    map[string]any{"blueprint_text": "Wall: 2x6 studs at 24in OC"},
				headers: map[string]string{"Idempotency-Key": "golden-idempotency-key-1"},
			},
			{
				name: "idempotency.key_malformed", method: "POST", path: "/api/v1/vision/scan",
				body:    map[string]any{"blueprint_text": ""},
				headers: map[string]string{"Idempotency-Key": "bad key with\ttab"},
			},
		},
	}}
}

// seedAckRequiredQuote inserts, through the harness's own SQL, the one state
// the API cannot create: a SENT quote whose exposure rollup is ACK_REQUIRED,
// with one line and an active escalator in the same state. The rows mirror
// the pricing package's own acknowledgement fixture (same columns). It runs
// after every earlier group, so it moves none of their goldens.
func seedAckRequiredQuote(t *testing.T, h *harness) {
	t.Helper()
	db, err := sql.Open("pgx", h.dbURL)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()

	var quoteID, lineID, escalatorID string
	if err := db.QueryRow(`SELECT gen_random_uuid()::text, gen_random_uuid()::text, gen_random_uuid()::text`).
		Scan(&quoteID, &lineID, &escalatorID); err != nil {
		t.Fatalf("fixture ids: %v", err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("fixture %s: %v", query, err)
		}
	}
	exec(`INSERT INTO quotes (id, customer_id, project_id, state, total_amount, source,
	                          customer_notes, branch_id, exposure_state, created_at, updated_at)
	      VALUES ($1, $2, NULL, 'SENT', 500.00, 'manual', 'events fixture', $3,
	              'ACK_REQUIRED', NOW(), NOW())`, quoteID, h.vars["customer"], h.vars["branch"])
	exec(`INSERT INTO quote_lines (id, quote_id, product_id, sku, description, customer_note,
	                              quantity, uom, unit_price, line_total)
	      VALUES ($1, $2, $3, 'EVT', 'events fixture line', 'none', 5, 'EA', 100.00, 500.00)`,
		lineID, quoteID, h.vars["product"])
	exec(`INSERT INTO price_escalators (id, quote_line_id, escalation_type, escalation_rate,
	                                   base_price, effective_date, expiration_date, current_state, is_active)
	      VALUES ($1, $2, 'INDEX_DELTA', 0, 100.00, CURRENT_DATE, CURRENT_DATE + 365,
	              'ACK_REQUIRED', TRUE)`, escalatorID, lineID)
	h.vars["eventsQuote"] = quoteID
}
