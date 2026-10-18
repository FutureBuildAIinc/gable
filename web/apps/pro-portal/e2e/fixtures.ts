// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { Page, Route } from '@playwright/test'
import type { PortalConfig, PortalMe } from '../src/types.ts'
import { holdFixture, orderFixture, slotsFixture, testIds, timelineFixture, todayFixture } from '../src/test/fixtures.ts'
import { catalogFixtures, customerQuoteFixture, customerQuoteSummaryFixture, quoteIds } from '../src/test/customer-quote-fixtures.ts'
import { publicQuoteFixture, publicToken, signedPublicQuoteFixture } from '../src/test/public-fixtures.ts'

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
  publicState?: 'open' | 'signed'
}

export async function installPortalMocks(page: Page, options: MockOptions = {}): Promise<void> {
  let signedIn = options.signedIn ?? true
  const customerQuote = structuredClone(customerQuoteFixture)
  const publicView = structuredClone(options.publicState === 'signed' ? signedPublicQuoteFixture : publicQuoteFixture)
  await page.route('https://fonts.googleapis.com/**', (route) => route.fulfill({ status: 200, contentType: 'text/css', body: '' }))
  await page.route('https://fonts.gstatic.com/**', (route) => route.fulfill({ status: 200, contentType: 'font/woff2', body: '' }))
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
    if (path === '/api/portal/v1/today') {
      return json(route, todayFixture, 200, { ETag: 'pro-e2e-v1', 'X-Portal-Latest-Change': iso() })
    }
    if (path === '/api/portal/v1/orders') {
      return json(route, [orderFixture], 200, { ETag: 'pro-e2e-v1', 'X-Portal-Latest-Change': iso() })
    }
    if (path === `/api/portal/v1/orders/${testIds.order}/timeline`) return json(route, timelineFixture)
    if (path === `/api/portal/v1/orders/${testIds.order}/delivery-instructions`) return json(route, orderFixture)
    if (path === `/api/portal/v1/orders/${testIds.order}`) return json(route, orderFixture)
    if (path === '/api/portal/v1/schedule/windows') {
      const yardId = url.searchParams.get('yard_id')
      const kind = url.searchParams.get('kind')
      return json(route, slotsFixture.filter((slot) => slot.yard_id === yardId && slot.kind === kind))
    }
    if (path === '/api/portal/v1/schedule/holds' && request.method() === 'POST') return json(route, holdFixture, 201)
    if (path === `/api/portal/v1/schedule/holds/${testIds.hold}`) {
      if (request.method() === 'DELETE') return route.fulfill({ status: 204 })
      return json(route, holdFixture)
    }
    if (path === '/api/portal/v1/customer-quotes') {
      if (request.method() === 'POST') return json(route, customerQuote, 201)
      return json(route, { items: [{ ...customerQuoteSummaryFixture, status: customerQuote.status, effective_status: customerQuote.effective_status }], next_cursor: null, limit: 30 })
    }
    if (path === '/api/portal/v1/customer-quotes/export') {
      return url.searchParams.get('format') === 'csv'
        ? route.fulfill({ status: 200, contentType: 'text/csv', body: 'reference,title\nCQ-1001,Deck' })
        : json(route, { customers: [], projects: [], quotes: [customerQuote] })
    }
    if (path === '/api/portal/v1/catalog') return json(route, catalogFixtures)
    if (path === '/api/portal/v1/projects') return json(route, { items: [], next_cursor: null, limit: 50 })
    if (path === `/api/portal/v1/customer-quotes/${quoteIds.quote}/preview`) return json(route, publicQuoteFixture)
    if (path === `/api/portal/v1/customer-quotes/${quoteIds.quote}/send`) {
      customerQuote.status = 'sent'
      customerQuote.effective_status = 'sent'
      customerQuote.link = { expires_at: customerQuote.valid_until }
      return json(route, { quote: customerQuote, link: { path: `/q/${publicToken}`, expires_at: customerQuote.valid_until, delivered: false } })
    }
    if (path === `/api/portal/v1/customer-quotes/${quoteIds.quote}/link/rotate`) {
      return json(route, { quote: customerQuote, link: { path: `/q/${publicToken}`, expires_at: customerQuote.valid_until, delivered: false } })
    }
    if (path === `/api/portal/v1/customer-quotes/${quoteIds.quote}/link/revoke`) {
      customerQuote.link = null
      return route.fulfill({ status: 204 })
    }
    if (path === `/api/portal/v1/customer-quotes/${quoteIds.quote}/confirm`) {
      customerQuote.status = 'confirmed'
      customerQuote.effective_status = 'confirmed'
      customerQuote.order = { id: testIds.order, number: 'SO-1001', status: 'CONFIRMED', fulfilment: 'ordered', total_cents: 1000 }
      return json(route, customerQuote)
    }
    if (path === `/api/portal/v1/customer-quotes/${quoteIds.quote}`) return json(route, customerQuote)
    return json(route, { error: 'not mocked' }, 404)
  })
  await page.route('**/api/public/v1/customer-quotes/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === `/api/public/v1/customer-quotes/${publicToken}`) return json(route, publicView)
    if (path === `/api/public/v1/customer-quotes/${publicToken}/selections`) {
      const body = request.postDataJSON() as { choices: { line_id: string, state: 'approved' | 'declined' }[] }
      publicView.lines = publicView.lines.map((line) => {
        const choice = body.choices.find((item) => item.line_id === line.id)
        if (choice !== undefined) return { ...line, selection: choice.state }
        const approvedSibling = body.choices.find((item) => item.state === 'approved' && publicView.lines.find((candidate) => candidate.id === item.line_id)?.group_id === line.group_id)
        return line.group_id !== undefined && approvedSibling !== undefined ? { ...line, selection: 'declined' as const } : line
      })
      publicView.selections_version = (publicView.selections_version ?? 1) + 1
      return json(route, publicView)
    }
    if (path === `/api/public/v1/customer-quotes/${publicToken}/code`) return route.fulfill({ status: 202 })
    if (path === `/api/public/v1/customer-quotes/${publicToken}/accept`) {
      customerQuote.status = 'approved'
      customerQuote.effective_status = 'approved'
      return json(route, signedPublicQuoteFixture)
    }
    if (path === `/api/public/v1/customer-quotes/${publicToken}/decline`) return json(route, { ...signedPublicQuoteFixture, status: 'declined' })
    return json(route, { error: 'not mocked' }, 404)
  })
}

export async function setTheme(page: Page, theme: 'dark' | 'light'): Promise<void> {
  await page.addInitScript((value) => {
    localStorage.setItem('gable_theme', value)
  }, theme)
}
