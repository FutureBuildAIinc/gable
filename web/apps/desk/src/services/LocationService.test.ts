// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The location and branch wire (ADR 0001): cursor lists, a quoted If-Match on every PUT and DELETE,
 * a lowercase type, optional fields present as null, and the one error envelope.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  LIST_ALL_LOCATIONS_CAP,
  LocationService,
  buildLocationQuery,
  locationUpdateFromLocation,
} from './LocationService'
import { ApiError } from './apiError'
import type { Location } from '../types/location'

let fetchMock: ReturnType<typeof vi.fn>

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function errorResponse(status: number, code: string, message: string, details: { field: string; message: string }[] = []): Response {
  return jsonResponse({ error: { code, message, details }, meta: { request_id: 'req-1' } }, status)
}

function lastUrl(): URL {
  const raw = fetchMock.mock.calls[fetchMock.mock.calls.length - 1][0] as string
  return new URL(raw, 'http://localhost')
}

function lastInit(): RequestInit {
  return (fetchMock.mock.calls[fetchMock.mock.calls.length - 1][1] ?? {}) as RequestInit
}

function lastHeader(name: string): string | undefined {
  // fetchWithAuth hands fetch a Headers object.
  return (lastInit().headers as Headers).get(name) ?? undefined
}

function lastBody(): Record<string, unknown> {
  return JSON.parse(lastInit().body as string) as Record<string, unknown>
}

beforeEach(() => {
  fetchMock = vi.fn(async () => jsonResponse({}))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const BRANCH: Location = {
  id: 'b-1',
  parent_id: null,
  path: 'West Yard',
  type: 'branch',
  code: 'WEST',
  description: null,
  name: 'West Yard',
  address: '1 Main St',
  city: 'Springfield',
  state: 'CT',
  zip: null,
  phone: null,
  tax_jurisdiction_code: 'CT-01',
  default_tax_rate: 0.0635,
  timezone: 'America/New_York',
  active: true,
  branch_id: 'b-1',
  revision: 7,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

describe('buildLocationQuery', () => {
  it('is empty with no params and sends only the declared names', () => {
    expect(buildLocationQuery()).toBe('')
    const q = new URLSearchParams(buildLocationQuery({ includeInactive: true, limit: 50, cursor: 'c', includeTotal: true }).slice(1))
    expect(Object.fromEntries(q)).toEqual({ include_inactive: 'true', limit: '50', cursor: 'c', include: 'total' })
    expect(buildLocationQuery({ includeInactive: false, cursor: null })).toBe('')
  })
})

describe('locationUpdateFromLocation', () => {
  it('carries every mutable field, so the replace does not clear what the form hides', () => {
    expect(locationUpdateFromLocation(BRANCH)).toEqual({
      path: 'West Yard', code: 'WEST', description: null, name: 'West Yard', address: '1 Main St', city: 'Springfield',
      state: 'CT', zip: null, phone: null, tax_jurisdiction_code: 'CT-01', default_tax_rate: 0.0635,
      timezone: 'America/New_York', active: true,
    })
  })

  it('never carries type, parent_id or the revision (those are 400s or travel in If-Match)', () => {
    const body = locationUpdateFromLocation(BRANCH)
    for (const f of ['type', 'parent_id', 'revision', 'id', 'branch_id', 'created_at']) {
      expect(body).not.toHaveProperty(f)
    }
  })
})

describe('locations', () => {
  it('lists the envelope with the cursor query', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [BRANCH], next_cursor: 'n', limit: 200 }))
    const page = await LocationService.listLocations({ limit: 200 })
    expect(lastUrl().pathname).toBe('/api/v1/locations')
    expect(lastUrl().search).toBe('?limit=200')
    expect(page.items[0].type).toBe('branch')
  })

  it('listAllLocations pages by cursor and stops at the cap', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ items: [BRANCH], next_cursor: 'p2', limit: 200 }))
      .mockResolvedValueOnce(jsonResponse({ items: [{ ...BRANCH, id: 'b-2' }], next_cursor: null, limit: 200 }))
    expect((await LocationService.listAllLocations()).map(l => l.id)).toEqual(['b-1', 'b-2'])
    expect(new URL(fetchMock.mock.calls[1][0] as string, 'http://localhost').search).toBe('?limit=200&cursor=p2')

    const full = Array.from({ length: 200 }, (_, i) => ({ ...BRANCH, id: `l-${i}` }))
    fetchMock.mockImplementation(async () => jsonResponse({ items: full, next_cursor: 'more', limit: 200 }))
    expect(await LocationService.listAllLocations()).toHaveLength(LIST_ALL_LOCATIONS_CAP)
  })

  it('creates with POST and a lowercase type', async () => {
    await LocationService.createLocation({ parent_id: 'b-1', path: 'West Yard/A1', type: 'aisle', code: 'A1' })
    expect(lastUrl().pathname).toBe('/api/v1/locations')
    expect(lastInit().method).toBe('POST')
    expect(lastHeader('If-Match')).toBeUndefined()
    expect(lastBody().type).toBe('aisle')
  })

  it('updates with PUT and the quoted revision', async () => {
    await LocationService.updateLocation('l-1', { code: 'A2', path: 'West Yard/A2' }, 4)
    expect(lastUrl().pathname).toBe('/api/v1/locations/l-1')
    expect(lastInit().method).toBe('PUT')
    expect(lastHeader('If-Match')).toBe('"4"')
    expect(lastBody()).toEqual({ code: 'A2', path: 'West Yard/A2' })
  })

  it('archives with DELETE and the quoted revision', async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await LocationService.deleteLocation('l-1', 9)
    expect(lastUrl().pathname).toBe('/api/v1/locations/l-1')
    expect(lastInit().method).toBe('DELETE')
    expect(lastHeader('If-Match')).toBe('"9"')
  })
})

