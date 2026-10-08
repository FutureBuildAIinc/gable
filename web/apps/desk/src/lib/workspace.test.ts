// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Workspace zone resolution — the ERP shell decides which app tab is open and
 * which menu item is lit purely from the current path. Both are longest-prefix
 * problems, and both have near-miss neighbours in the real zone table
 * (/purchasing vs /purchasing/vendors, /reports vs /reports/daily-till,
 * /admin vs /admin/branches) that a naive `startsWith` would get wrong.
 */
import { describe, it, expect } from 'vitest'
import { zoneForPath, menuForKey, activeMenuPath, type ZoneMenuItem } from './workspace'

describe('zoneForPath', () => {
  it('resolves an exact zone prefix', () => {
    expect(zoneForPath('/orders')?.key).toBe('order')
    expect(zoneForPath('/inventory')?.key).toBe('inventory')
  })

  it('resolves a nested path to its owning zone', () => {
    expect(zoneForPath('/orders/ord-42')?.key).toBe('order')
    expect(zoneForPath('/accounting/journal-entries')?.key).toBe('gl')
  })

  it('prefers the longest matching prefix', () => {
    // /purchasing/vendors is its own zone even though /purchasing also matches.
    expect(zoneForPath('/purchasing/vendors')?.key).toBe('vendor')
    expect(zoneForPath('/purchasing/vendors/v-1')?.key).toBe('vendor')
    expect(zoneForPath('/purchasing')?.key).toBe('purchase_order')
    expect(zoneForPath('/purchasing/new')?.key).toBe('purchase_order')
  })

  it('splits /reports between the Daily Till zone and the Reporting zone', () => {
    expect(zoneForPath('/reports/daily-till')?.key).toBe('pos')
    expect(zoneForPath('/reports/ar-aging')?.key).toBe('reporting')
  })

  it('splits /admin between Branches and Tech Admin', () => {
    expect(zoneForPath('/admin/branches')?.key).toBe('location')
    expect(zoneForPath('/admin/branches/b-1/users')?.key).toBe('location')
    expect(zoneForPath('/admin/apps')?.key).toBe('techadmin')
  })

  it('does not treat a prefix as a match on a segment boundary violation', () => {
    // /ordersomething must not resolve to the /orders zone.
    expect(zoneForPath('/ordersomething')).toBeNull()
    expect(zoneForPath('/inventory-report')).toBeNull()
  })

  it('returns null for a path outside every zone', () => {
    expect(zoneForPath('/portal/catalog')).toBeNull()
    expect(zoneForPath('/')).toBeNull()
    expect(zoneForPath('/totally-unknown')).toBeNull()
  })

  it('resolves zones contributed by converted app manifests', () => {
    const zone = zoneForPath('/millwork/configurator')
    expect(zone?.key).toBe('millwork')
    expect(zone?.label).toBe('Millwork')
  })

  it('carries a label and icon for every zone it returns', () => {
    const zone = zoneForPath('/invoices')
    expect(zone?.label).toBe('Invoicing')
    expect(zone?.icon).toBeDefined()
  })
})

describe('menuForKey', () => {
  it('returns the app menu for a zone that has one', () => {
    const menu = menuForKey('gl')
    expect(menu.map((m) => m.path)).toEqual([
      '/accounting/chart-of-accounts',
      '/accounting/journal-entries',
      '/accounting/trial-balance',
      '/accounting/profit-and-loss',
      '/accounting/balance-sheet',
      '/accounting/accounts-payable',
    ])
  })

  it('returns an empty menu for a zone without one (Home)', () => {
    expect(menuForKey('home')).toEqual([])
  })

  it('returns an empty menu for an unknown key rather than throwing', () => {
    expect(menuForKey('not-a-zone')).toEqual([])
  })

  it('builds the converted app menu from its manifest nav order', () => {
    // millwork.ts declares configure(10) before configurator(20) in source order
    // but assigns them orders 20 and 10 — the menu must follow `order`, not source.
    expect(menuForKey('millwork').map((m) => m.label)).toEqual([
      'Product Configurator',
      'Door Configurator',
      'Blueprint Verifier',
    ])
  })
})

describe('activeMenuPath', () => {
  const menu: ZoneMenuItem[] = [
    { label: 'Quotes', path: '/quotes' },
    { label: 'Quote Builder', path: '/quotes/new' },
    { label: 'Analytics', path: '/quotes/analytics' },
  ]

  it('lights the exact item', () => {
    expect(activeMenuPath(menu, '/quotes/analytics')).toBe('/quotes/analytics')
  })

  it('lights the longest matching item, not the first', () => {
    expect(activeMenuPath(menu, '/quotes/new')).toBe('/quotes/new')
  })

  it('keeps the parent item lit on a detail route', () => {
    expect(activeMenuPath(menu, '/quotes/q-42')).toBe('/quotes')
  })

  it('returns null when nothing in the menu matches', () => {
    expect(activeMenuPath(menu, '/orders')).toBeNull()
  })

  it('returns null for an empty menu', () => {
    expect(activeMenuPath([], '/quotes')).toBeNull()
  })

  it('respects segment boundaries', () => {
    expect(activeMenuPath(menu, '/quotes-archive')).toBeNull()
  })
})
