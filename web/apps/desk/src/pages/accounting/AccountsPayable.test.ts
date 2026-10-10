// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Accounts Payable UI.
 *
 * The vendor invoice routes are on the wire contract (C4-1b, ADR 0008 7.4),
 * so the tests below pin both sides of the money split:
 *
 *   read  — every invoice amount from /api/v1/ap/invoices is int64 CENTS and
 *           every line unit price int64 TEN THOUSANDTHS
 *           (core/internal/ap/model.go), rendered with `formatCents()` and
 *           `formatPrice4()`; the list arrives in the cursor envelope, which
 *           the service walks.
 *   write — POST /api/v1/ap/invoices takes cents, a decimal string quantity
 *           and ten thousandths; the approve goes through
 *           /ap/invoices/{id}/transitions with the revision; POST
 *           /api/v1/ap/payments still takes float dollars until its own
 *           conversion.
 *
 * Getting the scales backwards in either direction is a 100x error on a
 * vendor payment, so the request bodies are asserted, not just the rendering.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './AccountsPayable'
import type { LitElement } from 'lit'
import type { VendorInvoice, APPayment, APAgingSummary } from '../../types/ap'
import type { Vendor } from '../../types/vendor'
import { mountAsync, text, jsonResponse, clickByText, update, flush, q } from '../../test/dom'

const VENDOR_ID = '11111111-1111-1111-1111-111111111111'
const BRANCH_ID = '22222222-2222-4222-8222-222222222222'

function invoice(overrides: Partial<VendorInvoice> = {}): VendorInvoice {
  return {
    id: 'inv-1',
    number: 'AP-000001',
    vendor_id: VENDOR_ID,
    vendor_name: 'Cascade Lumber Co',
    branch_id: BRANCH_ID,
    vendor_invoice_number: 'CL-4471',
    currency: 'USD',
    invoice_date: '2026-08-01',
    due_date: '2026-08-31',
    po_id: null,
    subtotal_cents: 1_200_000,
    tax_cents: 34_567,
    total_cents: 1_234_567, // $12,345.67
    amount_paid_cents: 0,
    amount_open_cents: 1_234_567,
    status: 'approved',
    approved_by: null,
    approved_at: null,
    notes: '',
    revision: 1,
    gl_entry_id: null,
    created_at: '2026-08-01T10:00:00Z',
    ...overrides,
  }
}

function payment(overrides: Partial<APPayment> = {}): APPayment {
  return {
    id: 'pmt-1',
    vendor_id: VENDOR_ID,
    vendor_name: 'Cascade Lumber Co',
    amount: 738_807, // $7,388.07
    method: 'CHECK',
    check_number: '10432',
    payment_date: new Date().toISOString().slice(0, 10),
    status: 'COMPLETE',
    created_at: new Date().toISOString(),
    ...overrides,
  }
}

function aging(overrides: Partial<APAgingSummary> = {}): APAgingSummary {
  return {
    vendor_id: VENDOR_ID,
    vendor_name: 'Cascade Lumber Co',
    current: 500_000,
    past_30: 250_000,
    past_60: 100_000,
    past_90: 50_000,
    total: 900_000,
    ...overrides,
  }
}

