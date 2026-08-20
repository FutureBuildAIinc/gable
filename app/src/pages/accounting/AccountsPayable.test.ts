// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Accounts Payable UI.
 *
 * This page straddles the ERP's money split, so the tests below pin both
 * sides of it:
 *
 *   read  — every amount from /api/v1/ap/* is int64 CENTS
 *           (backend/internal/ap/model.go), rendered with the shared
 *           `formatCents()`.
 *   write — POST /api/v1/ap/invoices and /api/v1/ap/payments take float64
 *           DOLLARS (ap/model.go:100,109,116), which the service multiplies
 *           by 100 on the way in (ap/service.go:53,56,81,167).
 *
 * Getting that backwards in either direction is a 100x error on a vendor
 * payment, so the request bodies are asserted, not just the rendering.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './AccountsPayable'
import type { LitElement } from 'lit'
import type { VendorInvoice, APPayment, APAgingSummary } from '../../types/ap'
import type { Vendor } from '../../types/vendor'
import { mountAsync, text, jsonResponse, clickByText, update, flush, q } from '../../test/dom'

const VENDOR_ID = '11111111-1111-1111-1111-111111111111'

function invoice(overrides: Partial<VendorInvoice> = {}): VendorInvoice {
  return {
    id: 'inv-1',
    vendor_id: VENDOR_ID,
    vendor_name: 'Cascade Lumber Co',
    invoice_number: 'CL-4471',
    invoice_date: '2026-08-01',
    due_date: '2026-08-31',
    subtotal: 1_200_000,
    tax_amount: 34_567,
    total: 1_234_567, // $12,345.67
    amount_paid: 0,
    status: 'APPROVED',
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
    if (url.includes('/ap/invoices')) return Promise.resolve(jsonResponse(f.invoices ?? []))
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

  it('groups thousands on a large outstanding balance', async () => {
    serve({ invoices: [invoice({ total: 987_654_321_00, subtotal: 987_654_321_00, tax_amount: 0 })] })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    expect(text(el)).toContain('$987,654,321.00')
  })

  it('renders a zero amount paid as $0.00', async () => {
    serve({ invoices: [invoice({ amount_paid: 0 })] })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    expect(text(el)).toContain('$0.00')
  })

  it('totals outstanding AP as invoice total less amount already paid', async () => {
    serve({
      invoices: [
        invoice({ id: 'a', total: 1_000_000, amount_paid: 250_000, status: 'PARTIAL' }),
        invoice({ id: 'b', total: 500_000, amount_paid: 0, status: 'APPROVED' }),
        // PENDING is not yet an obligation; it must not count as outstanding.
        invoice({ id: 'c', total: 900_000, amount_paid: 0, status: 'PENDING' }),
      ],
    })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    // (1,000,000 - 250,000) + 500,000 = 1,250,000 cents.
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
})

describe('accounts payable — writing dollars', () => {
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

    // Two text inputs in this drawer: the invoice number, then the single
    // line item's description. Both are `required` and the submit handler
    // rejects a blank description, so the draft is not submittable without it.
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
    // 3 x $1.005. Before the preview was computed in cents it rendered
    // "$3.015" — three decimal places on a money field, because
    // `toLocaleString` defaults `maximumFractionDigits` to 3.
    await draftBill(el, { qty: '3', unitPrice: '1.005', tax: '0' })

    const body = text(el)
    expect(body).not.toMatch(/\$\d+\.\d{3}/)

    // $3.01, not the $3.02 exact decimal arithmetic would give: 1.005 is not
    // representable in binary floating point and lands just below, so
    // 3 * 1.005 * 100 = 301.49999999999994 and rounds down. The backend
    // computes the stored figure the same way — `int64(UnitPrice*Quantity*100
    // + 0.5)` at ap/service.go:53 gives int64(301.99999999999994) = 301 — so
    // the preview and the saved invoice agree. Pinned because they must keep
    // agreeing: whichever side is "fixed" alone would introduce a drift
    // between what the user is shown and what is filed.
    expect(body).toContain('$3.01')
  })

  it('sends unit price and tax as dollars, the units the endpoint documents', async () => {
    const spy = serve()
    const el = await mountAsync<LitElement>('gable-accounts-payable')
    await draftBill(el, { qty: '10', unitPrice: '73.88', tax: '12.34' })
    await submitOpenForm(el)

    const bill = postedBodies(spy).find((b) => 'invoice_number' in b)
    expect(bill).toBeDefined()
    // Dollars, NOT 7388 / 1234 cents — ap/service.go multiplies by 100 itself,
    // so sending cents here would store a bill 100x too large.
    expect(bill!.tax_amount).toBe(12.34)
    expect((bill!.lines as { unit_price: number }[])[0].unit_price).toBe(73.88)
  })

  it('converts the selected invoices to dollars when prefilling a payment', async () => {
    const spy = serve({ invoices: [invoice({ total: 1_234_567, amount_paid: 0 })] })
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
    // 1,234,567 cents = $12,345.67 — dollars on the wire.
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

  it('approves an invoice through the approve endpoint', async () => {
    const spy = serve({ invoices: [invoice({ status: 'PENDING' })] })
    const el = await mountAsync<LitElement>('gable-accounts-payable')

    await clickByText(el, 'button', 'Approve')

    const approved = spy.mock.calls.find((c) => String(c[0]).includes('/approve'))
    expect(approved, 'expected a POST to /api/v1/ap/invoices/{id}/approve').toBeDefined()
    expect(String(approved![0])).toContain('/api/v1/ap/invoices/inv-1/approve')
    expect(approved![1]?.method).toBe('POST')
  })
})
