// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0

/**
 * The Kit Components tab edits a kit's definition: what the kit holds, per one
 * kit. The route replaces the whole list, so the tab sends the list the dealer
 * is editing. Its refusals (a kit inside a kit) are shown as sentences.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './ProductKitTab'
import type { GableProductKitTab } from './ProductKitTab'
import { mountAsync, update, flush, text, q } from '../../../test/dom'
import type { Product } from '../../../types/product'

const KIT_ID = '55555555-5555-4555-8555-555555555555'
const PART_ID = '66666666-6666-4666-8666-666666666666'
const OTHER_KIT_ID = '77777777-7777-4777-8777-777777777777'

let fetchMock: ReturnType<typeof vi.fn>

function product(id: string, sku: string, isKit: boolean): Product {
  return {
    id,
    sku,
    description: `${sku} description`,
    is_kit: isKit,
    stock_uom: 'PCS',
    average_unit_cost_ten_thousandths: 51000,
    revision: 1,
  } as unknown as Product
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('the Kit Components tab', () => {
  beforeEach(() => {
    fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes(`/api/v1/products/${KIT_ID}/kit-components`)) {
        if (init?.method === 'PUT') {
          const body = JSON.parse(String(init.body))
          return json({
            kit_product_id: KIT_ID,
            revision: 2,
            components: body.components.map((c: { component_product_id: string; quantity: string }) => ({
              component_product_id: c.component_product_id,
              sku: 'LUM-2492-STUD',
              description: '2x4x92-5/8 SPF Stud',
              quantity: c.quantity,
              uom: 'PCS',
              position: 0,
            })),
          })
        }
        return json({
          kit_product_id: KIT_ID,
          revision: 1,
          components: [
            {
              component_product_id: PART_ID,
              sku: 'LUM-2492-STUD',
              description: '2x4x92-5/8 SPF Stud',
              quantity: '12',
              uom: 'PCS',
              position: 0,
            },
          ],
        })
      }
      if (url.includes('/api/v1/products')) {
        return json({ items: [], next_cursor: null })
      }
      return json({})
    })
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('shows the parts the kit holds', async () => {
    const el = await mountAsync<GableProductKitTab>('gable-product-kit-tab', { productId: KIT_ID })

    expect(el.querySelectorAll('[data-testid="kit-part"]')).toHaveLength(1)
    expect(text(el.querySelector('[data-testid="kit-part"]'))).toContain('LUM-2492-STUD')
  })

  it('saves the whole list the dealer is editing', async () => {
    const el = await mountAsync<GableProductKitTab>('gable-product-kit-tab', { productId: KIT_ID })

    const select = q(el, 'gable-product-select')
    select.dispatchEvent(new CustomEvent('product-select', {
      detail: product(PART_ID, 'OSB-12', false),
      bubbles: true,
      composed: true,
    }))
    await flush()
    await update(el, {})

    q<HTMLButtonElement>(el, '[data-testid="kit-add-part"]').click()
    await flush()
    await update(el, {})

    q<HTMLButtonElement>(el, '[data-testid="kit-save"]').click()
    await flush()
    await update(el, {})

    const put = fetchMock.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'PUT')
    expect(put).toBeTruthy()
    const body = JSON.parse(String((put as unknown as [string, RequestInit])[1].body))
    expect(body.components).toEqual([{ component_product_id: PART_ID, quantity: '12' }])
  })

  it('refuses a kit inside a kit in plain words', async () => {
    const el = await mountAsync<GableProductKitTab>('gable-product-kit-tab', { productId: KIT_ID })

    const select = q(el, 'gable-product-select')
    select.dispatchEvent(new CustomEvent('product-select', {
      detail: product(OTHER_KIT_ID, 'KIT-FRAME-8', true),
      bubbles: true,
      composed: true,
    }))
    await flush()
    await update(el, {})

    const refusal = el.querySelector('[data-testid="kit-refusal"]')
    expect(refusal).not.toBeNull()
    expect(text(refusal)).toContain('cannot hold another kit')
  })
})
