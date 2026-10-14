// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The purchase order detail page: three things it must show on a demo:
 *
 *  1. The vendor's name, resolved from `vendor_id` (the wire carries only the id).
 *  2. The PO's yard, as a branch name resolved client-side from
 *     `branch_id` against `BranchContext.branches`.
 *  3. Each line's SKU in place of the UUID fragment the old code put in
 *     parentheses.
 *
 * The receive form must only show locations of the PO's branch: the
 * `branch_id` on `Location` is the link. Top-level "branch" locations
 * (their own `type === 'branch'`) must be excluded; a stray top-level location
 * means a user could receive into another yard by accident. Locations of a
 * different branch than the PO must not appear in the dropdown.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './PurchaseOrderDetail'
import type { PurchaseOrderDetail } from './PurchaseOrderDetail'
import { branchContext } from '../../services/BranchContext'
import { mountAsync, q, text as textOf, flush, jsonResponse } from '../../test/dom'
import type { GableSelect } from '../../components/ui/gable-select.ts'

const PO_ID = '00000000-0000-4000-8000-0000000000a1'
const PO_BRANCH_ID = '00000000-0000-4000-8000-0000000000b1'
const OTHER_BRANCH_ID = '00000000-0000-4000-8000-0000000000b2'
const PRODUCT_ID = '00000000-0000-4000-8000-0000000000p1'

function po(overrides: Record<string, unknown> = {}) {
  return {
    id: PO_ID,
    branch_id: PO_BRANCH_ID,
    vendor_id: 'v-1',
    status: 'SENT',
    source: 'MANUAL',
    created_at: '2031-03-09T08:00:00Z',
    updated_at: '2031-03-09T08:00:00Z',
    total_cost: 1234.56,
    lines: [
      {
        id: 'l-1',
        po_id: PO_ID,
        product_id: PRODUCT_ID,
        description: '2x4x8 SPF Premium',
        quantity: 50,
        qty_received: 0,
        cost: 4.5,
      },
    ],
    ...overrides,
  }
}

function loc(id: string, branchId: string | null, type = 'bin', path = 'A-1') {
  return {
    id,
    parentId: null,
    path,
    type,
    code: id,
    description: null,
    name: null,
    address: null,
    city: null,
    state: null,
    zip: null,
    phone: null,
    taxJurisdictionCode: null,
    defaultTaxRate: null,
    timezone: null,
    active: true,
    branch_id: branchId,
    revision: 1,
    createdAt: '2031-03-09T08:00:00Z',
    updatedAt: '2031-03-09T08:00:00Z',
  }
}

beforeEach(() => {
  // Pre-populate BranchContext so the page can look up the yard name from id.
  ;(branchContext as unknown as { _branches: unknown[] })._branches = [
    { id: PO_BRANCH_ID, code: 'MAIN', name: 'Main Yard', active: true, is_home: true },
    { id: OTHER_BRANCH_ID, code: 'NORTH', name: 'North Yard', active: true },
  ]
  ;(branchContext as unknown as { _currentId: string | null })._currentId = PO_BRANCH_ID
})

afterEach(() => {
  vi.unstubAllGlobals()
  ;(branchContext as unknown as { _branches: unknown[] })._branches = []
})

describe('purchase order detail header', () => {
  it('shows the vendor name on the detail header', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.includes(`/purchase-orders/${PO_ID}/freight`)) return jsonResponse([])
      if (url.endsWith(`/purchase-orders/${PO_ID}`)) return jsonResponse(po())
      if (url.includes('/locations')) return jsonResponse([])
      if (url.endsWith('/vendors/v-1')) return jsonResponse({ id: 'v-1', name: 'Acme Lumber' })
      if (url.endsWith(`/products/${PRODUCT_ID}`)) return jsonResponse({ id: PRODUCT_ID, sku: 'LUM-2X4-08' })
      return jsonResponse([])
    }))
    const el = await mountAsync<PurchaseOrderDetail>('gable-purchase-order-detail', { routeId: PO_ID })
    expect(textOf(el)).toContain('Acme Lumber')
  })

  it('shows the PO yard name (resolved from branch_id)', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.includes(`/purchase-orders/${PO_ID}/freight`)) return jsonResponse([])
      if (url.endsWith(`/purchase-orders/${PO_ID}`)) return jsonResponse(po())
      if (url.includes('/locations')) return jsonResponse([])
      if (url.endsWith('/vendors/v-1')) return jsonResponse({ id: 'v-1', name: 'Acme Lumber' })
      if (url.endsWith(`/products/${PRODUCT_ID}`)) return jsonResponse({ id: PRODUCT_ID, sku: 'LUM-2X4-08' })
      return jsonResponse([])
    }))
    const el = await mountAsync<PurchaseOrderDetail>('gable-purchase-order-detail', { routeId: PO_ID })
    expect(textOf(el)).toContain('Main Yard')
    expect(textOf(el)).not.toContain('North Yard')
  })

  it('shows the line SKU in place of the UUID fragment', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.includes(`/purchase-orders/${PO_ID}/freight`)) return jsonResponse([])
      if (url.endsWith(`/purchase-orders/${PO_ID}`)) return jsonResponse(po())
      if (url.includes('/locations')) return jsonResponse([])
      if (url.endsWith('/vendors/v-1')) return jsonResponse({ id: 'v-1', name: 'Acme Lumber' })
      if (url.endsWith(`/products/${PRODUCT_ID}`)) return jsonResponse({ id: PRODUCT_ID, sku: 'LUM-2X4-08' })
      return jsonResponse([])
    }))
    const el = await mountAsync<PurchaseOrderDetail>('gable-purchase-order-detail', { routeId: PO_ID })
    expect(textOf(el)).toContain('LUM-2X4-08')
    expect(textOf(el)).not.toMatch(/\(\s*00000000/)
  })
})

