// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dashboard's top customers: each row opens that customer's account.
 * The row's link is the keyboard path; a click anywhere on the row
 * follows it.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { router } from '../../lib/router.ts'
import './TopCustomersTable.ts'
import type { GableTopCustomersTable } from './TopCustomersTable.ts'
import { mount, q, text, flush } from '../../test/dom'

const CUSTOMER = { customer_id: 'cust-1', customer_name: 'Kelbrook Construction', total_revenue: 186600, order_count: 4 }

afterEach(() => {
  vi.restoreAllMocks()
})

describe('top customers table', () => {
  it("links each row to the customer's account, named for a screen reader", async () => {
    const el = await mount<GableTopCustomersTable>('gable-top-customers-table', {
      customers: [CUSTOMER],
    })

    const link = q<HTMLAnchorElement>(el, 'a[href="/accounts/cust-1"]')
    expect(link.getAttribute('aria-label')).toBe("Open Kelbrook Construction's account")
    expect(text(el)).toContain('Kelbrook Construction')
  })

  it('a click anywhere on the row follows the row\'s link', async () => {
    history.replaceState(null, '', '/home')
    router.init([
      { path: '/accounts/:id', load: async () => {}, layout: 'erp' },
      { path: '/home', load: async () => {}, layout: 'erp' },
    ])
    const el = await mount<GableTopCustomersTable>('gable-top-customers-table', {
      customers: [CUSTOMER],
    })

    // The revenue cell is not inside the link.
    const revenueCell = Array.from(el.querySelectorAll('td')).find((td) => text(td).includes('$1,866.00')) as HTMLElement
    expect(revenueCell, 'expected the revenue cell').toBeDefined()
    revenueCell.click()
    await flush()

    expect(window.location.pathname).toBe('/accounts/cust-1')
  })

  it('a Ctrl, Cmd or middle click on a row outside its link opens the account in a new tab', async () => {
    history.replaceState(null, '', '/home')
    router.init([
      { path: '/accounts/:id', load: async () => {}, layout: 'erp' },
      { path: '/home', load: async () => {}, layout: 'erp' },
    ])
    const el = await mount<GableTopCustomersTable>('gable-top-customers-table', {
      customers: [CUSTOMER],
    })
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)

    const revenueCell = Array.from(el.querySelectorAll('td')).find((td) => text(td).includes('$1,866.00')) as HTMLElement

    revenueCell.dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true }))
    await flush()
    expect(openSpy).toHaveBeenCalledWith('/accounts/cust-1', '_blank')
    expect(window.location.pathname, 'a modifier click does not navigate in place').toBe('/home')

    revenueCell.dispatchEvent(new MouseEvent('click', { bubbles: true, metaKey: true }))
    await flush()
    expect(openSpy).toHaveBeenCalledTimes(2)

    revenueCell.dispatchEvent(new MouseEvent('auxclick', { bubbles: true, button: 1 }))
    await flush()
    expect(openSpy).toHaveBeenCalledTimes(3)
    expect(window.location.pathname).toBe('/home')
  })
})
