// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** Invoice and credit memo fixtures on the wire contract, for the page tests. */
import type { Invoice, InvoiceLine, InvoiceSummary } from '../types/invoice'
import type { CreditMemo, CreditMemoLine, CreditMemoSummary } from '../types/creditMemo'

export const INVOICE_ID = '00000000-0000-4000-8000-0000000000i1'
export const ORDER_ID = '00000000-0000-4000-8000-0000000000o1'
export const CUSTOMER_ID = '00000000-0000-4000-8000-0000000000c1'

export function invoiceLine(overrides: Partial<InvoiceLine> = {}): InvoiceLine {
  return {
    id: '00000000-0000-4000-8000-000000000l01',
    position: 0,
    line_type: 'product',
    parent_line_id: null,
    product_id: '00000000-0000-4000-8000-0000000000p1',
    charge_code_id: null,
    charge_code: null,
    sku: 'LUM-248-PREM',
    description: '2x4x8 SPF',
    quantity: '24',
    uom: 'PCS',
    price_uom: 'PCS',
    uom_qty: '1',
    price_uom_qty: '1',
    unit_price_ten_thousandths: 324_000,
    priced_unit_price_ten_thousandths: 324_000,
    price_source: 'price_list',
    override_reason: null,
    discount_percent: null,
    discount_cents: null,
    discount_reason: null,
    price_adjusted_by: null,
    line_total_cents: 77_760,
    taxable: true,
    revenue_account_code: null,
    is_special_order: false,
    vendor_id: null,
    special_order_unit_cost_ten_thousandths: null,
    created_at: '2026-08-11T14:00:00Z',
    order_line_id: '00000000-0000-4000-8000-000000000ol1',
    unit_cost_ten_thousandths: 228_000,
    cost_cents: 54_720,
    ...overrides,
  }
}

export function invoice(overrides: Partial<Invoice> = {}): Invoice {
  return {
    id: INVOICE_ID,
    number: 'IN-000042',
    branch_id: '00000000-0000-4000-8000-0000000000b1',
    customer_id: CUSTOMER_ID,
    customer_name: 'Ridgeview Framing',
    order_id: ORDER_ID,
    job_id: null,
    ship_to_id: null,
    status: 'unpaid',
    revision: 2,
    currency: 'USD',
    origin: 'order',
    delivery_type: 'delivery',
    picked_up_by: null,
    delivery_id: null,
    invoice_date: '2026-08-11',
    due_date: '2026-09-10',
    payment_terms_id: '00000000-0000-4000-8000-0000000000t1',
    discount_due_date: null,
    discount_percent: null,
    subtotal_cents: 77_760,
    tax_cents: 6_902,
    tax_rate_percent: '8.875',
    tax_exempt: false,
    tax_source: 'branch_rate',
    total_cents: 84_662,
    open_cents: 84_662,
    is_overdue: false,
    paid_at: null,
    gl_entry_id: null,
    voided_at: null,
    voided_by: null,
    void_reason: null,
    created_at: '2026-08-11T14:00:00Z',
    updated_at: '2026-08-11T14:00:00Z',
    ship_to: null,
    lines: [invoiceLine()],
    ...overrides,
  } as Invoice
}

export function invoiceSummary(overrides: Partial<InvoiceSummary> = {}): InvoiceSummary {
  const { lines: _lines, ship_to: _shipTo, ...summary } = invoice(overrides as Partial<Invoice>)
  void _lines
  void _shipTo
  return summary as InvoiceSummary
}

export function creditLine(overrides: Partial<CreditMemoLine> = {}): CreditMemoLine {
  const { order_line_id: _o, ...line } = invoiceLine({ quantity: '-2', line_total_cents: -6_480, cost_cents: -4_560 })
  void _o
  return { ...line, invoice_line_id: invoiceLine().id, restock: true, ...overrides } as CreditMemoLine
}

export function creditMemo(overrides: Partial<CreditMemo> = {}): CreditMemo {
  return {
    id: '00000000-0000-4000-8000-0000000000m1',
    number: null,
    branch_id: '00000000-0000-4000-8000-0000000000b1',
    customer_id: CUSTOMER_ID,
    customer_name: 'Ridgeview Framing',
    invoice_id: INVOICE_ID,
    pos_return_id: null,
    job_id: null,
    ship_to_id: null,
    status: 'draft',
    revision: 1,
    currency: 'USD',
    reason_code: 'return',
    reason: 'two boards split',
    subtotal_cents: -6_480,
    tax_cents: -575,
    tax_rate_percent: '8.875',
    total_cents: -7_055,
    open_cents: -7_055,
    gl_entry_id: null,
    memo_date: '2026-08-12',
    voided_at: null,
    voided_by: null,
    void_reason: null,
    created_at: '2026-08-12T14:00:00Z',
    updated_at: '2026-08-12T14:00:00Z',
    lines: [creditLine()],
    ...overrides,
  } as CreditMemo
}

export function creditMemoSummary(overrides: Partial<CreditMemoSummary> = {}): CreditMemoSummary {
  const { lines: _lines, ...summary } = creditMemo(overrides as Partial<CreditMemo>)
  void _lines
  return summary as CreditMemoSummary
}

/** A fetch stand-in that answers by path; a handler returns the body, or a Response for a status. */
export function routedFetch(routes: Record<string, (url: URL, init?: RequestInit) => unknown>) {
  const calls: { url: URL; init?: RequestInit }[] = []
  const fn = async (raw: string, init?: RequestInit): Promise<Response> => {
    const url = new URL(raw, 'http://localhost')
    calls.push({ url, init })
    // a key naming a method wins over the same path without one
    const keys = Object.keys(routes).sort((a, b) => Number(b.includes(' ')) - Number(a.includes(' ')))
    const key = keys.find(k => {
      const [method, path] = k.includes(' ') ? k.split(' ') : [undefined, k]
      return (!method || (init?.method ?? 'GET') === method) && url.pathname === path
    })
    if (!key) return new Response(JSON.stringify({ error: { code: 'not_found', message: 'not found' } }), { status: 404, headers: { 'Content-Type': 'application/json' } })
    const out = routes[key](url, init)
    if (out instanceof Response) return out
    return new Response(JSON.stringify(out), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  return { fn, calls }
}
