// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Money on an ERP order — the int64-cents side of the boundary.
 *
 * `/api/v1/orders` serializes every amount as int64 **cents** (CLAUDE.md,
 * "Money convention is not uniform across modules"; `order/model.go` marks
 * `TotalAmount`, `PriceEach` and `UnitCost` `// Cents`). This page renders
 * eight distinct amounts plus three it derives itself, and CLAUDE.md is
 * explicit that "calling `.toFixed(2)` directly on an ERP money field will
 * render $73.88 as $7,388.00". `lib/utils.test.ts` pins the helper; this file
 * pins that the page actually reaches for it, on every amount, including the
 * derived ones.
 *
 * It is also where negative money genuinely reaches a user: a below-cost sale
 * shows a negative margin on the line, the footer and the sidebar card.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './OrderDetail'
import type { GableOrderDetail } from './OrderDetail'
import type { Order } from '../../types/order'
import { mountAsync, update, text, jsonResponse } from '../../test/dom'

/**
 * A confirmed order in cents. 24 × $32.40 = $777.60 revenue on $547.20 cost,
 * i.e. $230.40 margin (29.6%).
 */
function order(): Order {
  return {
    id: 'ord-42-0000-0000',
    customer_id: 'cus-99-0000-0000',
    customer_name: 'Ridgeview Framing',
    status: 'CONFIRMED',
    total_amount: 77_760,
    total_cost: 54_720,
    total_margin: 23_040,
    margin_percent: 29.6,
    total_commission: 1_152,
    created_at: '2026-08-11T14:00:00Z',
    updated_at: '2026-08-11T14:00:00Z',
    lines: [
      {
        id: 'line-1',
        order_id: 'ord-42-0000-0000',
        product_id: 'p-1-0000-0000',
        product_sku: '2X6-16-SPF',
        product_name: '2x6x16 SPF',
        quantity: 24,
        price_each: 3_240,
        unit_cost: 2_280,
        commission_rate: 0.05,
      },
    ],
  }
}

let fetchMock: ReturnType<typeof vi.fn>

/** Mount the page for `o`, serving it from `/api/v1/orders/:id`. */
async function orderPage(mutate: (o: Order) => void = () => {}): Promise<GableOrderDetail> {
  const o = order()
  mutate(o)
  fetchMock = vi.fn((url: string) =>
    Promise.resolve(url.includes('/orders/') ? jsonResponse(o) : jsonResponse({})),
  )
  vi.stubGlobal('fetch', fetchMock)
  return mountAsync<GableOrderDetail>('gable-order-detail', { routeId: o.id })
}

/** Cells of the line-items table body, as rendered text. */
function lineCells(el: GableOrderDetail): string[] {
  return Array.from(el.querySelectorAll('tbody tr td')).map((td) => text(td))
}

/** Cells of the grand-total footer row. */
function footerCells(el: GableOrderDetail): string[] {
  return Array.from(el.querySelectorAll('tfoot tr td')).map((td) => text(td))
}

