-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 080_ailm_integration_contract
-- Description: Schema required by the AI_LM (`gable-ai-lm`) integration surface
--              at /api/integration/{products,vehicles,drivers,orders,
--              delivery-routes,validate-staff}.
--
-- This migration is purely additive: every statement is `IF NOT EXISTS` /
-- `ON CONFLICT DO NOTHING`, no column is dropped or retyped, and no existing
-- row is rewritten. The reverse script lives at
-- `migrations/down/080_ailm_integration_contract_down.sql` — it is deliberately
-- NOT a sibling in this directory because `cmd/migrate` applies
-- `migrations/*.sql` unconditionally, so a `*_down.sql` file placed here would
-- be executed as a forward migration and drop the columns it had just added.
--
-- Four things are added:
--   1. products: canonical parametric 3D geometry (the PIM digital twin).
--   2. orders:   scheduled_delivery_date + packing_manifest (dispatch seam).
--   3. staff + module_grants: the roster AI_LM authenticates against.
--   4. system_settings row registering the AI_LM module.

-- ---------------------------------------------------------------------------
-- 1. Product geometry (PIM digital twin)
-- ---------------------------------------------------------------------------
-- AI_LM's Load Builder consumes these via GET /api/integration/products to
-- render every product as a scaled digital twin and to run the 3D packing
-- solver. All five columns are NULLABLE on purpose:
--
--   * length_in/width_in/height_in are surfaced to AI_LM as JSON `*float64`.
--     NULL means "the PIM has no geometry for this SKU yet" and is materially
--     different from a real 0.0 dimension — AI_LM's resolveGeometry() falls
--     back to its own override/default only for NULL. A NOT NULL DEFAULT 0
--     would silently claim every SKU is a zero-volume box.
--   * stackable is surfaced as `*bool`. NULL means "unknown"; AI_LM defaults
--     unknown to true. A NOT NULL DEFAULT TRUE here would assert that a 6x6 PT
--     post is stackable rather than admitting the value was never entered.
--   * geometry_source is a forward-compat seam: a future 'mesh' value plus a
--     mesh_url column can ship without breaking the wire contract. The
--     integration layer reports 'parametric' when dimensions are present and
--     no explicit source was recorded.
--
-- Units are inches; DECIMAL(19,4) matches the precision used elsewhere for
-- dimensional data and avoids binary-float drift in the packing solver.

ALTER TABLE products ADD COLUMN IF NOT EXISTS length_in       DECIMAL(19,4);
ALTER TABLE products ADD COLUMN IF NOT EXISTS width_in        DECIMAL(19,4);
ALTER TABLE products ADD COLUMN IF NOT EXISTS height_in       DECIMAL(19,4);
ALTER TABLE products ADD COLUMN IF NOT EXISTS stackable       BOOLEAN;
ALTER TABLE products ADD COLUMN IF NOT EXISTS geometry_source TEXT;

COMMENT ON COLUMN products.length_in IS 'PIM-canonical length in inches; NULL = no geometry entered yet (not zero).';
COMMENT ON COLUMN products.width_in IS 'PIM-canonical width in inches; NULL = no geometry entered yet (not zero).';
COMMENT ON COLUMN products.height_in IS 'PIM-canonical height in inches; NULL = no geometry entered yet (not zero).';
COMMENT ON COLUMN products.stackable IS 'Whether other cargo may be stacked on this product; NULL = unknown.';
COMMENT ON COLUMN products.geometry_source IS 'Provenance of the L/W/H triple (e.g. parametric, MANUAL); forward-compat seam for mesh geometry.';

-- Bulk catalog pulls (GET /api/integration/products with no filter) sort by SKU;
-- the existing idx_products_sku already covers that ordering, so no new index
-- is required here.

