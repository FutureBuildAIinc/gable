-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 129_dealer_signin_methods.sql.

DROP TRIGGER IF EXISTS dealer_signin_methods_truncate_guard ON dealer_signin_methods;
DROP TRIGGER IF EXISTS dealer_signin_methods_update_guard ON dealer_signin_methods;
DROP TRIGGER IF EXISTS dealer_signin_methods_delete_guard ON dealer_signin_methods;
DROP FUNCTION IF EXISTS dealer_signin_methods_keep_one();
DROP TABLE IF EXISTS dealer_signin_methods;
