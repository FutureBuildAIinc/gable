// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { GableProPay } from './pay.ts'
import { OFFLINE_WRITE_REASON } from '../lib/offline.ts'
import { day, iso, meFixture, testIds } from '../test/fixtures.ts'
import type {
  PortalBillingSummary,
  PortalInvoice,
  PortalPayment,
  PortalPaymentMethod,
  PortalStatement,
} from '../types.ts'

const invoiceAId = testIds.invoice
const invoiceBId = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbc'
const methodId = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'

const billingSummaryFixture: PortalBillingSummary = {
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
    as_of: day(0),
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
}

const invoicesFixture: PortalInvoice[] = [
  {
    id: invoiceAId,
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
    id: invoiceBId,
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
]

const statementFixture: PortalStatement = {
  customer_id: testIds.customer,
  customer_name: 'Summit Frame Works',
  from: day(-30),
  to: day(0),
  currency: 'USD',
  opening_balance_cents: 200000,
  closing_balance_cents: 412575,
  unapplied_cents: 15000,
  net_balance_cents: 397575,
  aging: billingSummaryFixture.aging,
  lines: [
    {
      id: '80000000-0000-4000-8000-000000000008',
      date: day(-10),
      type: 'INVOICE',
      description: 'Invoice IN-1001',
      amount_cents: 125050,
      balance_after_cents: 325050,
      reference_id: invoiceAId,
    },
  ],
  open_invoices: [
    { kind: 'INVOICE', id: invoiceAId, number: 'IN-1001', date: day(-10), due_date: day(-1), total_cents: 125050, open_cents: 125050 },
    { kind: 'INVOICE', id: invoiceBId, number: 'IN-1002', date: day(-3), due_date: day(27), total_cents: 287525, open_cents: 287525 },
  ],
}

const paymentMethodsFixture: PortalPaymentMethod[] = [
  {
    id: methodId,
    customer_id: testIds.customer,
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
]

const paymentResultFixture: PortalPayment = {
  id: '90000000-0000-4000-8000-000000000009',
  number: 'PAY-000042',
  customer_id: testIds.customer,
  currency: 'USD',
  method: 'CARD',
  status: 'APPLIED',
  amount_cents: 412575,
  unapplied_cents: 0,
  card_brand: 'Visa',
  card_last4: '4242',
  received_on: day(0),
  created_at: iso(),
  allocations: [
    { id: '91000000-0000-4000-8000-000000000001', invoice_id: invoiceAId, invoice_number: 'IN-1001', amount_cents: 125050, applied_on: day(0), created_at: iso() },
    { id: '91000000-0000-4000-8000-000000000002', invoice_id: invoiceBId, invoice_number: 'IN-1002', amount_cents: 287525, applied_on: day(0), created_at: iso() },
  ],
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ETag: 'v1', 'X-Portal-Latest-Change': iso() },
  })
}

