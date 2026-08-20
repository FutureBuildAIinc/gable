-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 082_report_schedule_format
-- Description: Records the output format an operator chose for a scheduled
--              report, so the scheduled-report CRUD surface at
--              /api/v1/reporting/schedules can round-trip it.
--
-- Purely additive: one nullable-by-default column with a backfill default, no
-- column dropped or retyped, no existing row rewritten. The reverse script is
-- migrations/down/082_report_schedule_format_down.sql — deliberately NOT a
-- sibling here, because cmd/migrate applies migrations/*.sql unconditionally
-- and a *_down.sql in this directory would run as a forward migration.
--
-- report_schedules itself was created by 032b_report_builder_and_bi.sql with
-- (report_id, cron_expression, recipients, status, last_run_at, next_run_at).
-- It had no format column, so the API had no way to persist the CSV/XLSX/PDF
-- choice the schedule editor collects.
--
-- NOT NULL DEFAULT 'CSV' is correct here, unlike the geometry columns added in
-- 080. Every schedule has to be delivered in *some* format, and CSV is what the
-- one (currently unwired) execution path in internal/reporting/scheduler.go
-- hard-codes. There is no "unknown format" state worth distinguishing, so no
-- information is destroyed by giving existing rows the format they would in
-- fact have been rendered in.

ALTER TABLE report_schedules
    ADD COLUMN IF NOT EXISTS format VARCHAR(16) NOT NULL DEFAULT 'CSV';

COMMENT ON COLUMN report_schedules.format IS 'Output format for the scheduled export: CSV, XLSX or PDF. Defaults to CSV.';

-- NOTE ON status: rows created through POST /api/v1/reporting/schedules are
-- written with status = 'STORED', not 'ACTIVE'. Nothing in this repository runs
-- scheduled reports — internal/reporting/scheduler.go is unwired and its
-- ExecuteAndSendReport never populates the report definition, and no EmailSender
-- is implemented anywhere. 'STORED' says the schedule is persisted but not
-- executing, and Scheduler.Start() only ever registers 'ACTIVE' rows, so these
-- cannot start firing by accident if the scheduler is wired up later. See
-- internal/reporting/handler.go for the honest API contract that goes with this.
