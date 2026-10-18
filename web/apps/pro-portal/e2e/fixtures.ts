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
    if (path === '/api/portal/v1/catalog/categories') {
      return json(route, [
        { id: '10000000-0000-4000-8000-000000000001', name: 'Dimensional Lumber', slug: 'dimensional-lumber', path: 'lumber.dimensional', depth: 1, sort_order: 1, product_count: 3, children: [] },
      ])
    }
    if (path.startsWith('/api/portal/v1/catalog/')) {
      const first = catalogFixtures[0]!
      return json(route, {
        ...first,
        weight_lbs: 11.4,
        upc: '012345678905',
        vendor: 'Cascade Forest',
        volume_breaks: [
          { min_quantity: 100, unit_price: 8.5, price_source: 'TIER_100', details: 'Full unit bunk', saves_per_unit: 0.7 },
        ],
      })
    }
    if (path === '/api/portal/v1/catalog') {
      return json(
        route,
        catalogFixtures.map((prod) => ({
          ...prod,
          availability: [
            { yard_id: ids.yardA, yard_code: 'MAIN', yard_name: 'Main Yard', on_hand: 320, available: 280, in_stock: true, lead_time_days: 1 },
            { yard_id: ids.yardB, yard_code: 'NORTH', yard_name: 'North Yard', on_hand: 140, available: 120, in_stock: true, lead_time_days: 2 },
          ],
        })),
      )
    }
    if (path === '/api/portal/v1/cart/items' || path === '/api/portal/v1/cart') {
      const first = catalogFixtures[0]!
      return json(
        route,
        {
          id: '20000000-0000-4000-8000-000000000002',
          item_count: 24,
          subtotal: 220.8,
          items: [
            {
              id: '30000000-0000-4000-8000-000000000003',
              product_id: first.id,
              product_sku: first.sku,
              product_name: first.name,
              image_url: '',
              quantity: 24,
              unit_price: first.customer_price,
              line_total: 220.8,
              available: first.available,
            },
          ],
        },
        request.method() === 'POST' ? 201 : 200,
      )
    }
    if (path === '/api/portal/v1/checkout') {
      return json(route, { order_id: testIds.order, message: 'Order placed.' }, 201)
    }
    if (path === `/api/portal/v1/orders/${testIds.order}/reorder-preview`) {
      return json(route, {
        source_order_id: testIds.order,
        project_id: null,
        original_total_cents: 23424,
        current_total_cents: 24720,
        total_diff_cents: 1296,
        lines: [
          {
            line_id: '40000000-0000-4000-8000-000000000004',
            product_id: testIds.product,
            sku: 'LUM-248',
            description: '2x4x8 SPF Premium',
            quantity: 48,
            uom: 'EA',
            original_unit_price_cents: 488,
            current_unit_price_cents: 515,
            price_diff_cents: 27,
            original_line_total_cents: 23424,
            current_line_total_cents: 24720,
            line_diff_cents: 1296,
            price_source: 'CONTRACT',
            available_qty: 480,
            in_stock: true,
          },
        ],
      })
    }
    if (path === '/api/portal/v1/orders/reorder') {
      return json(route, { order_id: testIds.order, new_order_id: testIds.order, message: 'Reorder placed.' }, 201)
    }
    if (path === '/api/portal/v1/procurement/projects') {
      return json(route, [
        {
          id: '50000000-0000-4000-8000-000000000005',
          name: 'Standard Cedar Deck Package',
          status: 'DRAFT',
          created_at: iso(),
          updated_at: iso(),
          lines: [
            {
              id: '51000000-0000-4000-8000-000000000005',
              project_id: '50000000-0000-4000-8000-000000000005',
              product_id: catalogFixtures[0]?.id ?? null,
              product_sku: catalogFixtures[0]?.sku ?? 'LUM-248',
              product_name: catalogFixtures[0]?.name ?? '2x10x16 #2 Fir',
              description: catalogFixtures[0]?.name ?? '2x10x16 #2 Fir',
              qty_entered: 36,
              uom: 'EA',
              notes: '',
              unit_price_cents: 920,
              line_total_cents: 33120,
              available_qty: 200,
              in_stock: true,
              created_at: iso(),
            },
          ],
        },
      ])
    }
    if (path === '/api/portal/v1/quotes/intake') {
      return json(route, {
        source: 'TEXT',
        synthetic: false,
        items: [
          {
            raw_text: '24 pcs 2x10x16 #2 Fir',
            parsed_qty: 24,
            parsed_unit: 'EA',
            parsed_description: '2x10x16 #2 Fir',
            matched_product_id: catalogFixtures[0]?.id ?? null,
            matched_product_name: catalogFixtures[0]?.name ?? '2x10x16 #2 Fir',
            matched_sku: catalogFixtures[0]?.sku ?? 'LUM-21016',
            confidence: 'HIGH',
          },
        ],
      })
    }
    if (path === '/api/portal/v1/quotes') {
      return json(route, [
        {
          id: testIds.quote,
          status: 'PRICED',
          erp_state: 'SENT',
          project_id: null,
          project_name: 'Glenview Lofts',
          notes: 'Second floor framing',
          priced: true,
          total_amount: 1420.5,
          freight_amount: 75,
          delivery_type: 'DELIVERY',
          expires_at: iso(),
          sent_at: iso(),
          accepted_at: null,
          rejected_at: null,
          created_at: iso(),
          updated_at: iso(),
          lines: [
            {
              id: '61000000-0000-4000-8000-000000000006',
              product_id: testIds.product,
              product_sku: 'LUM-248',
              description: '2x4x8 SPF Premium',
              customer_note: '',
              quantity: 200,
              uom: 'EA',
              price_uom: 'EA',
              unit_price: 4.88,
              line_total: 976,
              is_special_order: false,
            },
          ],
        },
      ])
    }
    if (path === '/api/portal/v1/billing/summary') {
      return json(route, {
        balance_tiles: {
          currency: 'USD',
          current_balance_cents: 412575,
          credit_limit_cents: 2500000,
          available_credit_cents: 2087425,
          past_due_cents: 125050,
          unapplied_cash_cents: 15000,
          net_balance_cents: 397575,
          open_invoice_count: 2,
          overdue_invoice_count: 1,
        },
        aging: {
          as_of: '2026-06-15',
          currency: 'USD',
          current_cents: 287525,
          days_1_30_cents: 125050,
          days_31_60_cents: 0,
          days_61_90_cents: 0,
          days_over_90_cents: 0,
          unapplied_cents: 15000,
          total_open_cents: 412575,
          net_cents: 397575,
        },
        job_summaries: [
          {
            project_id: '70000000-0000-4000-8000-000000000007',
            project_name: 'Glenview Lofts',
            status: 'ACTIVE',
            budget_cents: 1500000,
            quoted_cents: 920000,
            ordered_cents: 742118,
            invoiced_cents: 412575,
            paid_cents: 200000,
            open_balance_cents: 212575,
            quote_variance_cents: -177882,
            order_count: 3,
            invoice_count: 2,
            delivery_count: 3,
            delivered_count: 2,
          },
        ],
      })
    }
    if (path === '/api/portal/v1/invoices') {
      return json(route, [
        {
          id: testIds.invoice,
          number: 'IN-1001',
          is_overdue: true,
          order_id: testIds.order,
          status: 'UNPAID',
          total_amount: 1250.5,
          subtotal: 1150,
          tax_amount: 100.5,
          payment_terms: 'Net 30',
          due_date: iso(),
          paid_at: null,
          created_at: iso(),
          updated_at: iso(),
          lines: [
            { product_id: testIds.product, product_sku: 'LUM-248', product_name: '2x4x8 SPF Premium', quantity: 48, price_each: 4.88 },
          ],
        },
        {
          id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbc',
          number: 'IN-1002',
          is_overdue: false,
          order_id: testIds.order,
          status: 'PARTIAL',
          total_amount: 2875.25,
          subtotal: 2650,
          tax_amount: 225.25,
          payment_terms: 'Net 30',
          due_date: iso(),
          paid_at: null,
          created_at: iso(),
          updated_at: iso(),
          lines: [
            { product_id: testIds.product, product_sku: 'PLY-34', product_name: '3/4 CDX Plywood', quantity: 50, price_each: 53 },
          ],
        },
      ])
    }
    if (path === '/api/portal/v1/statements') return json(route, [])
    if (path === '/api/portal/v1/statement') {
      return json(route, {
        customer_id: ids.customer,
        customer_name: 'Summit Frame Works',
        from: '2026-05-15',
        to: '2026-06-15',
        currency: 'USD',
        opening_balance_cents: 200000,
        closing_balance_cents: 412575,
        unapplied_cents: 15000,
        net_balance_cents: 397575,
        aging: {
          as_of: '2026-06-15',
          currency: 'USD',
          current_cents: 287525,
          days_1_30_cents: 125050,
          days_31_60_cents: 0,
          days_61_90_cents: 0,
          days_over_90_cents: 0,
          unapplied_cents: 15000,
          total_open_cents: 412575,
          net_cents: 397575,
        },
        lines: [
          {
            id: '80000000-0000-4000-8000-000000000008',
            date: '2026-06-01',
            type: 'INVOICE',
            description: 'Invoice IN-1001',
            amount_cents: 125050,
            balance_after_cents: 325050,
            reference_id: testIds.invoice,
          },
        ],
        open_invoices: [
          { kind: 'INVOICE', id: testIds.invoice, number: 'IN-1001', date: '2026-06-01', due_date: '2026-06-10', total_cents: 125050, open_cents: 125050 },
          { kind: 'INVOICE', id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbc', number: 'IN-1002', date: '2026-06-08', due_date: '2026-07-08', total_cents: 287525, open_cents: 287525 },
        ],
      })
    }
    if (path === '/api/portal/v1/payment-methods') {
      return json(route, [
        {
          id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',
          customer_id: ids.customer,
          gateway: 'HOSTED_VAULT',
          method_type: 'CARD',
          brand: 'Visa',
          last4: '4242',
          exp_month: 12,
          exp_year: 2028,
          is_default: true,
          status: 'ACTIVE',
          created_at: iso(),
        },
      ])
    }
    if (path === '/api/portal/v1/payments') {
      const payment = {
        id: '90000000-0000-4000-8000-000000000009',
        number: 'PAY-000042',
        customer_id: ids.customer,
        currency: 'USD',
        method: 'CARD',
        status: 'APPLIED',
        amount_cents: 412575,
        unapplied_cents: 0,
        card_brand: 'Visa',
        card_last4: '4242',
        received_on: '2026-06-15',
        created_at: iso(),
        allocations: [
          { id: '91000000-0000-4000-8000-000000000001', invoice_id: testIds.invoice, invoice_number: 'IN-1001', amount_cents: 125050, applied_on: '2026-06-15', created_at: iso() },
        ],
      }
      return json(route, request.method() === 'POST' ? payment : [payment], request.method() === 'POST' ? 201 : 200)
    }
    if (path === '/api/portal/v1/projects') return json(route, { items: [{ id: '70000000-0000-4000-8000-000000000007', name: 'Glenview Lofts' }], next_cursor: null, limit: 50 })
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
