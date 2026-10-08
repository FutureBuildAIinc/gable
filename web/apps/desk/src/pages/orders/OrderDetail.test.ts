// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The order page on the wire contract (C2-2a): integer cents money, decimal
 * string quantities, the scaled unit price, a lowercase status, the document
 * number, and the credit hold banner a confirm can land on.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './OrderDetail'
import type { GableOrderDetail } from './OrderDetail'
import type { Order } from '../../types/order'
import { mountAsync, text, jsonResponse } from '../../test/dom'

function order(overrides: Partial<Order> = {}): Order {
  const line = {
    id: '00000000-0000-4000-8000-000000000l01',
    position: 0,
    line_type: 'product' as const,
    parent_line_id: null,
    product_id: '00000000-0000-4000-8000-0000000000p1',
    charge_code_id: null,
    charge_code: null,
    sku: 'LUM-248-PREM',
    description: '2x4x8 SPF',
    quantity: '24',
    uom: 'PCS',
    price_uom: 'PCS',
    uom_qty: '1',
    price_uom_qty: '1',
    unit_price_ten_thousandths: 324_000,
    priced_unit_price_ten_thousandths: 324_000,
    price_source: 'price_list' as const,
    override_reason: null,
    discount_percent: null,
    discount_cents: null,
    discount_reason: null,
    price_adjusted_by: null,
    line_total_cents: 77_760,
    taxable: true,
    revenue_account_code: null,
    is_special_order: false,
    vendor_id: null,
    special_order_unit_cost_ten_thousandths: null,
    quote_line_id: null,
    quantity_allocated: '24',
    quantity_backordered: '0',
    quantity_fulfilled: '0',
    created_at: '2026-08-11T14:00:00Z',
  }
  const base: Order = {
    id: '00000000-0000-4000-8000-0000000000o1',
    number: 'SO-000042',
    branch_id: '00000000-0000-4000-8000-0000000000b1',
    customer_id: '00000000-0000-4000-8000-0000000000c1',
    customer_name: 'Ridgeview Framing',
    quote_id: null,
    job_id: null,
    status: 'confirmed',
    revision: 2,
    currency: 'USD',
    delivery_type: 'pickup' as const,
    ship_to_id: null,
    customer_po: null,
    ordered_by_contact_id: null,
    salesperson_id: null,
    salesperson_name: null,
    scheduled_delivery_date: null,
    subtotal_cents: 77_760,
    tax_cents: 6_902,
    tax_rate_percent: '8.875',
    tax_exempt: false,
    tax_source: 'branch_rate' as const,
    total_cents: 84_662,
    total_cost_cents: 54_720,
    total_margin_cents: 23_040,
    margin_percent: '29.63',
    total_commission_cents: 1_152,
    hold_reason: null,
    hold_note: null,
    confirmed_at: '2026-08-11T15:00:00Z',
    created_at: '2026-08-11T14:00:00Z',
    updated_at: '2026-08-11T15:00:00Z',
    invoice_ids: [],
    ship_to: null,
    lines: [line],
    ...overrides,
  }
  return base
}

