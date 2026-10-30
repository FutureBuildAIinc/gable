// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The market index staleness banner (QE-07-b1): it shows when an active
 * index's newest value is older than the threshold the deployment set, and
 * self-hides otherwise.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './index-staleness-banner'
import type { GableIndexStalenessBanner } from './index-staleness-banner'
import { mount, update, flush, q, text, jsonResponse } from '../../test/dom'

let answer: unknown

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  answer = { threshold_days: 7, stale: false, stale_indices: [], indices: [] }
  vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(answer)))
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

async function open(): Promise<GableIndexStalenessBanner> {
  const el = await mount<GableIndexStalenessBanner>('gable-index-staleness-banner')
  await flush()
  await update(el, {})
  return el
}

describe('market index staleness banner', () => {
  it('shows nothing when nothing is stale', async () => {
    const el = await open()
    expect(el.querySelector('[data-testid="index-staleness-banner"]')).toBeNull()
  })

  it('names the stale index and the threshold', async () => {
    answer = {
      threshold_days: 7,
      stale: true,
      stale_indices: [{ index_id: 'a', index_code: 'RLS', name: 'Random Lengths SYP', current_value: 100,
        newest_date: '2026-01-01T00:00:00Z', days_old: 30, stale: true }],
      indices: [],
    }
    const el = await open()
    const banner = q(el, '[data-testid="index-staleness-banner"]')
    expect(text(banner)).toContain('Random Lengths SYP')
    expect(text(q(el, '[data-testid="staleness-message"]'))).toContain('7 day')
    expect(text(q(el, '[data-testid="staleness-message"]'))).toContain('30 days old')
  })

  it('shows nothing when the answer cannot be read', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new Error('offline') }))
    const el = await open()
    expect(el.querySelector('[data-testid="index-staleness-banner"]')).toBeNull()
  })
})
