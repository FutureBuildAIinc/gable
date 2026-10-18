// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * @gable/work-offline: the no signal guard and the money scope fence.
 * Money never queues: writes resolve online, reads may ride the cache.
 */

export * from './signal.ts'
export * from './scopes.ts'
