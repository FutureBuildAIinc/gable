// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The invoice list on the wire contract (C2-3): the cursor envelope, the
 * document number, lowercase statuses with the computed Overdue badge, cents
 * money, the filters and the load-more cursor.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './InvoiceList'
import type { GableInvoiceList } from './InvoiceList'
import { mountAsync, text, flush } from '../../test/dom'
import { invoiceSummary, routedFetch } from '../../test/salesFixtures'

describe('gable-invoice-list', () => {
  let el: GableInvoiceList
  let state: ReturnType<typeof routedFetch>

  function page(items: ReturnType<typeof invoiceSummary>[], next: string | null = null, total?: number) {
    return { items, next_cursor: next, limit: 50, ...(total !== undefined ? { total } : {}) }
  }

  beforeEach(() => {
    state = routedFetch({
      '/api/v1/invoices': () =>
        page(
          [
            invoiceSummary({ id: 'a', number: 'IN-000002', status: 'unpaid', is_overdue: true, due_date: '2026-07-01' }),
            invoiceSummary({ id: 'b', number: 'IN-000001', status: 'paid', open_cents: 0, is_overdue: false }),
            invoiceSummary({ id: 'c', number: 'IN-000003', status: 'written_off', open_cents: 0 }),
          ],
          'cursor-2',
          12,
        ),
      '/api/v1/customers': () => ({ items: [{ id: 'c1', name: 'Ridgeview Framing' }], next_cursor: null, limit: 200 }),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('lists the document number, cents money and the total from include=total', async () => {
    el = await mountAsync<GableInvoiceList>('gable-invoice-list')
    const body = text(el)
    expect(body).toContain('IN-000002')
    expect(body).toContain('$846.62')
    expect(el.querySelector('[data-testid="invoice-total"]')?.textContent).toContain('12 invoices')
    const call = state.calls.find(c => c.url.pathname === '/api/v1/invoices')!
    expect(call.url.searchParams.get('include')).toBe('total')
  })

  it('shows lowercase statuses as words and an Overdue badge only where is_overdue is set', async () => {
    el = await mountAsync<GableInvoiceList>('gable-invoice-list')
    const rows = Array.from(el.querySelectorAll('tbody tr'))
    expect(text(rows[0])).toContain('Unpaid')
    expect(text(rows[0])).toContain('Overdue')
    expect(text(rows[1])).toContain('Paid')
    expect(text(rows[1])).not.toContain('Overdue')
    expect(text(rows[2])).toContain('Written Off')
    expect(el.querySelectorAll('[data-testid="overdue-badge"]').length).toBe(1)
  })

  it('offers Load more while a cursor remains and sends it on the next request', async () => {
    el = await mountAsync<GableInvoiceList>('gable-invoice-list')
    const more = Array.from(el.querySelectorAll('button')).find(b => b.textContent?.includes('Load more')) as HTMLButtonElement
    expect(more).toBeTruthy()
    more.click()
    await flush()
    await el.updateComplete
    const last = state.calls.filter(c => c.url.pathname === '/api/v1/invoices').pop()!
    expect(last.url.searchParams.get('cursor')).toBe('cursor-2')
    expect(last.url.searchParams.has('include')).toBe(false)
  })

  it('sends the status and overdue filters under their contract names', async () => {
    el = await mountAsync<GableInvoiceList>('gable-invoice-list')
    const select = el.querySelector('select[aria-label="Status"]') as HTMLSelectElement
    select.value = 'partial'
    select.dispatchEvent(new Event('change'))
    await flush()
    const overdue = el.querySelector('input[aria-label="Overdue only"]') as HTMLInputElement
    overdue.checked = true
    overdue.dispatchEvent(new Event('change'))
    await flush()
    await el.updateComplete
    const last = state.calls.filter(c => c.url.pathname === '/api/v1/invoices').pop()!
    expect(last.url.searchParams.get('status')).toBe('partial')
    expect(last.url.searchParams.get('overdue')).toBe('true')
  })

  it('shows an empty state that names the filter, and an error state with Retry', async () => {
    vi.stubGlobal('fetch', vi.fn(routedFetch({ '/api/v1/invoices': () => page([], null, 0), '/api/v1/customers': () => page([]) }).fn))
    el = await mountAsync<GableInvoiceList>('gable-invoice-list')
    expect(el.querySelector('[data-testid="invoice-empty"]')?.textContent).toContain('No invoices')

    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { code: 'internal', message: 'boom here' }, meta: {} }), { status: 500, headers: { 'Content-Type': 'application/json' } })))
    const failed = await mountAsync<GableInvoiceList>('gable-invoice-list')
    expect(text(failed)).toContain('boom here')
    expect(text(failed)).toContain('Retry')
  })
})
