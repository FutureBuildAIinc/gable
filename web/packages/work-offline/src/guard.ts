// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The guard entry (`@gable/work-offline/guard`): the no signal guard and the
 * money scope fence without anything else. A surface whose writes only need
 * the honest refusal imports this and ships no store and no sync driver.
 */
export * from './signal.ts'
export * from './scopes.ts'
