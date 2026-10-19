// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { PortalCapabilities } from '../types.ts'

export type Capability = keyof PortalCapabilities
export type TabKey = 'today' | 'order' | 'schedule' | 'pay' | 'quotes'

export interface TabDefinition {
  key: TabKey
  label: string
  path: string
}

const TABS: readonly TabDefinition[] = [
  { key: 'today', label: 'Today', path: '/today' },
  { key: 'order', label: 'Order', path: '/order' },
  { key: 'schedule', label: 'Schedule', path: '/schedule' },
  { key: 'pay', label: 'Pay', path: '/pay' },
  { key: 'quotes', label: 'Quotes', path: '/quotes' },
]

export function can(capabilities: PortalCapabilities | null, capability: Capability): boolean {
  return capabilities?.[capability] === true
}

export function tabAllowed(tab: TabKey, capabilities: PortalCapabilities | null): boolean {
  if (capabilities === null) return false
  switch (tab) {
    case 'today': return true
    case 'order': return can(capabilities, 'order')
    case 'schedule': return can(capabilities, 'schedule')
    case 'pay': return can(capabilities, 'see_money')
    case 'quotes': return can(capabilities, 'request_quotes') || can(capabilities, 'approve_quotes')
  }
}

export function visibleTabs(capabilities: PortalCapabilities | null): TabDefinition[] {
  return TABS.filter((tab) => tabAllowed(tab.key, capabilities))
}

export function defaultTab(capabilities: PortalCapabilities | null): TabKey {
  return visibleTabs(capabilities)[0]?.key ?? 'today'
}

export function roleLabel(role: string): string {
  const labels: Record<string, string> = {
    owner: 'Owner',
    owner_admin: 'Owner / Admin',
    estimator: 'Estimator',
    project_manager: 'Project manager',
    site_super: 'Site supervisor',
    ap_clerk: 'Accounts payable',
    ap_bookkeeper: 'AP / Bookkeeper',
  }
  return labels[role] ?? 'Contractor'
}

export function roleCapabilities(role: string): PortalCapabilities {
  switch (role) {
    case 'owner':
    case 'owner_admin':
      return {
        manage_team: true,
        see_money: true,
        pay: true,
        order: true,
        request_quotes: true,
        approve_quotes: true,
        schedule: true,
        edit_delivery_instructions: true,
        quote_customers: true,
      }
    case 'estimator':
      return {
        manage_team: false,
        see_money: false,
        pay: false,
        order: true,
        request_quotes: true,
        approve_quotes: true,
        schedule: true,
        edit_delivery_instructions: true,
        quote_customers: true,
      }
    case 'project_manager':
      return {
        manage_team: false,
        see_money: true,
        pay: false,
        order: true,
        request_quotes: true,
        approve_quotes: true,
        schedule: true,
        edit_delivery_instructions: true,
        quote_customers: true,
      }
    case 'site_super':
      return {
        manage_team: false,
        see_money: false,
        pay: false,
        order: true,
        request_quotes: false,
        approve_quotes: false,
        schedule: true,
        edit_delivery_instructions: true,
        quote_customers: true,
      }
    case 'ap_clerk':
    case 'ap_bookkeeper':
      return {
        manage_team: false,
        see_money: true,
        pay: true,
        order: false,
        request_quotes: false,
        approve_quotes: false,
        schedule: false,
        edit_delivery_instructions: false,
        quote_customers: false,
      }
    default:
      return {
        manage_team: false,
        see_money: false,
        pay: false,
        order: false,
        request_quotes: false,
        approve_quotes: false,
        schedule: false,
        edit_delivery_instructions: false,
        quote_customers: false,
      }
  }
}

