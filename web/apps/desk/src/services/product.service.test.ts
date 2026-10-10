// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The product wire (ADR 0001, ADR 0006 sections 6 and 7.1): cursor lists, a quoted If-Match on every
 * PATCH, integer ten thousandths prices and the one error envelope. A renamed parameter or a dropped
 * If-Match is a 400 or 428 at runtime, not a compile error, so these tests pin the exact URLs,
 * query strings, headers and bodies.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  LIST_ALL_PRODUCTS_CAP,
  ProductService,
  buildProductQuery,
  matchProducts,
} from './product.service'
import { ApiError } from './apiError'
import type { Product } from '../types/product'

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

const PRODUCT: Product = {
  id: 'p-1',
  sku: 'SPF-2x4x8',
  description: '2x4x8 SPF Stud',
  stock_uom: 'PCS',
  base_price_ten_thousandths: 42500,
  average_unit_cost_ten_thousandths: 31000,
  target_margin: 30,
  commission_rate: 5,
  vendor: 'North Mill',
  vendor_id: null,
  upc: '012345678905',
  weight_lbs: 8.5,
  length_in: null,
  width_in: null,
  height_in: null,
  stackable: null,
  geometry_source: null,
  lead_time_days: null,
  reorder_point: '100.0000',
  reorder_qty: '500.0000',
  on_hand: '1200.0000',
  allocated: '200.0000',
  available: '1000.0000',
  revision: 3,
  sale_uom: 'PCS',
  price_uom: 'PCS',
  purchase_uom: 'PCS',
  board_thickness_in: null,
  board_width_in: null,
  board_length_ft: null,
  random_length: false,
  units: [],
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

describe('buildProductQuery', () => {
  it('is empty with no params and sends only limit, cursor and include', () => {
    expect(buildProductQuery()).toBe('')
    const q = new URLSearchParams(buildProductQuery({ limit: 1, cursor: 'abc', includeTotal: true }).slice(1))
    expect(Object.fromEntries(q)).toEqual({ limit: '1', cursor: 'abc', include: 'total' })
  })

  it('never sends offset, and drops a null cursor', () => {
    expect(buildProductQuery({ cursor: null })).toBe('')
    expect(buildProductQuery({ limit: 200 })).not.toContain('offset')
  })
})

describe('reads', () => {
  it('lists with the query and returns the envelope', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [PRODUCT], next_cursor: 'n1', limit: 1, total: 4 }))
    const page = await ProductService.listProducts({ limit: 1, includeTotal: true })
    expect(lastUrl().pathname).toBe('/api/v1/products')
    expect(lastUrl().search).toBe('?limit=1&include=total')
    expect(page.next_cursor).toBe('n1')
    expect(page.items[0].base_price_ten_thousandths).toBe(42500)
  })

  it('listAllProducts pages by cursor until the last page', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ items: [PRODUCT], next_cursor: 'p2', limit: 200 }))
      .mockResolvedValueOnce(jsonResponse({ items: [{ ...PRODUCT, id: 'p-2' }], next_cursor: null, limit: 200 }))
    const all = await ProductService.listAllProducts()
    expect(all.map(p => p.id)).toEqual(['p-1', 'p-2'])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(new URL(fetchMock.mock.calls[0][0] as string, 'http://localhost').search).toBe('?limit=200')
    expect(new URL(fetchMock.mock.calls[1][0] as string, 'http://localhost').search).toBe('?limit=200&cursor=p2')
  })

  it('listAllProducts stops at the cap even when the server keeps offering a cursor', async () => {
    const full = Array.from({ length: 200 }, (_, i) => ({ ...PRODUCT, id: `p-${i}` }))
    fetchMock.mockImplementation(async () => jsonResponse({ items: full, next_cursor: 'more', limit: 200 }))
    const all = await ProductService.listAllProducts()
    expect(all).toHaveLength(LIST_ALL_PRODUCTS_CAP)
    expect(fetchMock).toHaveBeenCalledTimes(LIST_ALL_PRODUCTS_CAP / 200)
  })

  it('gets one product', async () => {
    await ProductService.getProduct('p-1')
    expect(lastUrl().pathname).toBe('/api/v1/products/p-1')
  })
})

