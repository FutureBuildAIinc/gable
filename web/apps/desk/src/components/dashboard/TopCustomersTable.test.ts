// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dashboard's top customers: each row opens that customer's account
 * and is a real link named for a screen reader.
 */
import { describe, it, expect } from 'vitest'
import './TopCustomersTable.ts'
import type { GableTopCustomersTable } from './TopCustomersTable.ts'
import { mount, q, text } from '../../test/dom'

describe('top customers table', () => {
  it("links each row to the customer's account, named for a screen reader", async () => {
    const el = await mount<GableTopCustomersTable>('gable-top-customers-table', {
      customers: [
        { customer_id: 'cust-1', customer_name: 'Kelbrook Construction', total_revenue: 186600, order_count: 4 },
      ],
    })

    const link = q<HTMLAnchorElement>(el, 'a[href="/accounts/cust-1"]')
    expect(link.getAttribute('aria-label')).toBe("Open Kelbrook Construction's account")
    expect(text(el)).toContain('Kelbrook Construction')
  })
})
