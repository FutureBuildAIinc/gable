// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// What following a send returns when this sign in may not read the delivery
// record (a 403): the mail was handed on, its outcome cannot be seen. Never a
// failure. It lives apart from the service so a screen's test that replaces
// the service still sees the same value.
export const OUTCOME_UNKNOWN = 'unknown' as const;
