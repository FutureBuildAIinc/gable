// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './CreditMemoList'
import type { GableCreditMemoList } from './CreditMemoList'
import { mountAsync, text, flush } from '../../test/dom'
import { creditMemoSummary, routedFetch } from '../../test/salesFixtures'

describe('gable-credit-memo-list', () => {
  let el: GableCreditMemoList
  let state: ReturnType<typeof routedFetch>

  beforeEach(() => {
    state = routedFetch({
      '/api/v1/credit-memos': () => ({
        items: [
          creditMemoSummary({ id: 'a', number: 'CM-000002', status: 'open', total_cents: -4400 }),
          creditMemoSummary({ id: 'b', number: null, status: 'draft', total_cents: -7055 }),
          creditMemoSummary({ id: 'c', number: 'CM-000001', status: 'void', total_cents: -100, reason_code: 'damage' }),
        ],
        next_cursor: 'more',
        limit: 50,
        total: 7,
      }),
      '/api/v1/customers': () => ({ items: [], next_cursor: null, limit: 200 }),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows a draft as Draft, a posted memo by number, and the total as a credit', async () => {
    el = await mountAsync<GableCreditMemoList>('gable-credit-memo-list')
    const rows = Array.from(el.querySelectorAll('tbody tr'))
    expect(text(rows[0])).toContain('CM-000002')
    expect(text(rows[0])).toContain('-$44.00')
    expect(rows[0].querySelector('td.text-red-400')).not.toBeNull()
    expect(text(rows[1])).toContain('Draft')
    expect(text(rows[1])).toContain('-$70.55')
    expect(text(rows[2])).toContain('Damage')
    expect(text(rows[2])).toContain('Void')
    expect(text(el.querySelector('[data-testid="credit-memo-total"]'))).toBe('7 credit memos')
  })

  it('pages by cursor and filters by status under the contract names', async () => {
    el = await mountAsync<GableCreditMemoList>('gable-credit-memo-list')
    const more = Array.from(el.querySelectorAll('button')).find(b => b.textContent?.includes('Load more')) as HTMLButtonElement
    more.click()
    await flush()
    await el.updateComplete
    let last = state.calls.filter(c => c.url.pathname === '/api/v1/credit-memos').pop()!
    expect(last.url.searchParams.get('cursor')).toBe('more')
    const select = el.querySelector('select[aria-label="Status"]') as HTMLSelectElement
    select.value = 'draft'
    select.dispatchEvent(new Event('change'))
    await flush()
    await el.updateComplete
    last = state.calls.filter(c => c.url.pathname === '/api/v1/credit-memos').pop()!
    expect(last.url.searchParams.get('status')).toBe('draft')
    expect(last.url.searchParams.has('cursor')).toBe(false)
  })

  it('reads an invoice filter from the address', async () => {
    window.history.replaceState(null, '', '/credit-memos?invoice_id=11111111-2222-4333-8444-555555555555')
    try {
      el = await mountAsync<GableCreditMemoList>('gable-credit-memo-list')
      const first = state.calls.find(c => c.url.pathname === '/api/v1/credit-memos')!
      expect(first.url.searchParams.get('invoice_id')).toBe('11111111-2222-4333-8444-555555555555')
    } finally {
      window.history.replaceState(null, '', '/')
    }
  })

  it('shows an empty state', async () => {
    vi.stubGlobal('fetch', vi.fn(routedFetch({ '/api/v1/credit-memos': () => ({ items: [], next_cursor: null, limit: 50, total: 0 }), '/api/v1/customers': () => ({ items: [], next_cursor: null, limit: 200 }) }).fn))
    const empty = await mountAsync<GableCreditMemoList>('gable-credit-memo-list')
    expect(empty.querySelector('[data-testid="credit-memo-empty"]')?.textContent).toContain('No credit memos')
  })
})
