// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The price read (ADR 0006 section 7.3): a decimal string quantity, the scaled unit price, a
 * lowercase price_basis, and a 400 for a quantity that is not a positive decimal.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { PricingService } from './pricing.service'
import { ApiError } from './apiError'

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

beforeEach(() => {
  fetchMock = vi.fn(async () => jsonResponse({}))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const PRICE = {
  customer_id: 'c-1',
  product_id: 'p-1',
  quantity: '1',
  uom: 'PCS',
  unit_price_ten_thousandths: 42500,
  price_uom: 'PCS',
  uom_qty: '1',
  price_uom_qty: '1',
  line_total_cents: 425,
  price_basis: 'tier',
  details: 'Gold Tier (15%)',
}

describe('calculatePrice', () => {
  it('reads GET /pricing/calculate with the customer and product, and no quantity unless given', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(PRICE))
    const price = await PricingService.calculatePrice('c-1', 'p-1')
    expect(lastUrl().pathname).toBe('/api/v1/pricing/calculate')
    expect(Object.fromEntries(lastUrl().searchParams)).toEqual({ customer_id: 'c-1', product_id: 'p-1' })
    expect(price.unit_price_ten_thousandths).toBe(42500)
    expect(price.price_basis).toBe('tier')
  })

  it('sends the quantity as the decimal string it is, and the job', async () => {
    await PricingService.calculatePrice('c-1', 'p-1', '12.5', 'j-1')
    expect(Object.fromEntries(lastUrl().searchParams)).toEqual({ customer_id: 'c-1', product_id: 'p-1', quantity: '12.5', job_id: 'j-1' })
  })

  it('refuses a quantity that is not a positive decimal before any request', async () => {
    for (const bad of ['0', '0.0000', '-1', 'abc', '']) {
      await expect(PricingService.calculatePrice('c-1', 'p-1', bad)).rejects.toThrow(/positive/)
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('throws an ApiError with the server message on a 400', async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(400, 'validation_failed', 'quantity is not a decimal', [{ field: 'quantity', message: 'must be a decimal string' }]))
    const err = await PricingService.calculatePrice('c-1', 'p-1', '1').catch(e => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.details[0].field).toBe('quantity')
  })
})
