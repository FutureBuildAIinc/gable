// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { GableProOrder } from './order.ts'
import { OFFLINE_WRITE_REASON } from '../lib/offline.ts'
import { iso, meFixture, orderFixture, testIds } from '../test/fixtures.ts'
import type {
  PortalCart,
  PortalCatalogDetail,
  PortalCatalogProduct,
  PortalCategoryNode,
  PortalProcurementProject,
  PortalQuote,
  PortalReorderPreview,
} from '../types.ts'

const catalogProductFixture: PortalCatalogProduct = {
  id: testIds.product,
  sku: 'LUM-248',
  name: '2x4x8 SPF Premium Stud',
  category: 'Dimensional Lumber',
  species: 'SPF',
  grade: '#2 & Better',
  image_url: '',
  uom: 'EA',
  base_price: 5.5,
  customer_price: 4.88,
  price_source: 'CONTRACT',
  available: 640,
  in_stock: true,
  lead_time_days: 3,
  category_id: '10000000-0000-4000-8000-000000000001',
  category_slug: 'dimensional-lumber',
  category_path: 'lumber.dimensional',
  availability: [
    { yard_id: testIds.yardA, yard_code: 'MAIN', yard_name: 'Main Yard', on_hand: 450, available: 420, in_stock: true, lead_time_days: 1 },
    { yard_id: testIds.yardB, yard_code: 'NORTH', yard_name: 'North Yard', on_hand: 230, available: 220, in_stock: true, lead_time_days: 2 },
  ],
}

const catalogDetailFixture: PortalCatalogDetail = {
  ...catalogProductFixture,
  weight_lbs: 9.2,
  upc: '012345678905',
  vendor: 'Cascade Forest',
  volume_breaks: [
    { min_quantity: 100, unit_price: 4.55, price_source: 'TIER_100', details: 'Unit bunk quantity', saves_per_unit: 0.33 },
  ],
}

const categoriesFixture: PortalCategoryNode[] = [
  {
    id: '10000000-0000-4000-8000-000000000001',
    name: 'Dimensional Lumber',
    slug: 'dimensional-lumber',
    path: 'lumber.dimensional',
    depth: 1,
    sort_order: 1,
    product_count: 14,
    children: [],
  },
]

const cartFixture: PortalCart = {
  id: '20000000-0000-4000-8000-000000000002',
  item_count: 48,
  subtotal: 234.24,
  items: [
    {
      id: '30000000-0000-4000-8000-000000000003',
      product_id: testIds.product,
      product_sku: 'LUM-248',
      product_name: '2x4x8 SPF Premium Stud',
      image_url: '',
      quantity: 48,
      unit_price: 4.88,
      line_total: 234.24,
      available: 640,
    },
  ],
}

const reorderPreviewFixture: PortalReorderPreview = {
  source_order_id: testIds.order,
  project_id: null,
  original_total_cents: 23424,
  current_total_cents: 24720,
  total_diff_cents: 1296,
  lines: [
    {
      line_id: '40000000-0000-4000-8000-000000000004',
      product_id: testIds.product,
      sku: 'LUM-248',
      description: '2x4x8 SPF Premium Stud',
      quantity: 48,
      uom: 'EA',
      original_unit_price_cents: 488,
      current_unit_price_cents: 515,
      price_diff_cents: 27,
      original_line_total_cents: 23424,
      current_line_total_cents: 24720,
      line_diff_cents: 1296,
      price_source: 'CONTRACT',
      available_qty: 640,
      in_stock: true,
    },
  ],
}

const listFixture: PortalProcurementProject = {
  id: '50000000-0000-4000-8000-000000000005',
  name: 'First Floor Wall Package',
  status: 'DRAFT',
  created_at: iso(),
  updated_at: iso(),
  lines: [
    {
      id: '51000000-0000-4000-8000-000000000005',
      project_id: '50000000-0000-4000-8000-000000000005',
      product_id: testIds.product,
      product_sku: 'LUM-248',
      product_name: '2x4x8 SPF Premium Stud',
      description: '2x4x8 SPF Premium Stud',
      qty_entered: 96,
      uom: 'EA',
      notes: '',
      unit_price_cents: 488,
      line_total_cents: 46848,
      available_qty: 640,
      in_stock: true,
      created_at: iso(),
    },
  ],
}

