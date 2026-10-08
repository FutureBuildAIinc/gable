// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Workspace zone resolution — the ERP shell decides which app tab is open and
 * which menu item is lit purely from the current path. Both are longest-prefix
 * problems, and both have near-miss neighbours in the real zone table
 * (/app/purchasing vs /app/purchasing/vendors, /app/reports vs /app/reports/daily-till,
 * /app/admin vs /app/admin/branches) that a naive `startsWith` would get wrong.
 *
 * All paths are prefixed /app/ because the desk bundle is mounted at /app/ in
 * the combined nginx layout.
 */
import { describe, it, expect } from 'vitest'
import { zoneForPath, menuForKey, activeMenuPath, type ZoneMenuItem } from './workspace'

describe('zoneForPath', () => {
  it('resolves an exact zone prefix', () => {
    expect(zoneForPath('/app/orders')?.key).toBe('order')
    expect(zoneForPath('/app/inventory')?.key).toBe('inventory')
  })

  it('resolves a nested path to its owning zone', () => {
    expect(zoneForPath('/app/orders/ord-42')?.key).toBe('order')
    expect(zoneForPath('/app/accounting/journal-entries')?.key).toBe('gl')
  })

  it('prefers the longest matching prefix', () => {
    // /app/purchasing/vendors is its own zone even though /app/purchasing also matches.
    expect(zoneForPath('/app/purchasing/vendors')?.key).toBe('vendor')
    expect(zoneForPath('/app/purchasing/vendors/v-1')?.key).toBe('vendor')
    expect(zoneForPath('/app/purchasing')?.key).toBe('purchase_order')
    expect(zoneForPath('/app/purchasing/new')?.key).toBe('purchase_order')
  })

  it('splits /app/reports between the Daily Till zone and the Reporting zone', () => {
    expect(zoneForPath('/app/reports/daily-till')?.key).toBe('pos')
    expect(zoneForPath('/app/reports/ar-aging')?.key).toBe('reporting')
  })

  it('splits /app/admin between Branches and Tech Admin', () => {
    expect(zoneForPath('/app/admin/branches')?.key).toBe('location')
    expect(zoneForPath('/app/admin/branches/b-1/users')?.key).toBe('location')
    expect(zoneForPath('/app/admin/apps')?.key).toBe('techadmin')
  })

  it('does not treat a prefix as a match on a segment boundary violation', () => {
    // /app/ordersomething must not resolve to the /app/orders zone.
    expect(zoneForPath('/app/ordersomething')).toBeNull()
    expect(zoneForPath('/app/inventory-report')).toBeNull()
  })

  it('returns null for a path outside every zone', () => {
    expect(zoneForPath('/app/portal/catalog')).toBeNull()
    expect(zoneForPath('/app/')).toBeNull()
    expect(zoneForPath('/app/totally-unknown')).toBeNull()
  })

  it('resolves zones contributed by converted app manifests', () => {
    const zone = zoneForPath('/app/millwork/configurator')
    expect(zone?.key).toBe('millwork')
    expect(zone?.label).toBe('Millwork')
  })

  it('carries a label and icon for every zone it returns', () => {
    const zone = zoneForPath('/app/invoices')
    expect(zone?.label).toBe('Invoicing')
    expect(zone?.icon).toBeDefined()
  })
})

describe('menuForKey', () => {
  it('returns the app menu for a zone that has one', () => {
    const menu = menuForKey('gl')
    expect(menu.map((m) => m.path)).toEqual([
      '/app/accounting/chart-of-accounts',
      '/app/accounting/journal-entries',
      '/app/accounting/trial-balance',
      '/app/accounting/profit-and-loss',
      '/app/accounting/balance-sheet',
      '/app/accounting/accounts-payable',
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
    { label: 'Quotes', path: '/app/quotes' },
    { label: 'Quote Builder', path: '/app/quotes/new' },
    { label: 'Analytics', path: '/app/quotes/analytics' },
  ]

  it('lights the exact item', () => {
    expect(activeMenuPath(menu, '/app/quotes/analytics')).toBe('/app/quotes/analytics')
  })

  it('lights the longest matching item, not the first', () => {
    expect(activeMenuPath(menu, '/app/quotes/new')).toBe('/app/quotes/new')
  })

  it('keeps the parent item lit on a detail route', () => {
    expect(activeMenuPath(menu, '/app/quotes/q-42')).toBe('/app/quotes')
  })

  it('returns null when nothing in the menu matches', () => {
    expect(activeMenuPath(menu, '/app/orders')).toBeNull()
  })

  it('returns null for an empty menu', () => {
    expect(activeMenuPath([], '/app/quotes')).toBeNull()
  })

  it('respects segment boundaries', () => {
    expect(activeMenuPath(menu, '/app/quotes-archive')).toBeNull()
  })
})
