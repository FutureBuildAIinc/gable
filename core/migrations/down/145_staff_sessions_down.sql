-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 145: the roster link leaves staff, then the session table goes
-- whole; nothing outside the sign in slice references it.

DROP INDEX IF EXISTS staff_sub_uniq;
ALTER TABLE staff DROP COLUMN IF EXISTS sub;
DROP TABLE IF EXISTS staff_sessions;
