// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The desk reads every page of the lists it shows beside totals or offers as
 * choices (review of PR 55): the aging, the customer's unapplied payments and
 * open invoices, and an invoice's applications each walk next_cursor. One test
 * per read answers two pages and checks both are returned and the cursor of
 * the first is sent for the second.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { ArService } from './ArService'
import { AccountService } from './AccountService'
import { paymentService } from './paymentService'

let fetchMock: ReturnType<typeof vi.fn>

function pagesOf(first: unknown[], second: unknown[]) {
  fetchMock = vi.fn(async (raw: string) => {
    const url = new URL(raw, 'http://localhost')
    const body = url.searchParams.get('cursor') === 'p2'
      ? { items: second, next_cursor: null, limit: 50 }
      : { items: first, next_cursor: 'p2', limit: 50 }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)
}

function cursorsSent(): (string | null)[] {
  return fetchMock.mock.calls.map(c => new URL(c[0] as string, 'http://localhost').searchParams.get('cursor'))
}

afterEach(() => vi.unstubAllGlobals())
beforeEach(() => { fetchMock = vi.fn() })

describe('lists the desk walks to the last page', () => {
  it('the AR aging', async () => {
    pagesOf([{ customer_id: 'c1' }, { customer_id: 'c2' }], [{ customer_id: 'c3' }])
    const rows = await ArService.agingAll({ groupBy: 'customer', basis: 'due_date', asOf: '2031-01-01' })
    expect(rows.map(r => r.customer_id)).toEqual(['c1', 'c2', 'c3'])
    expect(cursorsSent()).toEqual([null, 'p2'])
    expect(new URL(fetchMock.mock.calls[1][0] as string, 'http://localhost').searchParams.get('as_of')).toBe('2031-01-01')
  })

  it('the unapplied payments of the apply dialog', async () => {
    pagesOf([{ id: 'pay-1' }], [{ id: 'pay-2' }])
    const rows = await AccountService.getUnappliedPayments('cust-1')
    expect(rows.map(r => r.id)).toEqual(['pay-1', 'pay-2'])
    expect(cursorsSent()).toEqual([null, 'p2'])
  })

  it('the open invoices of the apply dialog', async () => {
    pagesOf([{ id: 'inv-1' }], [{ id: 'inv-51' }])
    const rows = await AccountService.getOpenInvoices('cust-1')
    expect(rows.map(r => r.id)).toEqual(['inv-1', 'inv-51'])
    expect(cursorsSent()).toEqual([null, 'p2'])
  })

  it('the applications of an invoice', async () => {
    pagesOf([{ id: 'app-1' }], [{ id: 'app-2' }])
    const rows = await paymentService.historyAll('inv-1')
    expect(rows.map(r => r.id)).toEqual(['app-1', 'app-2'])
    expect(cursorsSent()).toEqual([null, 'p2'])
  })
})