-- ---------------------------------------------------------------------------
-- 2. Dispatch seam on orders
-- ---------------------------------------------------------------------------
--   * scheduled_delivery_date — the FUTURE date an order is planned to be
--     delivered. AI_LM pulls a day's work with
--     GET /api/integration/orders?date=YYYY-MM-DD&status=CONFIRMED. The
--     integration query filters on
--     COALESCE(scheduled_delivery_date, created_at::date) so existing orders
--     without a scheduled date still resolve by their creation date instead of
--     disappearing from the board.
--   * packing_manifest — the 3D packing manifest AI_LM writes back alongside an
--     approved route (POST /api/integration/delivery-routes, `load_manifest`).
--     It is stored per stop's order so the yard "Pack Trucks" surface can
--     replay the loading steps for any order on the run.

ALTER TABLE orders ADD COLUMN IF NOT EXISTS scheduled_delivery_date DATE;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS packing_manifest        JSONB;

CREATE INDEX IF NOT EXISTS idx_orders_scheduled_delivery_date
    ON orders(scheduled_delivery_date);

COMMENT ON COLUMN orders.scheduled_delivery_date IS 'Planned delivery date; the key AI_LM pulls a day of work by.';
COMMENT ON COLUMN orders.packing_manifest IS 'Latest AI_LM-approved 3D packing manifest for this order (yard Pack Trucks).';

-- ---------------------------------------------------------------------------
-- 3. Staff roster + per-module access grants
-- ---------------------------------------------------------------------------
-- Backs POST /api/integration/validate-staff, which is AI_LM's entire login
-- path. Staff are deliberately distinct from ERP users: this repo has no users
-- table (ERP users are keyed by JWT `sub`, see migration 061), so `staff` is the
-- roster AI_LM authenticates an email against, and `module_grants` gates which
-- modules each staff member may use.

CREATE TABLE IF NOT EXISTS staff (
    id         UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    email      TEXT        UNIQUE NOT NULL,
    full_name  TEXT        NOT NULL,
    staff_no   TEXT        UNIQUE,
    role       TEXT        NOT NULL DEFAULT 'staff',
    active     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE staff IS 'Dealer staff roster; the identity AI_LM authenticates against via /api/integration/validate-staff. role is a free-text label — module access is governed by module_grants, not by role.';

-- A row means "this staff member is granted this module". Entitlement is
-- additionally gated by staff.active AND the global modules.<id>.enabled flag,
-- so an operator can cut off a whole module without touching any grant.
CREATE TABLE IF NOT EXISTS module_grants (
    staff_id   UUID        NOT NULL REFERENCES staff(id) ON DELETE CASCADE,
    module_id  TEXT        NOT NULL,
    granted_by TEXT,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (staff_id, module_id)
);

CREATE INDEX IF NOT EXISTS idx_module_grants_module ON module_grants(module_id);

-- Demo roster so a fresh `make migrate` yields a working AI_LM login without
-- hand-writing SQL. ON CONFLICT DO NOTHING (rather than DO UPDATE) so an
-- operator's edits to these rows are never clobbered by a re-run.
INSERT INTO staff (email, full_name, staff_no, role, active) VALUES
    ('dispatcher@gable.com', 'Dana Ramirez', 'STF-001', 'dispatcher', TRUE),
    ('yard@gable.com',       'Yuki Tan',     'STF-002', 'yard',       TRUE),
    ('admin@gable.com',      'Avery Kim',    'STF-003', 'admin',      TRUE)
ON CONFLICT (email) DO NOTHING;

-- Grant the dispatcher AI_LM access out of the box so validate-staff returns
-- entitled=true for at least one staff member on a fresh install.
INSERT INTO module_grants (staff_id, module_id, granted_by)
SELECT id, 'ai_lm', 'migration:080'
FROM staff WHERE email = 'dispatcher@gable.com'
ON CONFLICT (staff_id, module_id) DO NOTHING;

-- ---------------------------------------------------------------------------
-- 4. Module registration
-- ---------------------------------------------------------------------------
-- The global kill switch for the module. Registering it as enabled keeps the
-- out-of-the-box demo working; flipping this single row to 'false' revokes
-- AI_LM for every staff member at once without touching module_grants.
INSERT INTO system_settings (key, value) VALUES ('modules.ai_lm.enabled', 'true')
ON CONFLICT (key) DO NOTHING;