describe('Pay screen', () => {
  let paymentRequestBodies: Array<Record<string, unknown>> = []

  beforeEach(() => {
    document.body.innerHTML = ''
    localStorage.clear()
    sessionStorage.clear()
    window.history.replaceState({}, '', '/pay')
    paymentRequestBodies = []
    vi.restoreAllMocks()
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof Request ? input.url : input.toString()
      const method = init?.method ?? 'GET'
      if (url.includes('/api/portal/v1/billing/summary')) return jsonResponse(billingSummaryFixture)
      if (url.includes('/api/portal/v1/invoices')) return jsonResponse(invoicesFixture)
      if (url.includes('/api/portal/v1/statements')) return jsonResponse([])
      if (url.includes('/api/portal/v1/statement')) return jsonResponse(statementFixture)
      if (url.includes('/api/portal/v1/payment-methods')) return jsonResponse(paymentMethodsFixture)
      if (url.includes('/api/portal/v1/payments') && method === 'POST') {
        paymentRequestBodies.push(JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>)
        return jsonResponse(paymentResultFixture, 201)
      }
      if (url.includes('/api/portal/v1/payments')) return jsonResponse([paymentResultFixture])
      return jsonResponse({})
    }))
  })

  it('formats balance tiles and aging buckets in cents and pays multiple open invoices with tokenized method', async () => {
    const element = new GableProPay()
    element.me = structuredClone(meFixture)
    document.body.append(element)
    await vi.waitFor(() => expect(element.querySelector('[data-testid="pay-balance-tiles"]')).not.toBeNull())

    expect(element.querySelector('[data-testid="tile-current-balance"]')?.textContent).toBe('$4,125.75')
    expect(element.querySelector('[data-testid="tile-past-due"]')?.textContent).toBe('$1,250.50')
    expect(element.querySelector('[data-testid="aging-current"]')?.textContent).toBe('$2,875.25')
    expect(element.querySelector('[data-testid="aging-1-30"]')?.textContent).toBe('$1,250.50')

    element.querySelector<HTMLButtonElement>('[data-testid="select-all-open-btn"]')?.click()
    await element.updateComplete
    expect(element.querySelector('[data-testid="pay-selected-total"]')?.textContent).toBe('$4,125.75')

    element.querySelector<HTMLButtonElement>('[data-testid="submit-payment-btn"]')?.click()
    await vi.waitFor(() => expect(paymentRequestBodies).toHaveLength(1))
    expect(paymentRequestBodies[0]).toMatchObject({
      invoice_ids: [invoiceAId, invoiceBId],
      payment_method_id: methodId,
    })
  })

  it('allows viewing balances and statements when see_money is true and pay is false', async () => {
    const me = structuredClone(meFixture)
    me.capabilities.pay = false
    const element = new GableProPay()
    element.me = me
    document.body.append(element)
    await vi.waitFor(() => expect(element.querySelector('[data-testid="pay-capability-notice"]')).not.toBeNull())

    expect(element.querySelector<HTMLButtonElement>('[data-testid="submit-payment-btn"]')?.disabled).toBe(true)

    element.querySelector<HTMLButtonElement>('[data-testid="pay-tab-statement"]')?.click()
    await element.updateComplete
    expect(element.querySelector('[data-testid="statement-net"]')?.textContent).toBe('$3,975.75')
  })

  it('disables payment writes offline with the plain offline reason', async () => {
    const element = new GableProPay()
    element.me = structuredClone(meFixture)
    element.offline = true
    document.body.append(element)
    await vi.waitFor(() => expect(element.querySelector('[data-testid="pay-offline-banner"]')).not.toBeNull())

    expect(element.querySelector('[data-testid="pay-offline-banner"]')?.textContent).toBe(OFFLINE_WRITE_REASON)
    expect(element.querySelector<HTMLButtonElement>('[data-testid="submit-payment-btn"]')?.disabled).toBe(true)
  })

  it('renders job balances, manages tokenized payment methods, and restricts roles without see_money', async () => {
    const element = new GableProPay()
    element.me = structuredClone(meFixture)
    document.body.append(element)
    await vi.waitFor(() => expect(element.querySelector('[data-testid="pay-tab-jobs"]')).not.toBeNull())

    element.querySelector<HTMLButtonElement>('[data-testid="pay-tab-jobs"]')?.click()
    await element.updateComplete
    expect(element.querySelector('[data-testid="pay-jobs-view"]')?.textContent).toContain('Glenview Lofts')
    expect(element.querySelector('[data-testid="pay-jobs-view"]')?.textContent).toContain('$2,125.75 open')

    element.querySelector<HTMLButtonElement>('[data-testid="pay-tab-methods"]')?.click()
    await element.updateComplete
    expect(element.querySelector('[data-testid="saved-methods-list"]')?.textContent).toContain('Visa ending in 4242')

    element.querySelector<HTMLButtonElement>('[data-testid="save-payment-method-btn"]')?.click()
    await vi.waitFor(() => expect(element.textContent).toContain('Saved'))

    element.remove()
    const siteSuper = structuredClone(meFixture)
    siteSuper.capabilities.see_money = false
    siteSuper.capabilities.pay = false
    const restricted = new GableProPay()
    restricted.me = siteSuper
    document.body.append(restricted)
    await restricted.updateComplete
    expect(restricted.querySelector('[data-testid="pay-restricted"]')).not.toBeNull()
  })
})
