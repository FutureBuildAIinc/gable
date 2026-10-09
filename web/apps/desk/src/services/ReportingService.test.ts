// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The reporting read paths: query-string construction and the descriptive
 * error each failing report endpoint throws. (Credit memos moved to the
 * credit memo module, services/CreditMemoService.ts.)
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { ReportingService } from './ReportingService'

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
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