describe('branches', () => {
  it('lists with include_inactive only when asked', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [BRANCH], next_cursor: null, limit: 200 }))
    await LocationService.listAllBranches(true)
    expect(lastUrl().pathname).toBe('/api/v1/branches')
    expect(lastUrl().search).toBe('?include_inactive=true&limit=200')
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [], next_cursor: null, limit: 200 }))
    await LocationService.listAllBranches()
    expect(lastUrl().search).toBe('?limit=200')
  })

  it('creates without a type (the route forces it)', async () => {
    await LocationService.createBranch({ code: 'EAST', name: 'East Yard', path: 'East Yard' })
    expect(lastUrl().pathname).toBe('/api/v1/branches')
    expect(lastInit().method).toBe('POST')
    expect(lastBody()).not.toHaveProperty('type')
  })

  it('updates with PUT and the quoted revision, and archives with DELETE and the quoted revision', async () => {
    await LocationService.updateBranch('b-1', locationUpdateFromLocation(BRANCH), 7)
    expect(lastUrl().pathname).toBe('/api/v1/branches/b-1')
    expect(lastInit().method).toBe('PUT')
    expect(lastHeader('If-Match')).toBe('"7"')
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await LocationService.archiveBranch('b-1', 8)
    expect(lastInit().method).toBe('DELETE')
    expect(lastHeader('If-Match')).toBe('"8"')
  })

  it('reads the tree as the envelope and returns its rows', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [BRANCH], next_cursor: null, limit: 200 }))
    const rows = await LocationService.getBranchTree('b-1')
    expect(lastUrl().pathname).toBe('/api/v1/branches/b-1/tree')
    expect(rows).toHaveLength(1)
  })

  it('keeps the user grant routes as bare arrays', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([{ id: 'b-1', code: 'WEST', name: 'West', active: true, is_home: true }]))
    const mine = await LocationService.getMyBranches()
    expect(lastUrl().pathname).toBe('/api/v1/me/branches')
    expect(mine).toHaveLength(1)
  })
})

describe('errors', () => {
  it('throws an ApiError, with stale_revision recognised', async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(409, 'stale_revision', 'the branch changed since it was loaded'))
    const err = await LocationService.updateBranch('b-1', {}, 1).catch(e => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.isStaleRevision).toBe(true)
  })

  it('carries field details of a 400, such as a type sent on a PUT', async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(400, 'validation_failed', 'bad body', [{ field: 'type', message: 'is not mutable' }]))
    const err = await LocationService.updateLocation('l-1', {}, 1).catch(e => e)
    expect(err.details[0].field).toBe('type')
  })

  it('a 428 for a missing revision is an ApiError too', async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(428, 'precondition_required', 'send If-Match'))
    const err = await LocationService.deleteLocation('l-1', 1).catch(e => e)
    expect(err.status).toBe(428)
  })
})
