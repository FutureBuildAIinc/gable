-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 130_pro_portal_schedules_and_boards
--
-- The Contractor Pro Portal's core (the parity plan's M35).
--
-- 1. The five contractor portal roles replace the legacy three. The mapping
--    for existing users is stated here and applied in place:
--      Admin       -> owner            (full reach, including team and money)
--      Buyer       -> project_manager  (orders, quotes, schedule, sees money)
--      View-Only   -> estimator        (quotes and orders, no money, no team)
--    The other two roles (site_super, ap_clerk) start empty; invites place
--    people into them from now on. Roles are never checked as strings on a
--    route again: the server resolves each role to a capability set
--    (core/internal/portal/capabilities.go) and every route checks a
--    capability.
--
-- 2. Per yard delivery windows and pickup slots, and the contractor's holds
--    on them. A window is a weekly recurring rule (weekday, start, end,
--    capacity, cutoff); a hold is one customer's claim on one concrete date
--    of one window, at checkout or on an open order. Times are times of day
--    in the yard's own time zone (locations.timezone); every instant the
--    API exchanges is UTC.
--
-- 3. Editable site delivery instructions on an order. The dealer's delivery
--    instructions live on the stop (deliveries.delivery_instructions); this
--    column is the contractor's own instruction for the site, editable while
--    the order is unfulfilled.

-- 1. Roles -------------------------------------------------------------------

ALTER TABLE customer_users DROP CONSTRAINT IF EXISTS customer_users_role_check;

UPDATE customer_users
SET role = CASE role
        WHEN 'Admin' THEN 'owner'
        WHEN 'Buyer' THEN 'project_manager'
        WHEN 'View-Only' THEN 'estimator'
        ELSE 'estimator'
    END,
    updated_at = NOW()
WHERE role NOT IN ('owner', 'estimator', 'project_manager', 'site_super', 'ap_clerk');

ALTER TABLE customer_users ALTER COLUMN role SET DEFAULT 'estimator';
ALTER TABLE customer_users ADD CONSTRAINT customer_users_role_check
    CHECK (role IN ('owner', 'estimator', 'project_manager', 'site_super', 'ap_clerk'));

ALTER TABLE portal_invites DROP CONSTRAINT IF EXISTS portal_invites_role_check;

UPDATE portal_invites
SET role = CASE role
        WHEN 'Admin' THEN 'owner'
        WHEN 'Buyer' THEN 'project_manager'
        WHEN 'View-Only' THEN 'estimator'
        ELSE 'estimator'
    END
WHERE role NOT IN ('owner', 'estimator', 'project_manager', 'site_super', 'ap_clerk');

ALTER TABLE portal_invites ALTER COLUMN role SET DEFAULT 'estimator';
ALTER TABLE portal_invites ADD CONSTRAINT portal_invites_role_check
    CHECK (role IN ('owner', 'estimator', 'project_manager', 'site_super', 'ap_clerk'));

-- 2. Scheduling windows and holds ---------------------------------------------

CREATE TABLE IF NOT EXISTS yard_schedule_windows (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    yard_id UUID NOT NULL REFERENCES locations(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('delivery', 'pickup')),
    -- ISO weekday: 0 Sunday .. 6 Saturday, matching EXTRACT(DOW FROM date)
    weekday INT NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    start_time TIME NOT NULL,
    end_time TIME NOT NULL,
    CHECK (end_time > start_time),
    capacity INT NOT NULL CHECK (capacity > 0),
    -- How many minutes before the window starts the yard stops accepting
    -- holds. 0 means holds are taken until the window starts.
    cutoff_minutes INT NOT NULL DEFAULT 0 CHECK (cutoff_minutes >= 0),
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_yard_schedule_windows_yard ON yard_schedule_windows(yard_id, kind);

CREATE TABLE IF NOT EXISTS portal_schedule_holds (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    window_id UUID NOT NULL REFERENCES yard_schedule_windows(id) ON DELETE CASCADE,
    hold_date DATE NOT NULL,
    customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    -- Null at checkout (before an order exists); set when the hold rides on
    -- an open order. One open order carries one held hold at a time.
    order_id UUID REFERENCES orders(id) ON DELETE SET NULL,
    created_by UUID REFERENCES customer_users(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'held' CHECK (status IN ('held', 'released', 'used', 'expired')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_schedule_holds_window_date
    ON portal_schedule_holds(window_id, hold_date, status);
CREATE INDEX IF NOT EXISTS idx_schedule_holds_customer
    ON portal_schedule_holds(customer_id, status);

-- 3. Site delivery instructions ------------------------------------------------

ALTER TABLE orders ADD COLUMN IF NOT EXISTS site_delivery_instructions TEXT;