describe('gable-order-detail - the order contract', () => {
  let el: GableOrderDetail

  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(order())))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('renders the document number and the lowercase status', async () => {
    el = await mountAsync<GableOrderDetail>('gable-order-detail', { routeId: order().id })
    expect(text(el)).toContain('SO-000042')
    expect(text(el)).toContain('Confirmed')
  })

  it('renders cents money in dollars: the line and the totals', async () => {
    el = await mountAsync<GableOrderDetail>('gable-order-detail', { routeId: order().id })
    const body = text(el)
    expect(body).toContain('$777.60')
    expect(body).toContain('$69.02')
    expect(body).toContain('$846.62')
    expect(body).toContain('$547.20')
  })

  it('renders the scaled unit price and the decimal string quantity', async () => {
    el = await mountAsync<GableOrderDetail>('gable-order-detail', { routeId: order().id })
    const body = text(el)
    expect(body).toContain('$32.40')
    expect(body).toContain('24')
    expect(body).toContain('PCS')
  })

  it('renders the hold banner a confirm can land on, never an error page', async () => {
    const held = order({ status: 'on_hold', hold_reason: 'credit_limit' })
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(held)))
    el = await mountAsync<GableOrderDetail>('gable-order-detail', { routeId: held.id })
    const body = text(el)
    expect(body).toContain('On hold')
    expect(body).toContain('over their credit limit')
  })

  it('renders a line priced per another unit with its price unit beside it', async () => {
    const perMbf = order()
    perMbf.lines[0].price_uom = 'MBF'
    perMbf.lines[0].uom_qty = '187.5'
    perMbf.lines[0].price_uom_qty = '1'
    perMbf.lines[0].unit_price_ten_thousandths = 5_000_000
    perMbf.lines[0].line_total_cents = 50_000
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(perMbf)))
    el = await mountAsync<GableOrderDetail>('gable-order-detail', { routeId: perMbf.id })
    const body = text(el)
    expect(body).toContain('per MBF')
    expect(body).toContain('$500.00')
    expect(body).toContain('$500.00')
  })

  it('shows the stock side of each line and a back order with its allocate retry', async () => {
    const short = order({ status: 'backordered' })
    short.lines[0].quantity_allocated = '6'
    short.lines[0].quantity_backordered = '18'
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(short)))
    el = await mountAsync<GableOrderDetail>('gable-order-detail', { routeId: short.id })
    const body = text(el)
    expect(body).toContain('6 allocated')
    expect(body).toContain('18 back ordered')
    expect(body).toContain('Allocate Stock')
    expect(body).toContain('Fulfil Order')
  })

  it('offers the fulfilment of a confirmed order; a pickup order names who collected it', async () => {
    el = await mountAsync<GableOrderDetail>('gable-order-detail', { routeId: order().id })
    expect(text(el)).toContain('Fulfil Order')
    expect(el.querySelector('input[aria-label="Picked up by"]')).not.toBeNull()
    const delivery = order({ delivery_type: 'delivery' })
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(delivery)))
    const el2 = await mountAsync<GableOrderDetail>('gable-order-detail', { routeId: delivery.id })
    expect(text(el2)).toContain('Fulfil Order')
    expect(el2.querySelector('input[aria-label="Picked up by"]')).toBeNull()
  })

  it('fulfils through the fulfilments route on the loaded revision, with picked_up_by', async () => {
    const fulfilled = order({ status: 'fulfilled', revision: 3, invoice_ids: ['00000000-0000-4000-8000-0000000000i1'] })
    fulfilled.lines[0].quantity_allocated = '0'
    fulfilled.lines[0].quantity_fulfilled = '24'
    const calls: { url: string; init?: RequestInit }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init })
      if (url.endsWith('/fulfillments')) {
        return new Response(JSON.stringify(fulfilled), {
          status: 201,
          headers: { 'Content-Type': 'application/json', Location: '/api/v1/invoices/00000000-0000-4000-8000-0000000000i1' },
        })
      }
      return jsonResponse(order())
    }))
    el = await mountAsync<GableOrderDetail>('gable-order-detail', { routeId: order().id })
    const input = el.querySelector('input[aria-label="Picked up by"]') as HTMLInputElement
    input.value = 'Counter customer'
    input.dispatchEvent(new Event('input'))
    const button = Array.from(el.querySelectorAll('button')).find((b) => b.textContent?.includes('Fulfil Order')) as HTMLButtonElement
    button.click()
    await new Promise((r) => setTimeout(r, 20))
    const call = calls.find((c) => c.url.endsWith('/fulfillments'))
    expect(call).toBeTruthy()
    expect(JSON.parse(String(call!.init!.body))).toEqual({ revision: 2, picked_up_by: 'Counter customer' })
    expect(new Headers(call!.init!.headers).get('If-Match')).toBe('"2"')
    expect(text(el)).toContain('Fulfilled')
  })
})
