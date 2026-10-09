// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The portal's project list reads every page the cursor walks, not one page
 * of 50: a customer with more projects than one page must not silently lose
 * the rest from the screen. These tests pin the paging URLs.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { ProjectService } from './ProjectService'
import type { Project } from '../types/project'

let fetchMock: ReturnType<typeof vi.fn>

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function urlOf(call: number): URL {
  return new URL(fetchMock.mock.calls[call][0] as string, 'http://localhost')
}

const PROJECT: Project = {
  id: 'p-1',
  customer_id: 'c-1',
  name: 'Ridge roof',
  status: 'active',
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

describe('ProjectService.listAllProjects', () => {
  it('pages by cursor until the last page', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ items: [PROJECT], next_cursor: 'p2', limit: 50 }))
      .mockResolvedValueOnce(jsonResponse({ items: [{ ...PROJECT, id: 'p-2' }], next_cursor: null, limit: 50 }))
    const all = await ProjectService.listAllProjects()
    expect(all.map(p => p.id)).toEqual(['p-1', 'p-2'])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(urlOf(0).search).toBe('?limit=50')
    expect(urlOf(1).search).toBe('?limit=50&cursor=p2')
  })

  it('carries the status filter on every page', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ items: [PROJECT], next_cursor: 'p2', limit: 50 }))
      .mockResolvedValueOnce(jsonResponse({ items: [], next_cursor: null, limit: 50 }))
    await ProjectService.listAllProjects({ status: 'active' })
    expect(urlOf(0).search).toBe('?status=active&limit=50')
    expect(urlOf(1).search).toBe('?status=active&limit=50&cursor=p2')
  })

  it('stops at the cap even when the server keeps offering a cursor', async () => {
    const full = Array.from({ length: 50 }, (_, i) => ({ ...PROJECT, id: `p-${i}` }))
    fetchMock.mockImplementation(async () => jsonResponse({ items: full, next_cursor: 'more', limit: 50 }))
    const all = await ProjectService.listAllProjects()
    expect(all).toHaveLength(2000)
    expect(fetchMock).toHaveBeenCalledTimes(40)
  })
})