describe('purchase order detail receive form', () => {
  it('offers only locations of the PO branch, excluding other yards and top-level branches', async () => {
    const locations = [
      // PO branch: should appear
      loc('loc-po-1', PO_BRANCH_ID, 'bin', 'A-1'),
      loc('loc-po-2', PO_BRANCH_ID, 'shelf', 'A-2'),
      // Other yard: must NOT appear
      loc('loc-other-1', OTHER_BRANCH_ID, 'bin', 'N-1'),
      // Top-level branch of a different yard: must NOT appear
      loc('loc-branch-other', OTHER_BRANCH_ID, 'branch', 'North Yard'),
      // The PO's own yard row: on the wire a branch row's branch_id is its own
      // id, so only the type rule keeps it out
      loc(PO_BRANCH_ID, PO_BRANCH_ID, 'branch', 'Main Yard'),
      // A location tied to no yard: must NOT appear
      loc('loc-no-yard', null, 'bin', 'X-1'),
    ]
    const locationsPage = { items: locations, next_cursor: null, limit: 200 }
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.includes(`/purchase-orders/${PO_ID}/freight`)) return jsonResponse([])
      if (url.endsWith(`/purchase-orders/${PO_ID}`)) return jsonResponse(po({ status: 'SENT' }))
      if (url.includes('/locations')) return jsonResponse(locationsPage)
      if (url.endsWith('/vendors/v-1')) return jsonResponse({ id: 'v-1', name: 'Acme Lumber' })
      if (url.endsWith(`/products/${PRODUCT_ID}`)) return jsonResponse({ id: PRODUCT_ID, sku: 'LUM-2X4-08' })
      return jsonResponse([])
    }))
    const el = await mountAsync<PurchaseOrderDetail>('gable-purchase-order-detail', { routeId: PO_ID })

    // Open the receive form.
    const receiveBtn = Array.from(el.querySelectorAll('button')).find((b) => textOf(b as Element).includes('Receive Items')) as HTMLButtonElement
    expect(receiveBtn, 'expected a Receive Items button').toBeDefined()
    receiveBtn.click()
    await flush()
    await flush()
    await el.updateComplete

    const select = q<GableSelect>(el, 'gable-select')
    const optionValues = select.options.map((o) => o.value).filter((v) => v !== '')
    expect(optionValues).toContain('loc-po-1')
    expect(optionValues).toContain('loc-po-2')
    expect(optionValues).not.toContain('loc-other-1')
    expect(optionValues).not.toContain('loc-branch-other')
    expect(optionValues).not.toContain(PO_BRANCH_ID)
    expect(optionValues).not.toContain('loc-no-yard')
  })

  it('shows a line total whose fraction rounds up as the next dollar', async () => {
    const line = { id: 'l-1', po_id: PO_ID, product_id: PRODUCT_ID, description: '2x4x8 SPF Premium', quantity: 1.0009, qty_received: 0, cost: 9.99 }
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.includes(`/purchase-orders/${PO_ID}/freight`)) return jsonResponse([])
      if (url.endsWith(`/purchase-orders/${PO_ID}`)) return jsonResponse(po({ lines: [line] }))
      if (url.includes('/locations')) return jsonResponse([])
      if (url.endsWith('/vendors/v-1')) return jsonResponse({ id: 'v-1', name: 'Acme Lumber' })
      if (url.endsWith(`/products/${PRODUCT_ID}`)) return jsonResponse({ id: PRODUCT_ID, sku: 'LUM-2X4-08' })
      return jsonResponse([])
    }))
    const el = await mountAsync<PurchaseOrderDetail>('gable-purchase-order-detail', { routeId: PO_ID })
    expect(textOf(el)).toContain('$10.00')
    expect(textOf(el)).not.toContain('$9.00')
  })
})