describe('writes', () => {
  it('creates with POST, no If-Match, and the stock_uom and scaled price names', async () => {
    await ProductService.createProduct({ sku: 'A-1', description: 'Thing', stock_uom: 'EA', base_price_ten_thousandths: 42500 })
    expect(lastUrl().pathname).toBe('/api/v1/products')
    expect(lastInit().method).toBe('POST')
    expect(lastHeader('If-Match')).toBeUndefined()
    const body = lastBody()
    expect(body).toEqual({ sku: 'A-1', description: 'Thing', stock_uom: 'EA', base_price_ten_thousandths: 42500 })
    // The old names are unknown fields now, and an unknown field is a 400.
    for (const f of ['uom_primary', 'base_price', 'average_unit_cost', 'target_margin', 'commission_rate']) {
      expect(body).not.toHaveProperty(f)
    }
  })

  it('writes margins with PATCH and the quoted revision', async () => {
    await ProductService.updateMargins('p-1', { target_margin: 32.5, commission_rate: 4 }, 3)
    expect(lastUrl().pathname).toBe('/api/v1/products/p-1/margins')
    expect(lastInit().method).toBe('PATCH')
    expect(lastHeader('If-Match')).toBe('"3"')
    expect(lastBody()).toEqual({ target_margin: 32.5, commission_rate: 4 })
  })

  it('writes the lead time with PATCH, a null clearing it', async () => {
    await ProductService.updateLeadTime('p-1', null, 4)
    expect(lastUrl().pathname).toBe('/api/v1/products/p-1/lead-time')
    expect(lastHeader('If-Match')).toBe('"4"')
    expect(lastBody()).toEqual({ lead_time_days: null })
  })

  it('writes geometry with PATCH, every key present and null kept as null', async () => {
    await ProductService.updateDimensions('p-1', { length_in: 96, width_in: null, height_in: 0, stackable: null }, 5)
    expect(lastUrl().pathname).toBe('/api/v1/products/p-1/dimensions')
    expect(lastInit().method).toBe('PATCH')
    expect(lastHeader('If-Match')).toBe('"5"')
    expect(lastBody()).toEqual({ length_in: 96, width_in: null, height_in: 0, stackable: null, geometry_source: null })
  })

  it('answers the whole product at its new revision', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ...PRODUCT, revision: 4 }))
    const saved = await ProductService.updateMargins('p-1', { target_margin: 1, commission_rate: 1 }, 3)
    expect(saved.revision).toBe(4)
  })
})

describe('errors', () => {
  it('throws an ApiError carrying the code, message and field details', async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(400, 'validation_failed', 'one or more fields failed validation', [{ field: 'sku', message: 'is required' }]))
    const err = await ProductService.createProduct({ sku: '', description: 'x', stock_uom: 'EA' }).catch(e => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(400)
    expect(err.code).toBe('validation_failed')
    expect(err.details).toEqual([{ field: 'sku', message: 'is required', code: undefined }])
    expect(err.displayMessage).toContain('sku: is required')
  })

  it('marks a 409 stale_revision so the page can reload', async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(409, 'stale_revision', 'the product changed since it was loaded'))
    const err = await ProductService.updateMargins('p-1', { target_margin: 1, commission_rate: 1 }, 1).catch(e => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.isStaleRevision).toBe(true)
    expect(err.requestId).toBe('req-1')
  })

  it('falls back to the given message on a non-JSON failure', async () => {
    fetchMock.mockResolvedValueOnce(new Response('bad gateway', { status: 502 }))
    const err = await ProductService.getProduct('p-1').catch(e => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.message).toBe('Failed to fetch product')
  })
})

describe('matchProducts', () => {
  const list = [PRODUCT, { ...PRODUCT, id: 'p-2', sku: 'PLY-34', description: 'Plywood', vendor: null, upc: null }]

  it('returns everything for a blank query', () => {
    expect(matchProducts(list, '  ')).toHaveLength(2)
  })

  it('matches SKU, description, UPC and vendor case blind', () => {
    expect(matchProducts(list, 'ply').map(p => p.id)).toEqual(['p-2'])
    expect(matchProducts(list, 'STUD').map(p => p.id)).toEqual(['p-1'])
    expect(matchProducts(list, '0123456').map(p => p.id)).toEqual(['p-1'])
    expect(matchProducts(list, 'north mill').map(p => p.id)).toEqual(['p-1'])
    expect(matchProducts(list, 'nothing')).toEqual([])
  })
})
