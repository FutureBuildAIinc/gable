-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 151: the pending sign in table goes whole; the oidc issuer fields
-- the migration cleared are configuration that lives in Appwrite and are not
-- restored.

DROP TABLE IF EXISTS pending_sign_ins;
