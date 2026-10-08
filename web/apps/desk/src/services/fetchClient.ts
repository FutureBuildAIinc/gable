// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The shared fetch client moved to @gable/auth when web/ became one
 * workspace (JWT custody in memory, the front door's session handoff).
 * This module remains as the desk's import seam so every service keeps
 * its `./fetchClient` import; new code should import @gable/auth directly.
 */
export * from '@gable/auth';
