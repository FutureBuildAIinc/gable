-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 146_pos_offline_counter.sql.
--
-- Refuses while offline exceptions, offline or abandoned counter sales, or
-- OFFLINE_RATE orders or invoices exist in the database.

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pos_offline_exceptions) THEN
        RAISE EXCEPTION '146 down refused: pos_offline_exceptions holds rows';
    END IF;
    IF EXISTS (SELECT 1 FROM pos_transactions WHERE status = 'ABANDONED' OR device_epoch IS NOT NULL) THEN
        RAISE EXCEPTION '146 down refused: pos_transactions holds ABANDONED or offline rows';
    END IF;
    IF EXISTS (SELECT 1 FROM orders WHERE tax_source = 'OFFLINE_RATE') THEN
        RAISE EXCEPTION '146 down refused: orders holds OFFLINE_RATE rows';
    END IF;
    IF EXISTS (SELECT 1 FROM invoices WHERE tax_source = 'OFFLINE_RATE') THEN
        RAISE EXCEPTION '146 down refused: invoices holds OFFLINE_RATE rows';
    END IF;
END $$;

ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_tax_source_check;
ALTER TABLE invoices ADD CONSTRAINT invoices_tax_source_check
    CHECK (tax_source IN ('EXEMPT', 'PROVIDER', 'SHIP_TO_RATE', 'BRANCH_RATE', 'LEGACY'));

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_tax_source_check;
ALTER TABLE orders ADD CONSTRAINT orders_tax_source_check
    CHECK (tax_source IN ('EXEMPT', 'PROVIDER', 'SHIP_TO_RATE', 'BRANCH_RATE', 'LEGACY'));

ALTER TABLE pos_sync_log DROP COLUMN IF EXISTS gap_count;
ALTER TABLE pos_sync_log DROP COLUMN IF EXISTS refused_count;
ALTER TABLE pos_sync_log DROP COLUMN IF EXISTS exception_count;

ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_transactions_status_check;
ALTER TABLE pos_transactions ADD CONSTRAINT pos_transactions_status_check
    CHECK (status IN ('OPEN', 'HELD', 'COMPLETED', 'VOIDED', 'RETURNED'));

DROP INDEX IF EXISTS idx_pos_transactions_offline_session;
DROP INDEX IF EXISTS idx_pos_transactions_replaces_sale;
DROP INDEX IF EXISTS uq_pos_transactions_device_seq;

ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_transactions_confirmed_mark_check;
ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_transactions_queue_total_cents_check;
ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_transactions_queue_count_check;
ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_transactions_device_seq_check;
ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_transactions_device_epoch_check;

ALTER TABLE pos_transactions DROP COLUMN IF EXISTS confirmed_mark;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS queue_total_cents;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS queue_count;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS abandoned_reason;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS same_customer_confirmed;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS unknown_cart_id;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS different_customer;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS replaces_sale_id;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS offline_session_id;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS business_date;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS captured_at;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS prev_digest;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS signature_digest;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS signature;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS snapshot_id;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS offline_ref;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS device_seq;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS device_epoch;

DROP TABLE IF EXISTS pos_offline_exceptions;
DROP TABLE IF EXISTS pos_offline_snapshots;
DROP TABLE IF EXISTS pos_register_devices;
DROP TABLE IF EXISTS pos_offline_policy;

DELETE FROM system_settings WHERE key LIKE 'pos.offline.%';
