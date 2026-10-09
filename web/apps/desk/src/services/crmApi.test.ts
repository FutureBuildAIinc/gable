// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The activity feed reads every page the cursor walks, not one page of 50:
 * a customer with more activities than one page must not silently lose the
 * rest from the screen. These tests pin the paging URLs.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { crmApi } from './crmApi'
import type { Activity } from '../types/crm'

let fetchMock: ReturnType<typeof vi.fn>

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function urlOf(call: number): URL {
  return new URL(fetchMock.mock.calls[call][0] as string, 'http://localhost')
}

const ACTIVITY: Activity = {
  id: 'a-1',
  customer_id: 'c-1',
  contact_id: null,
  activity_type: 'call',
  description: 'rang',
  logged_by: null,
  activity_date: '2026-01-01T00:00:00.000000Z',
  revision: 1,
  created_at: '2026-01-01T00:00:00.000000Z',
  updated_at: '2026-01-01T00:00:00.000000Z',
}

beforeEach(() => {
  fetchMock = vi.fn(async () => jsonResponse({}))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('crmApi.listAllActivities', () => {
  it('pages by cursor until the last page', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ items: [ACTIVITY], next_cursor: 'p2', limit: 50 }))
      .mockResolvedValueOnce(jsonResponse({ items: [{ ...ACTIVITY, id: 'a-2' }], next_cursor: null, limit: 50 }))
    const all = await crmApi.listAllActivities('c-1')
    expect(all.map(a => a.id)).toEqual(['a-1', 'a-2'])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(urlOf(0).search).toBe('?limit=50')
    expect(urlOf(1).search).toBe('?limit=50&cursor=p2')
  })

  it('stops at the cap even when the server keeps offering a cursor', async () => {
    const full = Array.from({ length: 50 }, (_, i) => ({ ...ACTIVITY, id: `a-${i}` }))
    fetchMock.mockImplementation(async () => jsonResponse({ items: full, next_cursor: 'more', limit: 50 }))
    const all = await crmApi.listAllActivities('c-1')
    expect(all).toHaveLength(2000)
    expect(fetchMock).toHaveBeenCalledTimes(40)
  })
})