/** The Margin & Commission sidebar card. */
function marginCard(el: GableOrderDetail): string {
  const card = Array.from(el.querySelectorAll('div')).find((d) =>
    text(d).startsWith('Margin & Commission'),
  )
  if (!card) throw new Error('no Margin & Commission card rendered')
  return text(card)
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('gable-order-detail — cents rendered as dollars', () => {
  it('renders a unit price in dollars, not as raw cents', async () => {
    const el = await orderPage()
    // 3240 cents is $32.40. The documented failure is "$3,240.00".
    expect(lineCells(el)[2]).toBe('$32.40')
    expect(lineCells(el)[2]).not.toBe('$3,240.00')
  })

  it('renders the derived line total in dollars', async () => {
    // quantity × price_each stays in cents; the /100 must happen once, at render.
    const el = await orderPage()
    expect(lineCells(el)[3]).toBe('$777.60')
  })

  it('renders the derived line cost in dollars', async () => {
    const el = await orderPage()
    expect(lineCells(el)[4]).toBe('$547.20')
  })

  it('renders the derived line margin in dollars, with its percentage', async () => {
    const el = await orderPage()
    expect(lineCells(el)[5]).toBe('$230.40 (29.6%)')
  })

  it('renders the grand total, cost and margin in dollars', async () => {
    const el = await orderPage()
    const cells = footerCells(el)

    expect(cells[1]).toBe('$777.60')
    expect(cells[2]).toBe('$547.20')
    expect(cells[3]).toBe('$230.40')
  })

  it('renders the margin card in dollars, commission included', async () => {
    const el = await orderPage()
    const body = marginCard(el)

    expect(body).toContain('Revenue $777.60')
    expect(body).toContain('Cost $547.20')
    expect(body).toContain('Margin $230.40 (29.6%)')
    expect(body).toContain('Commission $11.52')
  })

  it('groups thousands on a six-figure order', async () => {
    // A truckload of engineered lumber is routinely $100k+; the separator is
    // the difference between $123,456.78 and a misread $12,345,678.
    const el = await orderPage((o) => {
      o.total_amount = 12_345_678
      o.lines![0].price_each = 514_403
      o.lines![0].quantity = 24
    })

    expect(footerCells(el)[1]).toBe('$123,456.78')
    expect(lineCells(el)[2]).toBe('$5,144.03')
  })

  it('renders a zero-cost line as $0.00 rather than a blank', async () => {
    const el = await orderPage((o) => {
      o.lines![0].unit_cost = 0
      o.total_cost = 0
    })

    expect(lineCells(el)[4]).toBe('$0.00')
    expect(footerCells(el)[2]).toBe('$0.00')
  })

  it('renders a sub-dollar unit price without losing the cents', async () => {
    // Fasteners and small hardware genuinely price under a dollar.
    const el = await orderPage((o) => {
      o.lines![0].price_each = 7
      o.lines![0].quantity = 1000
      o.total_amount = 7_000
    })

    expect(lineCells(el)[2]).toBe('$0.07')
    expect(lineCells(el)[3]).toBe('$70.00')
  })
})

describe('gable-order-detail — a below-cost sale', () => {
  /** Sold at $700.00 against $850.00 of cost: a $150.00 loss. */
  const belowCost = (o: Order) => {
    o.total_amount = 70_000
    o.total_cost = 85_000
    o.total_margin = -15_000
    o.margin_percent = -21.4
    o.lines![0].price_each = 2_916
    o.lines![0].unit_cost = 3_541
    o.lines![0].quantity = 24
  }

  it('gets the magnitude of a negative margin right', async () => {
    const el = await orderPage(belowCost)
    expect(footerCells(el)[3]).toContain('150.00')
  })

  it('flags a loss-making line in red', async () => {
    const el = await orderPage(belowCost)
    const marginCell = Array.from(el.querySelectorAll('tbody tr td')).at(-1)!
    expect(marginCell.className).toContain('text-red-400')
  })

  it('reports the negative margin percentage', async () => {
    const el = await orderPage(belowCost)
    expect(marginCard(el)).toContain('(-21.4%)')
  })

  // Regression: `formatCents()` used to string-concatenate a "$" in front of
  // `Number#toLocaleString()`, so every negative amount rendered "$-150.00"
  // instead of the en-US convention "-$150.00". This page is the live path: a
  // below-cost sale renders it three times — the line margin cell, the
  // grand-total footer and the Margin & Commission card — on the screen a sales
  // manager uses to decide whether to approve the order.
  // (Pinned once at the helper in lib/utils.test.ts; pinned here too because
  // this is where it reaches a user.)
  it('renders a loss as -$150.00, not $-150.00', async () => {
    const el = await orderPage(belowCost)
    expect(footerCells(el)[3]).toBe('-$150.00')
  })

  it('renders the loss the same way on the line and in the margin card', async () => {
    // All three renders of the same negative amount agree, so nothing on the
    // approval screen still shows the sign inside the symbol.
    const el = await orderPage(belowCost)
    const marginCell = Array.from(el.querySelectorAll('tbody tr td')).at(-1)!
    expect(text(marginCell)).toBe('-$150.00 (-21.4%)')
    expect(marginCard(el)).toContain('-$150.00 (-21.4%)')
    expect(text(el)).not.toContain('$-150.00')
  })
})

describe('gable-order-detail — missing and malformed amounts', () => {
  it('renders $0.00 rather than $NaN when the API omits an amount', async () => {
    // The Go zero value for an omitted int64 is 0, but a partially-populated
    // row (or a field renamed on the backend) arrives as undefined. "$NaN" on
    // an order screen is worse than a wrong number: it looks like a crash.
    const el = await orderPage((o) => {
      (o as unknown as Record<string, unknown>).total_commission = undefined
    })

    expect(marginCard(el)).toContain('Commission $0.00')
    expect(text(el)).not.toContain('NaN')
  })

  it('renders an order with no lines without throwing', async () => {
    const el = await orderPage((o) => {
      o.lines = []
    })

    expect(el.querySelectorAll('tbody tr')).toHaveLength(0)
    expect(footerCells(el)[1]).toBe('$777.60')
  })
})

describe('gable-order-detail — loading and failure', () => {
  it('says it is loading before the order arrives', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))
    const el = await mountAsync<GableOrderDetail>('gable-order-detail', { routeId: 'ord-42' })

    expect(text(el)).toBe('Loading order details...')
  })

  it('reports a failed load instead of rendering an empty order', async () => {
    // Rendering $0.00 totals for an order that failed to load would look like
    // a real, free order.
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse({}, 500))))
    const el = await mountAsync<GableOrderDetail>('gable-order-detail', { routeId: 'ord-42' })

    expect(text(el)).toContain('Failed to load order')
    expect(text(el)).not.toContain('$0.00')
  })

  it('reloads when the route id changes', async () => {
    const el = await orderPage()
    const before = fetchMock.mock.calls.length

    await update(el, { routeId: 'ord-43-0000-0000' })

    expect(fetchMock.mock.calls.length).toBeGreaterThan(before)
    expect(String(fetchMock.mock.calls.at(-1)![0])).toContain('ord-43-0000-0000')
  })
})
