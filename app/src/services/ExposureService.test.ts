// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The wire contract between the exposure UI and the Go handlers.
 *
 * Every assertion here mirrors a name the backend actually reads:
 * `ExposureHandler.HandleListExposure` parses `owner`, `state`, `customer_id`,
 * `index_code`, `min_dollars`, `limit`, `offset`; the escalation-policy and
 * index-refresh endpoints are method-sensitive. A renamed query parameter does
 * not fail a build or a type-check — it silently returns the *unfiltered* book,
 * which for a salesperson means seeing every account's exposure. That is what
 * these tests catch.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { ExposureService } from './ExposureService'

let fetchMock: ReturnType<typeof vi.fn>

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/** The URL of the most recent request, parsed. */
function lastUrl(): URL {
  const raw = fetchMock.mock.calls[fetchMock.mock.calls.length - 1][0] as string
  return new URL(raw, 'http://localhost')
}

function lastInit(): RequestInit {
  return (fetchMock.mock.calls[fetchMock.mock.calls.length - 1][1] ?? {}) as RequestInit
}

function lastBody(): Record<string, unknown> {
  return JSON.parse(lastInit().body as string) as Record<string, unknown>
}

beforeEach(() => {
  fetchMock = vi.fn().mockImplementation(() => jsonResponse({ items: [], total: 0 }))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('ExposureService.listAtRisk — query contract', () => {
  it('sends no query string when no filters are supplied', async () => {
    await ExposureService.listAtRisk()
    const url = lastUrl()
    expect(url.pathname).toBe('/api/v1/quotes/exposure')
    expect(url.search).toBe('')
  })

  it('maps every filter to the parameter name the handler reads', async () => {
    await ExposureService.listAtRisk({
      owner: 'me',
      state: 'FLAGGED,ACK_REQUIRED',
      customerId: 'cust-1',
      indexCode: 'RL_SPF_2X4',
      minDollars: 250,
      limit: 25,
      offset: 50,
    })
    const q = lastUrl().searchParams
    expect(q.get('owner')).toBe('me')
    expect(q.get('state')).toBe('FLAGGED,ACK_REQUIRED')
    expect(q.get('customer_id')).toBe('cust-1')
    expect(q.get('index_code')).toBe('RL_SPF_2X4')
    expect(q.get('min_dollars')).toBe('250')
    expect(q.get('limit')).toBe('25')
    expect(q.get('offset')).toBe('50')
  })

  it('omits filters that were not supplied rather than sending empty values', async () => {
    await ExposureService.listAtRisk({ owner: 'all' })
    const q = lastUrl().searchParams
    expect(q.get('owner')).toBe('all')
    expect(q.has('state')).toBe(false)
    expect(q.has('customer_id')).toBe(false)
    expect(q.has('min_dollars')).toBe(false)
  })

  it('sends min_dollars=0 rather than dropping it', async () => {
    // 0 is falsy but meaningful: "no floor". A truthiness check here would
    // silently apply the server default instead.
    await ExposureService.listAtRisk({ minDollars: 0 })
    expect(lastUrl().searchParams.get('min_dollars')).toBe('0')
  })

  it('defaults the summary call to the caller own book', async () => {
    await ExposureService.atRiskSummary()
    const q = lastUrl().searchParams
    expect(q.get('owner')).toBe('me')
    expect(q.get('summary')).toBe('true')
  })

  it('scopes the summary to an explicit owner when asked', async () => {
    await ExposureService.atRiskSummary('all')
    expect(lastUrl().searchParams.get('owner')).toBe('all')
  })
})

describe('ExposureService — per-quote actions', () => {
  it('POSTs an acknowledgment with the snake_case body the handler decodes', async () => {
    await ExposureService.acknowledge('q-1', {
      method: 'VERBAL',
      customer_contact: 'Dana Ruiz',
      notes: 'confirmed current market pricing by phone',
    })
    expect(lastUrl().pathname).toBe('/api/v1/quotes/q-1/exposure/acknowledge')
    expect(lastInit().method).toBe('POST')
    expect(lastBody()).toEqual({
      method: 'VERBAL',
      customer_contact: 'Dana Ruiz',
      notes: 'confirmed current market pricing by phone',
    })
  })

  it('POSTs request-ack with no body', async () => {
    await ExposureService.requestAck('q-1')
    expect(lastUrl().pathname).toBe('/api/v1/quotes/q-1/exposure/request-ack')
    expect(lastInit().method).toBe('POST')
    expect(lastInit().body).toBeUndefined()
  })

  it('POSTs an override carrying its justification', async () => {
    await ExposureService.override('q-1', { notes: 'owner released against policy' })
    expect(lastUrl().pathname).toBe('/api/v1/quotes/q-1/exposure/override')
    expect(lastInit().method).toBe('POST')
    expect(lastBody().notes).toBe('owner released against policy')
  })

  it('uses POST for the escalate-now preview, matching the route', async () => {
    // The preview writes nothing, but the route is registered as POST. A GET
    // here would 405 at runtime with no type error.
    await ExposureService.escalateNowPreview('q-1')
    expect(lastUrl().pathname).toBe('/api/v1/quotes/q-1/exposure/escalate-now')
    expect(lastInit().method).toBe('POST')
  })
})

describe('ExposureService — market index admin', () => {
  it('reads history from the index-scoped path', async () => {
    await ExposureService.getIndexHistory('idx-1')
    expect(lastUrl().pathname).toBe('/api/v1/market-indices/idx-1/history')
  })

  it('separates the applying refresh from the dry-run preview', async () => {
    await ExposureService.refreshIndex('idx-1', { new_value: 512.5, source: 'MANUAL' })
    expect(lastUrl().pathname).toBe('/api/v1/market-indices/idx-1/refresh')
    expect(lastBody()).toEqual({ new_value: 512.5, source: 'MANUAL' })

    await ExposureService.previewRefresh('idx-1', { new_value: 512.5, source: 'MANUAL' })
    expect(lastUrl().pathname).toBe('/api/v1/market-indices/idx-1/refresh/preview')
  })

  it('POSTs the admin safety-net scan trigger', async () => {
    await ExposureService.runScan()
    expect(lastUrl().pathname).toBe('/api/v1/admin/exposure-scan')
    expect(lastInit().method).toBe('POST')
  })
})

describe('ExposureService — customer escalation policy', () => {
  it('reads the policy with GET', async () => {
    await ExposureService.getEscalationPolicy('cust-1')
    expect(lastUrl().pathname).toBe('/api/v1/customers/cust-1/escalation-policy')
    expect(lastInit().method ?? 'GET').toBe('GET')
  })

  it('writes the policy with PUT, not POST', async () => {
    // The backend registers PUT only; a POST would 405 and the operator would
    // see "saved" in the UI while nothing changed.
    await ExposureService.setEscalationPolicy('cust-1', {
      policy: 'REQUIRE_ACK',
      threshold_pct: 4.5,
    })
    expect(lastInit().method).toBe('PUT')
    expect(lastBody()).toEqual({ policy: 'REQUIRE_ACK', threshold_pct: 4.5 })
  })
})

describe('ExposureService — error handling', () => {
  it('rejects on a non-2xx response instead of resolving with junk', async () => {
    fetchMock.mockImplementation(() => jsonResponse({ error: 'forbidden' }, 403))
    await expect(ExposureService.listAtRisk({ owner: 'all' })).rejects.toThrow('API Error: 403')
  })

  it('surfaces the 409 an already-cleared acknowledgment returns', async () => {
    fetchMock.mockImplementation(() => jsonResponse({ error: 'quote exposure already cleared' }, 409))
    await expect(
      ExposureService.acknowledge('q-1', { method: 'EMAIL', notes: 'customer confirmed by email' }),
    ).rejects.toThrow('API Error: 409')
  })
})
