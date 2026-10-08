// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Money on the portal — the float64-dollars side of the boundary.
 *
 * `/api/portal/v1/*` serializes money as float64 **dollars**
 * (`portal/model.go:73` and `:93` both carry a "TODO: align with int64 cents"),
 * which is the exact opposite of `/api/v1/orders`. CLAUDE.md spells out the
 * consequence: "Portal/quotes pages already get dollars from the API and should
 * format directly. Don't mix with ERP frontend helpers."
 *
 * That makes this page the mirror image of `orders/OrderDetail.test.ts`: there,
 * missing the `/100` renders $73.88 as $7,388.00; here, *adding* one renders
 * $7,388.07 as $73.88 — an invoice a contractor is being asked to pay, off by
 * 100x in their favour. Both directions get pinned.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './PortalOrders'
import type { PortalOrders } from './PortalOrders'
import type { PortalOrder } from '../../types/portal'
import { mountAsync, update, text, jsonResponse, clickByText } from '../../test/dom'

/** One fulfilled order: 24 × $32.40 = $777.60, in dollars on the wire. */
function orders(): PortalOrder[] {
  return [
    {
      id: 'aaaaaaaa-1111-2222-3333-444444444444',
      status: 'FULFILLED',
      total_amount: 777.6,
      created_at: '2026-08-11T14:00:00Z',
      lines: [
        {
          product_id: 'p-1',
          product_sku: '2X6-16-SPF',
          product_name: '2x6x16 SPF',
          quantity: 24,
          price_each: 32.4,
        },
      ],
    },
  ]
}

let fetchMock: ReturnType<typeof vi.fn>

async function ordersPage(mutate: (o: PortalOrder[]) => void = () => {}): Promise<PortalOrders> {
  const data = orders()
  mutate(data)
  fetchMock = vi.fn((url: string) =>
    Promise.resolve(url.includes('/portal/v1/orders') ? jsonResponse(data) : jsonResponse({})),
  )
  vi.stubGlobal('fetch', fetchMock)
  return mountAsync<PortalOrders>('gable-portal-orders')
}

/** Expand the first order card so its line table renders. */
async function expand(el: PortalOrders): Promise<void> {
  const header = el.querySelector('.cursor-pointer') ?? el.querySelector('[class*="justify-between"]')
  ;(header as HTMLElement).click()
  await update(el, {})
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('gable-portal-orders — dollars stay dollars', () => {
  it('renders the order total exactly as the portal API sent it', async () => {
    const el = await ordersPage()
    // 777.6 dollars. Dividing by 100 (the ERP helper's job) would show $7.78.
    expect(text(el)).toContain('$777.60')
    expect(text(el)).not.toContain('$7.78')
  })

  it('does not divide the docs\' $7,388.07 case by 100', async () => {
    // The value CLAUDE.md uses to illustrate the boundary, coming the other way.
    const el = await ordersPage((o) => {
      o[0].total_amount = 7388.07
    })

    expect(text(el)).toContain('$7,388.07')
    expect(text(el)).not.toContain('$73.88')
  })

  it('renders a line price and its derived total in dollars', async () => {
    const el = await ordersPage()
    await expand(el)

    const cells = Array.from(el.querySelectorAll('tbody td')).map((td) => text(td))
    expect(cells).toContain('$32.40')
    expect(cells).toContain('$777.60')
  })

  it('renders a fractional-cent total to two places', async () => {
    // float64 dollars can carry more precision than a currency can pay; the
    // portal must round for display rather than print $1,234.5670000001.
    const el = await ordersPage((o) => {
      o[0].total_amount = 1234.567
    })

    expect(text(el)).toContain('$1,234.57')
  })

  it('renders a zero-value order as $0.00', async () => {
    const el = await ordersPage((o) => {
      o[0].total_amount = 0
    })

    expect(text(el)).toContain('$0.00')
  })

  it('renders a credit as -$150.00, with the sign outside the symbol', async () => {
    // Intl.NumberFormat currency style gets this right; the ERP's hand-built
    // `formatCents()` does not (see lib/utils.test.ts). Same company, two
    // renderings of the same negative amount.
    const el = await ordersPage((o) => {
      o[0].total_amount = -150
    })

    expect(text(el)).toContain('-$150.00')
    expect(text(el)).not.toContain('$-150.00')
  })

  it('groups thousands on a large order', async () => {
    const el = await ordersPage((o) => {
      o[0].total_amount = 123456.78
    })

    expect(text(el)).toContain('$123,456.78')
  })
})

describe('gable-portal-orders — listing behaviour', () => {
  it('shows the order id, date and item count', async () => {
    const el = await ordersPage()
    const body = text(el)

    expect(body).toContain('AAAAAAAA') // first 8 of the uuid, uppercased
    expect(body).toContain('1 item')
    expect(body).toContain('FULFILLED')
  })

  it('pluralises the item count', async () => {
    const el = await ordersPage((o) => {
      o[0].lines.push({ ...o[0].lines[0], product_id: 'p-2', product_sku: 'LVL-11875' })
    })

    expect(text(el)).toContain('2 items')
  })

  it('invites a first order when there are none', async () => {
    const el = await ordersPage((o) => {
      o.length = 0
    })

    expect(el.querySelectorAll('tbody tr')).toHaveLength(0)
    expect(text(el)).not.toContain('$')
  })

  it('surfaces a load failure rather than an empty order history', async () => {
    // "No orders yet" for a contractor with 200 orders is a support call.
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse({}, 500))))
    const el = await mountAsync<PortalOrders>('gable-portal-orders')

    expect(text(el)).toContain('500')
  })

  it('collapses the line table again on a second click', async () => {
    const el = await ordersPage()
    await expand(el)
    expect(el.querySelector('tbody')).not.toBeNull()

    await expand(el)
    expect(el.querySelector('tbody')).toBeNull()
  })
})

describe('gable-portal-orders — reorder', () => {
  it('POSTs a reorder for the order it was clicked on and refreshes', async () => {
    const data = orders()
    const calls: { method: string; url: string; body?: unknown }[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init: RequestInit = {}) => {
        calls.push({
          method: init.method ?? 'GET',
          url,
          body: init.body ? JSON.parse(init.body as string) : undefined,
        })
        if (url.includes('/reorder')) {
          return Promise.resolve(jsonResponse({ order_id: 'bbbbbbbb-0000', message: 'ok' }))
        }
        return Promise.resolve(jsonResponse(data))
      }),
    )
    const el = await mountAsync<PortalOrders>('gable-portal-orders')

    await clickByText(el, 'button', 'Buy Again')

    // The order id travels in the body, not the path.
    expect(calls).toContainEqual({
      method: 'POST',
      url: '/api/portal/v1/orders/reorder',
      body: { order_id: 'aaaaaaaa-1111-2222-3333-444444444444' },
    })
    // The list reloads so the new draft appears.
    expect(calls.filter((c) => c.method === 'GET').length).toBeGreaterThan(1)
  })

  it('reports a rejected reorder without wiping the order history', async () => {
    const data = orders()
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) =>
        url.includes('/reorder')
          ? Promise.resolve(jsonResponse({ error: 'over credit limit' }, 403))
          : Promise.resolve(jsonResponse(data)),
      ),
    )
    const el = await mountAsync<PortalOrders>('gable-portal-orders')

    await clickByText(el, 'button', 'Buy Again')

    expect(text(el)).toContain('$777.60')
  })
})
