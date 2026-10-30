-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 144: the weight block leaves the counter line and the product,
-- then the ticket and device tables go whole; nothing outside the scale
-- slice references them.

ALTER TABLE pos_line_items
    DROP COLUMN IF EXISTS weight_source,
    DROP COLUMN IF EXISTS weight_gross_lb,
    DROP COLUMN IF EXISTS weight_tare_lb,
    DROP COLUMN IF EXISTS weight_net_lb,
    DROP COLUMN IF EXISTS weight_net_basis,
    DROP COLUMN IF EXISTS scale_device_id,
    DROP COLUMN IF EXISTS scale_ticket_id,
    DROP COLUMN IF EXISTS scale_ticket_number,
    DROP COLUMN IF EXISTS manual_weight_reason,
    DROP COLUMN IF EXISTS weight_captured_by,
    DROP COLUMN IF EXISTS weight_captured_at;

ALTER TABLE products
    DROP COLUMN IF EXISTS density_lb_per_cy,
    DROP COLUMN IF EXISTS density_volume_state;

DROP TABLE IF EXISTS scale_tickets;
DROP TABLE IF EXISTS scale_devices;
DROP SEQUENCE IF EXISTS scale_ticket_number_seq;
