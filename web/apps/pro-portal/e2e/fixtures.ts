// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { Page, Route } from '@playwright/test'
import type { PortalConfig, PortalMe } from '../src/types.ts'

export const ids = {
  user: '11111111-1111-4111-8111-111111111111',
  customer: '22222222-2222-4222-8222-222222222222',
  yardA: '33333333-3333-4333-8333-333333333333',
  yardB: '44444444-4444-4444-8444-444444444444',
  config: '55555555-5555-4555-8555-555555555555',
}

export function iso(): string {
  return new Date().toISOString()
}

export const meFixture: PortalMe = {
  user: {
    id: ids.user,
    customer_id: ids.customer,
    email: 'project@example.com',
    name: 'Project Lead',
    role: 'owner',
    status: 'Active',
    created_at: iso(),
    updated_at: iso(),
  },
  account: {
    id: ids.customer,
    name: 'Summit Frame Works',
    account_number: 'PRO-100',
    primary_branch_id: ids.yardA,
  },
  yards: [
    { id: ids.yardA, code: 'MAIN', name: 'Main Yard', timezone: 'UTC', is_primary: true },
    { id: ids.yardB, code: 'NORTH', name: 'North Yard', timezone: 'UTC', is_primary: false },
  ],
  capabilities: {
    manage_team: true,
    see_money: true,
    pay: true,
    order: true,
    request_quotes: true,
    approve_quotes: true,
    schedule: true,
    edit_delivery_instructions: true,
  },
}

export const configFixture: PortalConfig = {
  id: ids.config,
  dealer_name: 'Example Building Supply',
  logo_url: '',
  primary_color: '',
  support_email: 'support@example.com',
  support_phone: '',
  locale: 'en-US',
  created_at: iso(),
  updated_at: iso(),
}

export function json(route: Route, body: unknown, status = 200, headers: Record<string, string> = {}) {
  return route.fulfill({
    status,
    contentType: 'application/json',
    headers,
    body: JSON.stringify(body),
  })
}

export interface MockOptions {
  signedIn?: boolean
}

export async function installPortalMocks(page: Page, options: MockOptions = {}): Promise<void> {
  let signedIn = options.signedIn ?? true
  await page.route('**/api/portal/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    if (path === '/api/portal/v1/config') return json(route, configFixture)
    if (path === '/api/portal/v1/me') {
      return signedIn ? json(route, meFixture) : json(route, { error: 'signed out' }, 401)
    }
    if (path === '/api/portal/v1/login' && request.method() === 'POST') {
      signedIn = true
      return json(route, { user: meFixture.user, config: configFixture })
    }
    if (path === '/api/portal/v1/logout' && request.method() === 'POST') {
      signedIn = false
      return json(route, {})
    }
    return json(route, { error: 'not mocked' }, 404)
  })
}

export async function setTheme(page: Page, theme: 'dark' | 'light'): Promise<void> {
  await page.addInitScript((value) => {
    localStorage.setItem('gable_theme', value)
  }, theme)
}
