// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * @gable/work-offline: the no signal guard and the money scope fence.
 * Money never queues: writes resolve online, reads may ride the cache. The
 * one exception is the counter's offline sales, kept by the work shell's Rust
 * core under ADR 0011 and never by a page (COUNTER_OFFLINE_QUEUE).
 */

export * from './signal.ts'
export * from './scopes.ts'
