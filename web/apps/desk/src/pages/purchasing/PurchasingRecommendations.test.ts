// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The purchasing recommendations page: the route answers `items: null` when
 * nothing needs reordering, and the page must read that as an empty list.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './PurchasingRecommendations'
import type { LitElement } from 'lit'
import { mountAsync, text, jsonResponse } from '../../test/dom'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('purchasing recommendations', () => {
  it('reads items: null as no recommendations, never a crash', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({
      total_items: 0, critical_count: 0, high_count: 0, medium_count: 0, low_count: 0,
      total_estimated_cost: 0, items: null,
    })))
    const el = await mountAsync<LitElement>('gable-purchasing-recommendations')
    expect(text(el)).toContain('No recommendations found')
  })
})