const dealerQuoteFixture: PortalQuote = {
  id: testIds.quote,
  status: 'PRICED',
  erp_state: 'SENT',
  project_id: null,
  project_name: 'Glenview Lofts',
  notes: 'Roofing package',
  priced: true,
  total_amount: 1420.5,
  freight_amount: 75,
  delivery_type: 'DELIVERY',
  expires_at: iso(),
  sent_at: iso(),
  accepted_at: null,
  rejected_at: null,
  created_at: iso(),
  updated_at: iso(),
  lines: [
    {
      id: '61000000-0000-4000-8000-000000000006',
      product_id: testIds.product,
      product_sku: 'LUM-248',
      description: '2x4x8 SPF Premium Stud',
      customer_note: '',
      quantity: 200,
      uom: 'EA',
      price_uom: 'EA',
      unit_price: 4.88,
      line_total: 976,
      is_special_order: false,
    },
  ],
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ETag: 'v1', 'X-Portal-Latest-Change': iso() },
  })
}

describe('Order screen', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    localStorage.clear()
    sessionStorage.clear()
    window.history.replaceState({}, '', '/order')
    vi.restoreAllMocks()
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof Request ? input.url : input.toString()
      const method = init?.method ?? 'GET'
      if (url.includes('/api/portal/v1/catalog/categories')) return jsonResponse(categoriesFixture)
      if (url.includes(`/api/portal/v1/catalog/${testIds.product}`)) return jsonResponse(catalogDetailFixture)
      if (url.includes('/api/portal/v1/catalog')) return jsonResponse([catalogProductFixture])
      if (url.includes('/api/portal/v1/cart/items')) return jsonResponse(cartFixture, 201)
      if (url.includes('/api/portal/v1/cart')) return jsonResponse(cartFixture)
      if (url.includes('/api/portal/v1/checkout')) return jsonResponse({ order_id: testIds.order, message: 'Order placed.' }, 201)
      if (url.includes(`/api/portal/v1/orders/${testIds.order}/reorder-preview`)) return jsonResponse(reorderPreviewFixture)
      if (url.includes('/api/portal/v1/orders/reorder')) return jsonResponse({ order_id: testIds.order, new_order_id: testIds.order, message: 'Reorder created.' }, 201)
      if (url.includes('/api/portal/v1/orders')) return jsonResponse([orderFixture])
      if (url.includes('/api/portal/v1/quotes/intake') && method === 'POST') {
        return jsonResponse({
          source: 'TEXT',
          synthetic: false,
          items: [
            {
              raw_text: '24 pcs 2x4x8 SPF',
              parsed_qty: 24,
              parsed_unit: 'EA',
              parsed_description: '2x4x8 SPF',
              matched_product_id: testIds.product,
              matched_product_name: '2x4x8 SPF Premium Stud',
              matched_sku: 'LUM-248',
              confidence: 'HIGH',
            },
          ],
          quote: dealerQuoteFixture,
        })
      }
      if (url.includes(`/api/portal/v1/quotes/${testIds.quote}/accept`)) return jsonResponse({ ...dealerQuoteFixture, status: 'ACCEPTED' })
      if (url.includes('/api/portal/v1/quotes')) return jsonResponse([dealerQuoteFixture])
      if (url.includes('/api/portal/v1/projects')) return jsonResponse({ items: [{ id: '70000000-0000-4000-8000-000000000007', name: 'Glenview Lofts' }], next_cursor: null })
      if (url.includes('/api/portal/v1/procurement/projects')) return jsonResponse([listFixture])
      return jsonResponse({})
    }))
  })

  it('renders catalog with account price, per-yard stock, shortest lead time, and volume break sheet', async () => {
    const element = new GableProOrder()
    element.me = structuredClone(meFixture)
    document.body.append(element)
    await vi.waitFor(() => expect(element.querySelector(`[data-testid="catalog-product-${testIds.product}"]`)).not.toBeNull())

    expect(element.querySelector(`[data-testid="product-price-${testIds.product}"]`)?.textContent).toContain('$4.88')
    expect(element.querySelector(`[data-testid="product-lead-${testIds.product}"]`)?.textContent).toContain('Shortest lead: 1d')
    expect(element.querySelector(`[data-testid="product-yard-${testIds.product}-MAIN"]`)?.textContent).toContain('MAIN: 420 EA (1d)')

    element.querySelector<HTMLButtonElement>(`[data-testid="inspect-product-${testIds.product}"]`)?.click()
    await vi.waitFor(() => expect(element.querySelector('[data-testid="product-detail-sheet"]')).not.toBeNull())
    expect(element.querySelector('[data-testid="product-volume-breaks"]')?.textContent).toContain('$4.55')
  })

  it('previews reorder price differences in cents and confirms reorder', async () => {
    const element = new GableProOrder()
    element.me = structuredClone(meFixture)
    document.body.append(element)
    await vi.waitFor(() => expect(element.querySelector('[data-testid="order-tab-reorder"]')).not.toBeNull())

    element.querySelector<HTMLButtonElement>('[data-testid="order-tab-reorder"]')?.click()
    await element.updateComplete

    element.querySelector<HTMLButtonElement>(`[data-testid="reorder-preview-btn-${testIds.order}"]`)?.click()
    await vi.waitFor(() => expect(element.querySelector('[data-testid="reorder-preview-card"]')).not.toBeNull())

    expect(element.querySelector('[data-testid="reorder-original-total"]')?.textContent).toBe('$234.24')
    expect(element.querySelector('[data-testid="reorder-current-total"]')?.textContent).toBe('$247.20')
    expect(element.textContent).toContain('+$12.96')

    element.querySelector<HTMLButtonElement>('[data-testid="confirm-reorder-btn"]')?.click()
    await vi.waitFor(() => expect(element.textContent).toContain('Reorder created.'))
  })

  it('parses pasted quote intake lines and gates quote approval by capability', async () => {
    const me = structuredClone(meFixture)
    me.capabilities.approve_quotes = false
    const element = new GableProOrder()
    element.me = me
    document.body.append(element)
    await vi.waitFor(() => expect(element.querySelector('[data-testid="order-tab-requests"]')).not.toBeNull())

    element.querySelector<HTMLButtonElement>('[data-testid="order-tab-requests"]')?.click()
    await element.updateComplete

    const acceptBtn = element.querySelector<HTMLButtonElement>(`[data-testid="accept-dealer-quote-${testIds.quote}"]`)
    expect(acceptBtn?.disabled).toBe(true)

    const textarea = element.querySelector<HTMLTextAreaElement>('[data-testid="intake-textarea"]')!
    textarea.value = '24 pcs 2x4x8 SPF'
    textarea.dispatchEvent(new Event('input'))
    await element.updateComplete

    element.querySelector<HTMLButtonElement>('[data-testid="intake-parse-btn"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('[data-testid="intake-parsed-list"]')).not.toBeNull())
    expect(element.querySelector('[data-testid="intake-parsed-list"]')?.textContent).toContain('Matched LUM-248')
  })

  it('disables writes offline and displays the plain offline write reason', async () => {
    const element = new GableProOrder()
    element.me = structuredClone(meFixture)
    element.offline = true
    document.body.append(element)
    await vi.waitFor(() => expect(element.querySelector('[data-testid="order-offline-banner"]')).not.toBeNull())

    expect(element.querySelector('[data-testid="order-offline-banner"]')?.textContent).toBe(OFFLINE_WRITE_REASON)
    expect(element.querySelector<HTMLButtonElement>(`[data-testid="cart-add-${testIds.product}"]`)?.disabled).toBe(true)
    expect(element.querySelector<HTMLButtonElement>('[data-testid="checkout-submit-btn"]')?.disabled).toBe(true)
  })
})
