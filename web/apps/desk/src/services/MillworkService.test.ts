// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The configurator's pickers read one category's whole catalog: the wire
 * pages it newest first, so the service follows the cursor to the end and
 * hands the options to the pickers sorted by name, the order they read in.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { MillworkService } from './MillworkService'
import type { MillworkOption } from '../types/millwork'

let fetchMock: ReturnType<typeof vi.fn>

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function urlOf(call: number): URL {
  return new URL(fetchMock.mock.calls[call][0] as string, 'http://localhost')
}

function option(id: string, name: string): MillworkOption {
  return {
    id,
    category: 'door_type',
    name,
    price_adjustment_cents: 0,
    attributes: null,
    revision: 1,
    created_at: '2026-01-01T00:00:00.000000Z',
    updated_at: '2026-01-01T00:00:00.000000Z',
  }
}

beforeEach(() => {
  fetchMock = vi.fn(async () => jsonResponse({}))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('MillworkService.getOptionsByCategory', () => {
  it('pages by cursor until the last page and sorts by name', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({
        items: [option('o-3', 'Shaker'), option('o-1', 'Flat panel')],
        next_cursor: 'p2',
        limit: 50,
      }))
      .mockResolvedValueOnce(jsonResponse({
        items: [option('o-2', 'Glass')],
        next_cursor: null,
        limit: 50,
      }))
    const all = await MillworkService.getOptionsByCategory('door_type')
    expect(all.map(o => o.name)).toEqual(['Flat panel', 'Glass', 'Shaker'])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(urlOf(0).search).toBe('?category=door_type')
    expect(urlOf(1).search).toBe('?category=door_type&cursor=p2')
  })

  it('passes the caller limit on the first page only', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [option('o-1', 'Flat panel')], next_cursor: null, limit: 10 }))
    await MillworkService.getOptionsByCategory('material', { limit: 10 })
    expect(urlOf(0).search).toBe('?category=material&limit=10')
  })
})
