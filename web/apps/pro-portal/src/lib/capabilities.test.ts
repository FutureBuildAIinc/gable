// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest'
import { can, defaultTab, roleCapabilities, roleLabel, tabAllowed, visibleTabs } from './capabilities.ts'
import type { PortalCapabilities } from '../types.ts'

function capabilities(overrides: Partial<PortalCapabilities> = {}): PortalCapabilities {
  return {
    manage_team: false,
    see_money: false,
    pay: false,
    order: false,
    request_quotes: false,
    approve_quotes: false,
    schedule: false,
    edit_delivery_instructions: false,
    ...overrides,
  }
}

describe('capability gating', () => {
  it('uses the server capability instead of a role string', () => {
    const held = capabilities({ order: true, schedule: true })
    expect(can(held, 'order')).toBe(true)
    expect(can(held, 'see_money')).toBe(false)
    expect(visibleTabs(held).map((tab) => tab.key)).toEqual(['today', 'order', 'schedule'])
  })

  it('separates invoice reads from payment permission', () => {
    const held = capabilities({ see_money: true })
    expect(tabAllowed('pay', held)).toBe(true)
    expect(can(held, 'pay')).toBe(false)
  })

  it('opens quotes for either quote capability', () => {
    expect(tabAllowed('quotes', capabilities({ request_quotes: true }))).toBe(true)
    expect(tabAllowed('quotes', capabilities({ approve_quotes: true }))).toBe(true)
    expect(tabAllowed('quotes', capabilities())).toBe(false)
  })

  it('denies every tab except Today when capabilities are empty', () => {
    expect(visibleTabs(capabilities()).map((tab) => tab.key)).toEqual(['today'])
    expect(defaultTab(null)).toBe('today')
  })

  it('labels known roles and display aliases without trusting them for access', () => {
    expect(roleLabel('owner')).toBe('Owner')
    expect(roleLabel('owner_admin')).toBe('Owner / Admin')
    expect(roleLabel('project_manager')).toBe('Project manager')
    expect(roleLabel('site_super')).toBe('Site supervisor')
    expect(roleLabel('ap_clerk')).toBe('Accounts payable')
    expect(roleLabel('ap_bookkeeper')).toBe('AP / Bookkeeper')
    expect(roleLabel('unexpected')).toBe('Contractor')
  })

  it('maps canonical and alias roles to their capability sets and fails closed on unknown roles', () => {
    expect(roleCapabilities('owner_admin')).toEqual(roleCapabilities('owner'))
    expect(roleCapabilities('owner_admin').manage_team).toBe(true)
    expect(roleCapabilities('ap_bookkeeper')).toEqual(roleCapabilities('ap_clerk'))
    expect(roleCapabilities('ap_bookkeeper').see_money).toBe(true)
    expect(roleCapabilities('ap_bookkeeper').pay).toBe(true)
    expect(roleCapabilities('ap_bookkeeper').order).toBe(false)
    expect(roleCapabilities('site_super').see_money).toBe(false)
    expect(roleCapabilities('unknown_role')).toEqual(capabilities())
  })
})

