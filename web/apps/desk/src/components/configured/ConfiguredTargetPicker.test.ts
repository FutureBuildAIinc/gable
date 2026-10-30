// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The configured item's picker (QE-06): the dealer says which draft quote or
 * editable order the configured item belongs on, or starts a new draft quote.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, q, text, flush, jsonResponse } from '../../test/dom'
import type { GableConfiguredTargetPicker } from './ConfiguredTargetPicker'
import './ConfiguredTargetPicker'

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  fetchMock = vi.fn(async () => jsonResponse({ items: [], next_cursor: null, limit: 50 }))
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('the configured item picker', () => {
  it('lists the draft quotes and says which one was picked', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        items: [
          { id: 'q1', number: 'Q-000101', customer_name: 'Ridgeview Framing', status: 'DRAFT' },
          { id: 'q2', number: 'Q-000102', customer_name: 'Hollow Creek', status: 'DRAFT' },
        ],
        next_cursor: null,
        limit: 50,
      }),
    )
    const el = await mount<GableConfiguredTargetPicker>('gable-configured-target-picker', { target: 'quote' })
    await flush()

    expect(text(el)).toContain('Q-000101')
    expect(text(el)).toContain('Ridgeview Framing')
    expect(fetchMock.mock.calls[0][0]).toContain('/api/v1/quotes')
    expect(String(fetchMock.mock.calls[0][0])).toContain('status=draft')

    let picked: { id: string | null } | null = null
    el.addEventListener('configured-target-picked', (e) => {
      picked = (e as CustomEvent<{ id: string | null }>).detail
    })
    const drafts = Array.from(el.querySelectorAll('[data-testid="target-draft"]')) as HTMLElement[]
    expect(drafts).toHaveLength(2)
    drafts[1].click()
    await flush()
    expect(picked).toEqual({ id: 'q2' })
  })

  it('offers a new draft quote when the dealer starts one', async () => {
    const el = await mount<GableConfiguredTargetPicker>('gable-configured-target-picker', { target: 'quote' })
    await flush()

    let picked: { id: string | null } | null = null
    el.addEventListener('configured-target-picked', (e) => {
      picked = (e as CustomEvent<{ id: string | null }>).detail
    })
    q<HTMLButtonElement>(el, '[data-testid="new-draft"]').click()
    await flush()
    expect(picked).toEqual({ id: null })
  })

  it('lists the editable orders when the item is headed for an order', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        items: [{ id: 'o1', number: 'SO-000201', customer_name: 'Ridgeview Framing', status: 'DRAFT' }],
        next_cursor: null,
        limit: 50,
      }),
    )
    const el = await mount<GableConfiguredTargetPicker>('gable-configured-target-picker', { target: 'order' })
    await flush()

    expect(text(el)).toContain('SO-000201')
    expect(fetchMock.mock.calls[0][0]).toContain('/api/v1/orders')
    expect(String(fetchMock.mock.calls[0][0])).toContain('status=draft')
  })

  it('says when there is no draft to pick', async () => {
    const el = await mount<GableConfiguredTargetPicker>('gable-configured-target-picker', { target: 'quote' })
    await flush()
    expect(q(el, '[data-testid="no-drafts"]')).toBeTruthy()
  })
})
