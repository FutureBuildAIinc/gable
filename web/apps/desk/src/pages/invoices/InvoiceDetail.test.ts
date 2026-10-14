// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The invoice page on the wire contract (C2-3): the document number, the new
 * line shape, cents money and decimal string quantities, the Overdue badge, the
 * void dialog with the revision and the server's blockers, the VOID banner, and
 * the credit memos listed against the invoice.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './InvoiceDetail'
import type { GableInvoiceDetail } from './InvoiceDetail'
import { mountAsync, text, flush } from '../../test/dom'
import { creditMemoSummary, invoice, invoiceLine, routedFetch, INVOICE_ID, ORDER_ID } from '../../test/salesFixtures'
import type { Invoice } from '../../types/invoice'

function stub(inv: Invoice, extra: Record<string, (url: URL, init?: RequestInit) => unknown> = {}) {
  const state = routedFetch({
    [`/api/v1/invoices/${INVOICE_ID}`]: () => inv,
    [`/api/v1/invoices/${INVOICE_ID}/payments`]: () => null, // the payments route answers null when there are none
    '/api/v1/credit-memos': () => ({ items: [], next_cursor: null, limit: 100 }),
    [`/api/v1/orders/${ORDER_ID}`]: () => ({ id: ORDER_ID, number: 'SO-000007' }),
    ...extra,
  })
  vi.stubGlobal('fetch', vi.fn(state.fn))
  return state
}

function buttonByText(el: Element, label: string): HTMLButtonElement {
  return Array.from(el.querySelectorAll('button')).find(b => b.textContent?.includes(label)) as HTMLButtonElement
}

