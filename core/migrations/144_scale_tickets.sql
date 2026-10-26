-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration 144: the scale integration for bulk materials (CP-19, ruling D8).
--
--   1  scale_devices: the certified scale the counter reads, with its
--      certificate of conformance number and its yard location, so every
--      reading that becomes money names the device that produced it.
--   2  scale_tickets: the weight ticket with a consecutive number that is
--      never reused and a void state instead of a delete, carrying the
--      gross, tare and net, the vehicle, whether the weight came from the
--      scale or from a manual entry, and the net basis (Gable computes the
--      net as the difference of two gross readings, so it is a software net,
--      never a scale tare operation).
--   3  pos_line_items: the weight block on the counter line.
--   4  products: the density in pounds per cubic yard with its volume state,
--      the only bridge between a weight price and a volume quantity.

CREATE TABLE IF NOT EXISTS scale_devices (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    branch_id UUID NOT NULL REFERENCES locations (id),
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    protocol TEXT NOT NULL DEFAULT 'SMA' CHECK (protocol IN ('SMA')),
    endpoint TEXT NOT NULL CHECK (char_length(endpoint) BETWEEN 1 AND 200),
    certificate_no TEXT NULL,
    location_note TEXT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE SEQUENCE IF NOT EXISTS scale_ticket_number_seq;

CREATE TABLE IF NOT EXISTS scale_tickets (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ticket_number TEXT NOT NULL DEFAULT 'WT-' || lpad(nextval('scale_ticket_number_seq')::text, 6, '0'),
    branch_id UUID NOT NULL REFERENCES locations (id),
    scale_device_id UUID NULL REFERENCES scale_devices (id),
    vehicle_ref TEXT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('WEIGH_IN', 'WEIGH_OUT', 'SINGLE')),
    status TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'COMPLETED', 'VOID')),
    gross_lb NUMERIC(12, 4) NULL CHECK (gross_lb IS NULL OR gross_lb >= 0),
    tare_lb NUMERIC(12, 4) NULL CHECK (tare_lb IS NULL OR tare_lb >= 0),
    net_lb NUMERIC(12, 4) NULL CHECK (net_lb IS NULL OR net_lb >= 0),
    reading_unit TEXT NOT NULL DEFAULT 'lb' CHECK (reading_unit IN ('lb', 'kg')),
    source TEXT NOT NULL CHECK (source IN ('SCALE', 'MANUAL')),
    manual_reason TEXT NULL,
    manual_note TEXT NULL,
    net_basis TEXT NULL CHECK (net_basis IS NULL OR net_basis IN ('SINGLE_DRAFT', 'DOUBLE_DRAFT')),
    weigh_in_ticket_id UUID NULL REFERENCES scale_tickets (id),
    sale_line_id UUID NULL,
    captured_by TEXT NOT NULL,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    void_reason TEXT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (source <> 'MANUAL' OR manual_reason IS NOT NULL)
);

DO $$ BEGIN
    ALTER TABLE scale_tickets ADD CONSTRAINT scale_tickets_number_key UNIQUE (ticket_number);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

CREATE INDEX IF NOT EXISTS idx_scale_tickets_branch ON scale_tickets (branch_id);
CREATE INDEX IF NOT EXISTS idx_scale_tickets_vehicle ON scale_tickets (vehicle_ref) WHERE vehicle_ref IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_scale_tickets_status ON scale_tickets (status);

ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS weight_source TEXT NULL;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS weight_gross_lb NUMERIC(12, 4) NULL;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS weight_tare_lb NUMERIC(12, 4) NULL;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS weight_net_lb NUMERIC(12, 4) NULL;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS weight_net_basis TEXT NULL;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS scale_device_id UUID NULL REFERENCES scale_devices (id);
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS scale_ticket_id UUID NULL REFERENCES scale_tickets (id);
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS scale_ticket_number TEXT NULL;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS manual_weight_reason TEXT NULL;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS weight_captured_by TEXT NULL;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS weight_captured_at TIMESTAMPTZ NULL;

DO $$ BEGIN
    ALTER TABLE pos_line_items ADD CONSTRAINT pos_line_items_weight_source_check
        CHECK (weight_source IS NULL OR weight_source IN ('SCALE', 'MANUAL'));
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE pos_line_items ADD CONSTRAINT pos_line_items_weight_shape
        CHECK ((weight_source IS NULL) = (weight_net_lb IS NULL));
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE pos_line_items ADD CONSTRAINT pos_line_items_weight_manual_reason
        CHECK (weight_source IS DISTINCT FROM 'MANUAL' OR manual_weight_reason IS NOT NULL);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

ALTER TABLE scale_tickets
    ADD CONSTRAINT scale_tickets_sale_line_fkey
    FOREIGN KEY (sale_line_id) REFERENCES pos_line_items (id);

ALTER TABLE products ADD COLUMN IF NOT EXISTS density_lb_per_cy NUMERIC(12, 4) NULL
    CHECK (density_lb_per_cy IS NULL OR density_lb_per_cy > 0);
ALTER TABLE products ADD COLUMN IF NOT EXISTS density_volume_state TEXT NULL
    CHECK (density_volume_state IS NULL OR density_volume_state IN ('LOOSE', 'COMPACTED'));