const vendor: Vendor = {
  id: VENDOR_ID,
  name: 'Cascade Lumber Co',
  payment_terms: 'NET30',
  average_lead_time_days: 5,
  fill_rate: 0.97,
  total_spend_ytd: 0,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

interface Fixture {
  invoices?: VendorInvoice[]
  payments?: APPayment[]
  agingSummary?: APAgingSummary[]
}

/** Serve the five endpoints the page loads on connect. Returns the fetch spy. */
function serve(f: Fixture = {}) {
  const spy = vi.fn((url: string, _init?: RequestInit) => {
    // The invoice list is the envelope; one invoice's own route serves the
    // document itself.
    const byId = f.invoices?.find((inv) => url.endsWith(`/ap/invoices/${inv.id}`))
    if (byId) return Promise.resolve(jsonResponse(byId))
    if (url.includes('/ap/invoices')) return Promise.resolve(jsonResponse({ items: f.invoices ?? [], next_cursor: null, limit: 100 }))
    if (url.includes('/ap/payments')) return Promise.resolve(jsonResponse(f.payments ?? []))
    if (url.includes('/ap/aging')) return Promise.resolve(jsonResponse(f.agingSummary ?? []))
    if (url.includes('/vendors')) return Promise.resolve(jsonResponse([vendor]))
    if (url.includes('/gl/accounts')) return Promise.resolve(jsonResponse([]))
    return Promise.resolve(jsonResponse([]))
  })
  vi.stubGlobal('fetch', spy)
  return spy
}

/** Bodies of every non-GET request the page made, parsed. */
function postedBodies(spy: ReturnType<typeof serve>): Record<string, unknown>[] {
  return spy.mock.calls
    .filter((c) => c[1]?.body)
    .map((c) => JSON.parse(c[1]!.body as string) as Record<string, unknown>)
}

/**
 * Submit the open modal's form.
 *
 * Both drawers submit through `<form @submit=...>` with a `type="submit"`
 * button. jsdom does not perform implicit form submission on button click, so
 * clicking "Log Bill" in a test does nothing; dispatching the event the
 * component actually listens for is both closer to the real handler path and
 * independent of that jsdom gap.
 */
async function submitOpenForm(el: LitElement) {
  const form = q<HTMLFormElement>(el, 'form')
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await flush()
  await update(el, {})
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('accounts payable — reading cents', () => {
  it('renders an invoice total in dollars, not cents', async () => {
    serve({ invoices: [invoice()] })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    expect(text(el)).toContain('$12,345.67')
    expect(text(el)).not.toContain('$1,234,567.00')
  })

  it('shows the vendor own number and Gable number beside each other', async () => {
    serve({ invoices: [invoice()] })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    expect(text(el)).toContain('CL-4471')
  })

  it('groups thousands on a large outstanding balance', async () => {
    serve({ invoices: [invoice({ total_cents: 987_654_321_00, subtotal_cents: 987_654_321_00, tax_cents: 0, amount_open_cents: 987_654_321_00 })] })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    expect(text(el)).toContain('$987,654,321.00')
  })

  it('renders a zero amount paid as $0.00', async () => {
    serve({ invoices: [invoice({ amount_paid_cents: 0 })] })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    expect(text(el)).toContain('$0.00')
  })

  it('totals outstanding AP from the open amounts', async () => {
    serve({
      invoices: [
        invoice({ id: 'a', total_cents: 1_000_000, amount_paid_cents: 250_000, amount_open_cents: 750_000, status: 'partial' }),
        invoice({ id: 'b', total_cents: 500_000, amount_paid_cents: 0, amount_open_cents: 500_000, status: 'approved' }),
        // pending is not yet an obligation; it must not count as outstanding.
        invoice({ id: 'c', total_cents: 900_000, amount_paid_cents: 0, amount_open_cents: 900_000, status: 'pending' }),
      ],
    })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    // 750,000 + 500,000 = 1,250,000 cents.
    expect(text(el)).toContain('$12,500.00')
    // ...and the pending bill is reported separately, at its full value.
    expect(text(el)).toContain('$9,000.00')
    expect(text(el)).toContain('1 bills')
  })

  it('excludes a voided payment from paid month-to-date', async () => {
    serve({
      payments: [
        payment({ id: 'p1', amount: 100_000, status: 'COMPLETE' }),
        payment({ id: 'p2', amount: 999_999, status: 'VOIDED' }),
      ],
    })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    expect(text(el)).toContain('$1,000.00')
    expect(text(el)).not.toContain('$9,999.99')
  })

  it('excludes a prior-month payment from paid month-to-date', async () => {
    serve({
      payments: [
        payment({ id: 'p1', amount: 100_000, status: 'COMPLETE' }),
        payment({ id: 'p2', amount: 555_555, status: 'COMPLETE', payment_date: '2001-03-14' }),
      ],
    })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    expect(text(el)).toContain('$1,000.00')
    expect(text(el)).not.toContain('$5,555.55')
  })

  it('sums the aging buckets across vendors', async () => {
    serve({
      agingSummary: [
        aging(),
        aging({ vendor_id: 'v2', vendor_name: 'Ridgeline Supply', current: 100_000, past_30: 0, past_60: 0, past_90: 0, total: 100_000 }),
      ],
    })
    const el = await mountAsync<LitElement>('gable-accounts-payable')
    await clickByText(el, 'button', 'Aging')

    // current 500,000 + 100,000 = 600,000 cents
    expect(text(el)).toContain('$6,000.00')
    // total 900,000 + 100,000 = 1,000,000 cents
    expect(text(el)).toContain('$10,000.00')
    expect(text(el)).toContain('Ridgeline Supply')
  })

  it('shows payments on the payments tab', async () => {
    serve({ payments: [payment()] })
    const el = await mountAsync<LitElement>('gable-accounts-payable')
    await clickByText(el, 'button', 'Payments')

    expect(text(el)).toContain('$7,388.07')
    expect(text(el)).toContain('10432')
  })

  it('renders a line unit price at four decimals in the bill details', async () => {
    serve({ invoices: [invoice({ lines: [
      // 10 at $73.8813: the server's one rounding makes the line $7.39
      { id: 'l1', position: 0, description: '2x4 SPF', quantity: '10', unit_price_ten_thousandths: 738813, line_total_cents: 739,
        gl_account_id: null, purchase_order_line_id: null, product_id: null, po_freight_charge_id: null, created_at: '2026-08-01T10:00:00Z' },
    ] })] })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    await (el as unknown as { _viewInvoiceDetails: (id: string) => Promise<void> })._viewInvoiceDetails('inv-1')
    await update(el, {})

    expect(text(el)).toContain('$73.8813')
    expect(text(el)).toContain('$7.39')
  })
})

describe('accounts payable — writing the wire scales', () => {
  /** Open the bill drawer and fill one line plus tax. */
  async function draftBill(
    el: LitElement,
    opts: { qty: string; unitPrice: string; tax: string; description?: string },
  ) {
    await clickByText(el, 'button', 'Log Vendor Bill')

    const selects = Array.from(el.querySelectorAll<HTMLSelectElement>('select'))
    const vendorSelect = selects[0]
    vendorSelect.value = VENDOR_ID
    vendorSelect.dispatchEvent(new Event('change', { bubbles: true }))

    const setText = (input: HTMLInputElement, value: string) => {
      input.value = value
      input.dispatchEvent(new Event('input', { bubbles: true }))
      input.dispatchEvent(new Event('change', { bubbles: true }))
    }

    // Two text inputs in this drawer: the vendor invoice number, then the
    // single line item's description. Both are `required` and the submit
    // handler rejects a blank description, so the draft is not submittable
    // without it.
    const textInputs = Array.from(el.querySelectorAll<HTMLInputElement>('input[type="text"]'))
    setText(textInputs[0], 'CL-9001')
    setText(textInputs[1], opts.description ?? '2x4 SPF #2')

    const numbers = Array.from(el.querySelectorAll<HTMLInputElement>('input[type="number"]'))
    // Line inputs come first (qty, unit price), tax is the last number field.
    setText(numbers[0], opts.qty)
    setText(numbers[1], opts.unitPrice)
    setText(numbers[numbers.length - 1], opts.tax)

    await update(el, {})
  }

  it('previews the draft total in dollars, matching what the server will store', async () => {
    serve()
    const el = await mountAsync<LitElement>('gable-accounts-payable')
    // 10 x $73.88 = $738.80, plus $12.34 tax = $751.14
    await draftBill(el, { qty: '10', unitPrice: '73.88', tax: '12.34' })

    expect(text(el)).toContain('$751.14')
  })

  it('keeps the preview to two decimal places when the arithmetic does not divide evenly', async () => {
    serve()
    const el = await mountAsync<LitElement>('gable-accounts-payable')
    // 3 x $1.005 = $3.015, which rounds once, half away from zero, to $3.02:
    // the preview and the server's exact decimal arithmetic agree, where a
    // float preview (3 * 1.005 = 3.014999...) would show $3.01 and drift a
    // cent from the bill that is filed.
    await draftBill(el, { qty: '3', unitPrice: '1.005', tax: '0' })

    const body = text(el)
    expect(body).not.toMatch(/\$\d+\.\d{3}/)
    expect(body).toContain('$3.02')
  })

  it('sends cents, a decimal string quantity and ten thousandths, the units the endpoint documents', async () => {
    const spy = serve()
    const el = await mountAsync<LitElement>('gable-accounts-payable')
    await draftBill(el, { qty: '10', unitPrice: '73.88', tax: '12.34' })
    await submitOpenForm(el)

    const bill = postedBodies(spy).find((b) => 'vendor_invoice_number' in b)
    expect(bill).toBeDefined()
    // Cents and ten thousandths, NOT dollars — sending dollars here would
    // store a bill 100x (and a price 10000x) too small.
    expect(bill!.tax_cents).toBe(1234)
    expect((bill!.lines as { quantity: string; unit_price_ten_thousandths: number }[])[0].quantity).toBe('10')
    expect((bill!.lines as { quantity: string; unit_price_ten_thousandths: number }[])[0].unit_price_ten_thousandths).toBe(738800)
    expect(bill!.vendor_invoice_number).toBe('CL-9001')
  })

  it('converts the selected invoices to dollars when prefilling a payment', async () => {
    const spy = serve({ invoices: [invoice({ amount_open_cents: 1_234_567 })] })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    await clickByText(el, 'button', 'Record Payment')
    const vendorSelect = el.querySelector<HTMLSelectElement>('select')!
    vendorSelect.value = VENDOR_ID
    vendorSelect.dispatchEvent(new Event('change', { bubbles: true }))
    await update(el, {})

    const checkbox = el.querySelector<HTMLInputElement>('input[type="checkbox"]')
    expect(checkbox, 'the approved invoice should be selectable for payment').toBeTruthy()
    checkbox!.click()
    await update(el, {})

    await submitOpenForm(el)

    const pmt = postedBodies(spy).find((b) => 'invoice_ids' in b)
    expect(pmt).toBeDefined()
    // The payment route still takes float dollars until its own conversion:
    // 1,234,567 cents = $12,345.67.
    expect(pmt!.amount).toBe(12345.67)
  })
})

describe('accounts payable — endpoints', () => {
  it('calls exactly the AP endpoints the public backend serves', async () => {
    const spy = serve({ invoices: [invoice()] })
    await mountAsync<LitElement>('gable-accounts-payable')

    const urls = spy.mock.calls.map((c) => c[0])
    expect(urls.some((u) => u.includes('/api/v1/ap/invoices'))).toBe(true)
    expect(urls.some((u) => u.includes('/api/v1/ap/payments'))).toBe(true)
    expect(urls.some((u) => u.includes('/api/v1/ap/aging'))).toBe(true)
  })

  it('walks the invoice list cursor to the last page', async () => {
    const page1 = { items: [invoice({ id: 'inv-1' })], next_cursor: 'next', limit: 100 }
    const page2 = { items: [invoice({ id: 'inv-2', number: 'AP-000002' })], next_cursor: null, limit: 100 }
    let calls = 0
    const spy = vi.fn((url: string) => {
      if (url.includes('/ap/invoices')) {
        calls += 1
        return Promise.resolve(jsonResponse(calls === 1 ? page1 : page2))
      }
      if (url.includes('/ap/payments')) return Promise.resolve(jsonResponse([]))
      if (url.includes('/ap/aging')) return Promise.resolve(jsonResponse([]))
      if (url.includes('/vendors')) return Promise.resolve(jsonResponse([vendor]))
      if (url.includes('/gl/accounts')) return Promise.resolve(jsonResponse([]))
      return Promise.resolve(jsonResponse([]))
    })
    vi.stubGlobal('fetch', spy)

    const el = await mountAsync<LitElement>('gable-accounts-payable')

    const listUrl = spy.mock.calls.map((c) => String(c[0])).filter((u) => u.includes('/ap/invoices') && u.includes('cursor=next'))
    expect(listUrl, 'the second page is fetched with the first cursor').toHaveLength(1)
    expect(text(el)).toContain('CL-4471') // both pages' vendor numbers render
    vi.unstubAllGlobals()
  })

  it('approves an invoice through the transitions route with its revision', async () => {
    const spy = serve({ invoices: [invoice({ status: 'pending' })] })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    await clickByText(el, 'button', 'Approve')

    const approve = spy.mock.calls.find((c) => String(c[0]).includes('/transitions'))
    expect(approve, 'expected a POST to /api/v1/ap/invoices/{id}/transitions').toBeDefined()
    expect(String(approve![0])).toContain('/api/v1/ap/invoices/inv-1/transitions')
    expect(approve![1]?.method).toBe('POST')
    expect(JSON.parse(approve![1]!.body as string)).toEqual({ to: 'approved', revision: 1 })
  })
})