describe('gable-invoice-detail', () => {
  let el: GableInvoiceDetail
  afterEach(() => vi.unstubAllGlobals())

  describe('an unpaid invoice', () => {
    beforeEach(() => { stub(invoice()) })

    it('renders the document number, the lowercase status and the order link by number', async () => {
      el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
      const body = text(el)
      expect(body).toContain('IN-000042')
      expect(body).toContain('Unpaid')
      expect(body).toContain('SO-000007')
      expect(el.querySelector(`a[href="/orders/${ORDER_ID}"]`)).not.toBeNull()
    })

    it('renders cents as dollars: line, subtotal, tax with its rate and source, total and open amount', async () => {
      el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
      const totals = text(el.querySelector('[data-testid="invoice-totals"]'))
      expect(totals).toContain('$777.60')
      expect(totals).toContain('8.875%')
      expect(totals).toContain('branch rate')
      expect(totals).toContain('$69.02')
      expect(totals).toContain('$846.62')
      expect(text(el.querySelector('[data-testid="open-amount"]'))).toBe('$846.62')
    })

    it('renders the scaled unit price with its price unit and the decimal string quantity', async () => {
      el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
      const row = text(el.querySelector('tr[data-line-type="product"]'))
      expect(row).toContain('LUM-248-PREM')
      expect(row).toContain('24')
      expect(row).toContain('PCS')
      expect(row).toContain('$32.40')
      expect(row).toContain('$777.60')
    })

    it('shows cost and margin on the lines for the roles the server sends cost to', async () => {
      el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
      const headers = Array.from(el.querySelectorAll('thead th')).map(th => th.textContent?.trim())
      expect(headers).toContain('Cost')
      expect(headers).toContain('Margin')
      const row = text(el.querySelector('tr[data-line-type="product"]'))
      expect(row).toContain('$547.20')
      expect(row).toContain('$230.40')
    })

    it('shows no cost column when no line carries a cost', async () => {
      stub(invoice({ lines: [invoiceLine({ unit_cost_ten_thousandths: null, cost_cents: 0 })] }))
      el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
      const headers = Array.from(el.querySelectorAll('thead th')).map(th => th.textContent?.trim())
      expect(headers).not.toContain('Cost')
    })

    it('offers print, email, create credit memo (prefilled) and void', async () => {
      el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
      expect(text(el)).toContain('Print PDF')
      expect(text(el)).toContain('Email')
      const link = el.querySelector('a[href^="/credit-memos/new"]') as HTMLAnchorElement
      expect(link.getAttribute('href')).toBe(`/credit-memos/new?invoice_id=${INVOICE_ID}`)
      expect(buttonByText(el, 'Void')).toBeTruthy()
    })

    it('keeps every header button label on one line', async () => {
      el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
      const header = el.querySelector('h1')!.closest('div.border-b')!
      for (const b of Array.from(header.querySelectorAll('button, a'))) expect(b.className).toContain('whitespace-nowrap')
    })
  })

  it('shows the Overdue badge from is_overdue and the due date in red', async () => {
    stub(invoice({ is_overdue: true, due_date: '2026-07-01' }))
    el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
    expect(el.querySelector('[data-testid="overdue-badge"]')).not.toBeNull()
    expect(el.querySelector('[data-testid="due-date"]')!.className).toContain('text-red-400')
  })

  it('shows the discount terms and the ship-to snapshot when the invoice carries them', async () => {
    stub(invoice({
      discount_percent: '2.0000',
      discount_due_date: '2026-08-21',
      ship_to: { id: 's1', code: 'MAIN', name: 'Maple Ridge site', line1: '12 Quarry Rd', line2: null, city: 'Kelowna', region: 'BC', postal_code: 'V1Y 1A1', country: null, phone: null, delivery_instructions: null },
    }))
    el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
    expect(text(el)).toContain('Early payment')
    expect(text(el)).toContain('2.0000%')
    expect(text(el.querySelector('[data-testid="ship-to"]'))).toContain('12 Quarry Rd')
  })

  it('renders a kit with its components indented and a text line as a note', async () => {
    stub(invoice({
      lines: [
        invoiceLine({ id: 'k', line_type: 'kit', sku: 'KIT-1', description: 'Deck kit', position: 0 }),
        invoiceLine({ id: 'c1', line_type: 'component', parent_line_id: 'k', sku: 'SCR-1', description: 'Screws', position: 1, line_total_cents: null }),
        invoiceLine({ id: 't', line_type: 'text', description: 'Leave at the gate', position: 2, quantity: null, unit_price_ten_thousandths: null, line_total_cents: null }),
      ],
    }))
    el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
    expect(el.querySelector('tr[data-line-type="component"] td')!.className).toContain('pl-10')
    const note = el.querySelector('tr[data-line-type="text"]')!
    expect(text(note)).toBe('Leave at the gate')
  })

  it('lists the credit memos against the invoice as credits', async () => {
    stub(invoice(), {
      '/api/v1/credit-memos': () => ({ items: [creditMemoSummary({ id: 'm1', number: 'CM-000003', status: 'open' }), creditMemoSummary({ id: 'm2', number: null, status: 'draft' })], next_cursor: null, limit: 100 }),
    })
    el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
    const table = el.querySelector('table[aria-label="Credit memos for this invoice"]')!
    expect(text(table)).toContain('CM-000003')
    expect(text(table)).toContain('Draft')
    expect(text(table)).toContain('-$70.55')
    expect(table.querySelector('td.text-red-400')).not.toBeNull()
  })

  describe('voiding', () => {
    it('requires a reason, sends the revision and shows the VOID banner afterwards', async () => {
      const state = stub(invoice(), {
        [`POST /api/v1/invoices/${INVOICE_ID}/transitions`]: () => invoice({ status: 'void', revision: 3, void_reason: 'billed twice', voided_at: '2026-08-12T10:00:00Z', open_cents: 0 }),
      })
      el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
      buttonByText(el, 'Void').click()
      await el.updateComplete
      const dialog = el.querySelector('gable-sales-dialog')!
      const confirmBtn = Array.from(dialog.querySelectorAll('button')).find(b => b.textContent?.includes('Void invoice')) as HTMLButtonElement
      expect(confirmBtn.disabled).toBe(true)
      const textarea = dialog.querySelector('textarea') as HTMLTextAreaElement
      textarea.value = 'billed twice'
      textarea.dispatchEvent(new Event('input'))
      await flush()
      await (dialog as unknown as { updateComplete: Promise<boolean> }).updateComplete
      confirmBtn.click()
      await flush()
      await flush()
      await el.updateComplete
      const call = state.calls.find(c => c.url.pathname.endsWith('/transitions'))!
      expect(JSON.parse(String(call.init!.body))).toEqual({ to: 'void', revision: 2, reason: 'billed twice' })
      const banner = el.querySelector('[data-testid="void-banner"]')!
      expect(text(banner)).toContain('VOID')
      expect(text(banner)).toContain('billed twice')
      // a void invoice has no actions
      expect(text(el)).not.toContain('Print PDF')
      expect(buttonByText(el, 'Void invoice')).toBeFalsy()
    })

    it('keeps the dialog open and shows the server blocker in plain words', async () => {
      stub(invoice(), {
        [`POST /api/v1/invoices/${INVOICE_ID}/transitions`]: () =>
          new Response(JSON.stringify({ error: { code: 'conflict', message: 'a credit memo names the invoice: void the credit memos first', details: [{ code: 'has_credit_memos', message: 'x' }] }, meta: {} }), { status: 409, headers: { 'Content-Type': 'application/json' } }),
      })
      el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
      buttonByText(el, 'Void').click()
      await el.updateComplete
      const dialog = el.querySelector('gable-sales-dialog')!
      const textarea = dialog.querySelector('textarea') as HTMLTextAreaElement
      textarea.value = 'oops'
      textarea.dispatchEvent(new Event('input'))
      await flush()
      await (dialog as unknown as { updateComplete: Promise<boolean> }).updateComplete
      ;(Array.from(dialog.querySelectorAll('button')).find(b => b.textContent?.includes('Void invoice')) as HTMLButtonElement).click()
      await flush()
      await flush()
      await el.updateComplete
      await (dialog as unknown as { updateComplete: Promise<boolean> }).updateComplete
      const err = text(el.querySelector('[data-testid="dialog-error"]'))
      expect(err).toContain('void the credit memos first')
      expect(err).toContain('void them, then void the invoice')
      expect(el.querySelector('[role="dialog"]')).not.toBeNull()
    })
  })

  it('shows the void banner with its reason and no actions on a void invoice', async () => {
    stub(invoice({ status: 'void', void_reason: 'entered against the wrong customer', open_cents: 0 }))
    el = await mountAsync<GableInvoiceDetail>('gable-invoice-detail', { routeId: INVOICE_ID })
    expect(text(el.querySelector('[data-testid="void-banner"]'))).toContain('entered against the wrong customer')
    expect(text(el)).not.toContain('Email')
    expect(el.querySelector('a[href^="/credit-memos/new"]')).toBeNull()
  })
})
