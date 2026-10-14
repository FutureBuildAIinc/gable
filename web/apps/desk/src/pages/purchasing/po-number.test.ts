// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Purchase orders show their document number (PO-000042), not a fragment of
 * their id. A response without one (an older core) falls back to the id.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import type { LitElement } from 'lit'
import './PurchaseOrderList'
import { poLabel } from '../../lib/utils'
import { mountAsync, text, jsonResponse } from '../../test/dom'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('poLabel', () => {
  it('prints the number', () => {
    expect(poLabel({ id: '275555db-0000-0000-0000-000000000000', number: 'PO-000042' })).toBe('PO-000042')
  })
  it('falls back to the first eight characters of the id', () => {
    expect(poLabel({ id: '275555db-0000-0000-0000-000000000000' })).toBe('275555db')
  })
})

describe('purchase order list', () => {
  it('shows the number in the PO # column', async () => {
    const po = {
      id: '275555db-0000-0000-0000-000000000000',
      number: 'PO-000042',
      status: 'DRAFT',
      source: 'MANUAL',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
      line_count: 1,
      total_cost: 10,
    }
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) =>
        Promise.resolve(jsonResponse(url.endsWith('/api/v1/purchase-orders') ? [po] : {})),
      ),
    )
    const el = await mountAsync<LitElement>('gable-purchase-order-list')
    expect(text(el)).toContain('PO-000042')
    expect(text(el)).not.toContain('275555db')
  })
})
