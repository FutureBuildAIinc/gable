// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The purchase order list: the desk's first view of a PO. Three things must
 * be readable without clicking into a row: the vendor, the yard, and the total
 * in dollars with thousands separators. The list used to print a UUID for the
 * PO id and a bare cents number for the total: both were noise.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './PurchaseOrderList'
import type { PurchaseOrderList } from './PurchaseOrderList'
import { branchContext } from '../../services/BranchContext'
import { mountAsync, text as textOf } from '../../test/dom'

const PO_1_ID = '00000000-0000-4000-8000-0000000000a1'
const PO_2_ID = '00000000-0000-4000-8000-0000000000a2'
const MAIN_BRANCH_ID = '00000000-0000-4000-8000-0000000000b1'
const VENDOR_1_ID = '00000000-0000-4000-8000-0000000000v1'
const VENDOR_2_ID = '00000000-0000-4000-8000-0000000000v2'
// The list route carries only vendor_id; the page names the vendor from the vendor list.
const VENDORS = [
  { id: VENDOR_1_ID, name: 'Acme Lumber' },
  { id: VENDOR_2_ID, name: 'Stone Yard Co' },
]

function po(overrides: Record<string, unknown> = {}) {
  return {
    id: PO_1_ID,
    branch_id: MAIN_BRANCH_ID,
    vendor_id: VENDOR_1_ID,
    status: 'DRAFT',
    source: 'MANUAL',
    created_at: '2031-03-09T08:00:00Z',
    updated_at: '2031-03-09T08:00:00Z',
    line_count: 4,
    total_cost: 123456.78,
    ...overrides,
  }
}

beforeEach(() => {
  ;(branchContext as unknown as { _branches: unknown[] })._branches = [
    { id: MAIN_BRANCH_ID, code: 'MAIN', name: 'Main Yard', active: true, is_home: true },
  ]
})

afterEach(() => {
  vi.unstubAllGlobals()
  ;(branchContext as unknown as { _branches: unknown[] })._branches = []
})

function stubAll(poList: unknown[]) {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.includes('/reorder-alerts')) return jsonResponse([])
    if (url.includes('/source-summary')) return jsonResponse({})
    if (url.includes('/purchase-orders')) return jsonResponse(poList)
    if (url.includes('/vendors')) return jsonResponse(VENDORS)
    return jsonResponse([])
  }))
}

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

describe('purchase order list columns', () => {
  it('shows the vendor name as its own column, resolved from vendor_id', async () => {
    stubAll([po({ id: PO_1_ID, vendor_id: VENDOR_1_ID })])
    const el = await mountAsync<PurchaseOrderList>('gable-purchase-order-list')
    expect(textOf(el)).toContain('Acme Lumber')
  })

  it('shows the yard name as its own column (resolved from branch_id)', async () => {
    stubAll([po({ id: PO_1_ID, branch_id: MAIN_BRANCH_ID })])
    const el = await mountAsync<PurchaseOrderList>('gable-purchase-order-list')
    expect(textOf(el)).toContain('Main Yard')
  })

  it('groups thousands on the total cost and never prints a bare integer', async () => {
    stubAll([po({ id: PO_1_ID, total_cost: 123456.78 })])
    const el = await mountAsync<PurchaseOrderList>('gable-purchase-order-list')
    expect(textOf(el)).toContain('$123,456.78')
    expect(textOf(el)).not.toContain('$123456.78')
  })

  it('renders two rows independently with their own vendor and yard', async () => {
    stubAll([
      po({ id: PO_1_ID, vendor_id: VENDOR_1_ID, total_cost: 1000 }),
      po({ id: PO_2_ID, vendor_id: VENDOR_2_ID, branch_id: MAIN_BRANCH_ID, total_cost: 2000 }),
    ])
    const el = await mountAsync<PurchaseOrderList>('gable-purchase-order-list')
    expect(textOf(el)).toContain('Acme Lumber')
    expect(textOf(el)).toContain('Stone Yard Co')
  })

  it('reads "Not set" when the vendor is not in the vendor list', async () => {
    stubAll([po({ id: PO_1_ID, vendor_id: '00000000-0000-4000-8000-0000000000v9' })])
    const el = await mountAsync<PurchaseOrderList>('gable-purchase-order-list')
    expect(textOf(el)).toContain('Not set')
  })
})