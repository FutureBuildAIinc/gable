// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dollars -> cents *write* path.
 *
 * `formatCents()` guards the read side of the ERP money boundary; this guards
 * the write side. A credit memo is entered by a human in dollars and must reach
 * the backend as integer cents. Two ways to get this wrong: forget the *100
 * (issue a $0.74 credit instead of $73.88), or trust float multiplication
 * (19.99 * 100 === 1998.9999999999998, which truncates to 1998 — a one-cent
 * shortfall that will not reconcile).
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { ReportingService } from './ReportingService'

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/** The JSON body the service actually sent on its most recent call. */
function sentBody(mock: ReturnType<typeof vi.fn>): Record<string, unknown> {
  const init = mock.mock.calls[mock.mock.calls.length - 1][1] as RequestInit
  return JSON.parse(init.body as string) as Record<string, unknown>
}

let fetchMock: ReturnType<typeof vi.fn>

/** A fresh Response per call — a Response body can only be read once. */
function respondWith(body: unknown, status = 200) {
  return () => jsonResponse(body, status)
}

beforeEach(() => {
  fetchMock = vi.fn().mockImplementation(respondWith({ id: 'cm-1' }))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('ReportingService.createCreditMemo — dollars in, cents out', () => {
  it('converts whole dollars to cents', async () => {
    await ReportingService.createCreditMemo('inv-1', 100, 'damaged goods')
    expect(sentBody(fetchMock).amount_cents).toBe(10_000)
  })

  it('converts the $73.88 case to 7388 cents', async () => {
    await ReportingService.createCreditMemo('inv-1', 73.88, 'short shipment')
    expect(sentBody(fetchMock).amount_cents).toBe(7388)
  })

  it('rounds away float multiplication error instead of truncating', async () => {
    // 19.99 * 100 === 1998.9999999999998 in IEEE-754. Truncation would send 1998.
    await ReportingService.createCreditMemo('inv-1', 19.99, 'price adjustment')
    expect(sentBody(fetchMock).amount_cents).toBe(1999)
  })

  it('rounds the other classic float offender', async () => {
    // 1.005 * 100 === 100.49999999999999
    await ReportingService.createCreditMemo('inv-1', 1.005, 'rounding')
    expect(sentBody(fetchMock).amount_cents).toBe(100)
  })

  it('sends an integer, never a fractional cent', async () => {
    await ReportingService.createCreditMemo('inv-1', 12.345, 'odd amount')
    const cents = sentBody(fetchMock).amount_cents as number
    expect(Number.isInteger(cents)).toBe(true)
  })

  it('handles zero and negative amounts without changing magnitude', async () => {
    await ReportingService.createCreditMemo('inv-1', 0, 'zeroed out')
    expect(sentBody(fetchMock).amount_cents).toBe(0)

    await ReportingService.createCreditMemo('inv-1', -50.25, 'reversal')
    expect(sentBody(fetchMock).amount_cents).toBe(-5025)
  })

  it('passes the reason through untouched and POSTs to the invoice sub-resource', async () => {
    await ReportingService.createCreditMemo('inv-42', 5, 'wrong species delivered')
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/v1/invoices/inv-42/credit-memo')
    expect(init.method).toBe('POST')
    expect(sentBody(fetchMock).reason).toBe('wrong species delivered')
  })

  it('throws on a non-2xx response instead of returning a partial memo', async () => {
    fetchMock.mockImplementation(respondWith({ error: 'invoice already closed' }, 409))
    await expect(
      ReportingService.createCreditMemo('inv-1', 10, 'too late'),
    ).rejects.toThrow('Failed to create credit memo')
  })
})

describe('ReportingService query-string construction', () => {
  it('omits the query string entirely when no date is given', async () => {
    fetchMock.mockImplementation(respondWith({}))
    await ReportingService.getDailyTill()
    expect(fetchMock.mock.calls[0][0]).toMatch(/\/api\/v1\/reports\/daily-till$/)
  })

  it('appends a single date param', async () => {
    fetchMock.mockImplementation(respondWith({}))
    await ReportingService.getDailyTill('2026-08-07')
    expect(fetchMock.mock.calls[0][0]).toContain('/api/v1/reports/daily-till?date=2026-08-07')
  })

  it('appends only the params that are supplied', async () => {
    fetchMock.mockImplementation(respondWith({}))
    await ReportingService.getSalesSummary(undefined, '2026-08-07')
    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toContain('end=2026-08-07')
    expect(url).not.toContain('start=')
  })

  it('throws a descriptive error for each failing report endpoint', async () => {
    fetchMock.mockImplementation(respondWith({}, 500))
    await expect(ReportingService.getDailyTill()).rejects.toThrow('Failed to fetch daily till')
    await expect(ReportingService.getARAgingReport()).rejects.toThrow('Failed to fetch AR aging report')
  })
})
