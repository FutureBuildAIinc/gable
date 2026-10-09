// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The credit memo form (C2-3): quantities typed positive and sent as negative
 * decimal strings, validated against billed less already credited, a restock
 * flag per stocked line, free ADJUST lines, and an edit that keeps the revision.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './CreditMemoForm'
import { buildCreditMemoRequest, creditableLines, estimateCreditCents } from './CreditMemoForm'
import type { GableCreditMemoForm } from './CreditMemoForm'
import { mountAsync, text, flush } from '../../test/dom'
import { creditMemo, creditMemoSummary, creditLine, invoice, invoiceLine, routedFetch, INVOICE_ID } from '../../test/salesFixtures'

const LINE = invoiceLine()

function row(qtyInput: string, remaining = '24', restock = true) {
  return { line: LINE, qtyInput, restock, remaining }
}

describe('buildCreditMemoRequest', () => {
  const base = { invoiceId: INVOICE_ID, reasonCode: 'return' as const, reason: 'split boards' }

  it('turns a typed positive quantity into the wire negative with the restock flag', () => {
    const { request, errors } = buildCreditMemoRequest(base, [row('2')], [])
    expect(errors).toEqual({})
    expect(request).toEqual({
      invoice_id: INVOICE_ID,
      reason_code: 'return',
      reason: 'split boards',
      lines: [{ invoice_line_id: LINE.id, quantity: '-2', restock: true }],
    })
  })

  it('refuses more than is left to credit and names the remaining quantity', () => {
    const { errors } = buildCreditMemoRequest(base, [row('3', '2.5')], [])
    expect(errors[`return:${LINE.id}`]).toBe('Only 2.5 remain to credit')
  })

  it('refuses zero, text and negatives', () => {
    for (const bad of ['0', 'abc', '-1']) {
      const { errors } = buildCreditMemoRequest(base, [row(bad)], [])
      expect(errors[`return:${LINE.id}`]).toBe('Enter a quantity greater than zero')
    }
  })

  it('skips blank rows and asks for at least one line and a reason', () => {
    const { errors } = buildCreditMemoRequest({ ...base, reason: ' ' }, [row('')], [])
    expect(errors.lines).toBeTruthy()
    expect(errors.reason).toBeTruthy()
  })

  it('builds a free ADJUST line with a scaled price and a negative quantity', () => {
    const { request, errors } = buildCreditMemoRequest(base, [row('')], [{ description: 'Goodwill', qtyInput: '1', priceInput: '0.50' }])
    expect(errors).toEqual({})
    expect(request.lines).toEqual([{ line_type: 'charge', charge_code: 'ADJUST', quantity: '-1', unit_price_ten_thousandths: 5000, description: 'Goodwill' }])
  })

  it('refuses a free line with no description or a zero price', () => {
    expect(buildCreditMemoRequest(base, [], [{ description: '', qtyInput: '1', priceInput: '5' }]).errors['free:0']).toBe('Describe the adjustment')
    expect(buildCreditMemoRequest(base, [], [{ description: 'x', qtyInput: '1', priceInput: '0' }]).errors['free:0']).toBe('Enter a price greater than zero')
  })

  it('names the customer for a memo with no invoice', () => {
    const noInvoice = { customerId: 'c-1', reasonCode: 'other' as const, reason: 'goodwill' }
    const { request } = buildCreditMemoRequest(noInvoice, [], [{ description: 'Goodwill', qtyInput: '1', priceInput: '5' }])
    expect(request.customer_id).toBe('c-1')
    expect(request.invoice_id).toBeUndefined()
    expect(buildCreditMemoRequest({ ...noInvoice, customerId: '' }, [], []).errors.customer).toBeTruthy()
  })

  it('does not send restock for a charge line', () => {
    const charge = invoiceLine({ id: 'ch', line_type: 'charge', product_id: null, sku: null, charge_code: 'FREIGHT' })
    const { request } = buildCreditMemoRequest(base, [{ line: charge, qtyInput: '1', restock: true, remaining: '1' }], [])
    expect(request.lines[0]).toEqual({ invoice_line_id: 'ch', quantity: '-1' })
  })
})

describe('estimateCreditCents', () => {
  it('prices each credit from its invoice line, negative, with integer arithmetic', () => {
    expect(estimateCreditCents([row('2')], [])).toBe(-6480)
    expect(estimateCreditCents([row('0.5')], [])).toBe(-1620)
  })

  it('adds the free lines and ignores unparseable rows', () => {
    expect(estimateCreditCents([row('x')], [{ description: 'a', qtyInput: '1', priceInput: '2.50' }])).toBe(-250)
    expect(estimateCreditCents([], [])).toBe(0)
  })
})

describe('creditableLines', () => {
  it('offers products, kits and charges, never a note or a component', () => {
    const inv = invoice({
      lines: [
        invoiceLine({ id: 'p', position: 2 }),
        invoiceLine({ id: 't', line_type: 'text', position: 0 }),
        invoiceLine({ id: 'c', line_type: 'component', position: 1 }),
        invoiceLine({ id: 'k', line_type: 'kit', position: 3 }),
      ],
    })
    expect(creditableLines(inv).map(l => l.id)).toEqual(['p', 'k'])
  })
})

describe('gable-credit-memo-form', () => {
  let el: GableCreditMemoForm
  afterEach(() => {
    vi.unstubAllGlobals()
    window.history.replaceState(null, '', '/')
  })

  function stubAll(extra: Record<string, (url: URL, init?: RequestInit) => unknown> = {}) {
    const state = routedFetch({
      [`/api/v1/invoices/${INVOICE_ID}`]: () => invoice(),
      '/api/v1/credit-memos': () => ({ items: [], next_cursor: null, limit: 200 }),
      ...extra,
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
    return state
  }

  beforeEach(() => {
    window.history.replaceState(null, '', `/credit-memos/new?invoice_id=${INVOICE_ID}`)
  })

  it('explains draft versus posted and lists the invoice lines with what is left to credit', async () => {
    stubAll()
    el = await mountAsync<GableCreditMemoForm>('gable-credit-memo-form')
    expect(text(el.querySelector('[data-testid="draft-explainer"]'))).toContain('Nothing moves until you post it')
    const table = el.querySelector('table[aria-label="Invoice lines to credit"]')!
    expect(text(table)).toContain('LUM-248-PREM')
    expect(text(table)).toContain('24')
    expect(el.querySelector('input[aria-label="Restock LUM-248-PREM"]')).not.toBeNull()
  })

  it('reduces what is left by the credit memos already written against the invoice', async () => {
    stubAll({
      '/api/v1/credit-memos': () => ({ items: [creditMemoSummary({ id: 'm1', status: 'open' }), creditMemoSummary({ id: 'm2', status: 'void' })], next_cursor: null, limit: 200 }),
      '/api/v1/credit-memos/m1': () => creditMemo({ id: 'm1', lines: [creditLine({ quantity: '-5' })] }),
    })
    el = await mountAsync<GableCreditMemoForm>('gable-credit-memo-form')
    const cells = Array.from(el.querySelectorAll('table[aria-label="Invoice lines to credit"] tbody tr td'))
    expect(text(cells[2])).toBe('19')
  })

  it('shows a live total as a credit while the quantity is typed', async () => {
    stubAll()
    el = await mountAsync<GableCreditMemoForm>('gable-credit-memo-form')
    const qty = el.querySelector('input[aria-label="Credit quantity for LUM-248-PREM"]') as HTMLInputElement
    qty.value = '2'
    qty.dispatchEvent(new Event('input'))
    await el.updateComplete
    expect(text(el.querySelector('[data-testid="credit-estimate"]'))).toContain('-$64.80')
  })

  it('creates the draft with negative decimal strings and goes to it', async () => {
    const state = stubAll({
      'POST /api/v1/credit-memos': () => new Response(JSON.stringify(creditMemo({ id: 'new-1' })), { status: 201, headers: { 'Content-Type': 'application/json' } }),
    })
    el = await mountAsync<GableCreditMemoForm>('gable-credit-memo-form')
    const qty = el.querySelector('input[aria-label="Credit quantity for LUM-248-PREM"]') as HTMLInputElement
    qty.value = '2'
    qty.dispatchEvent(new Event('input'))
    const reason = el.querySelector('#cm-reason') as HTMLTextAreaElement
    reason.value = 'two boards split'
    reason.dispatchEvent(new Event('input'))
    await el.updateComplete
    ;(Array.from(el.querySelectorAll('button')).find(b => b.textContent?.includes('Create draft')) as HTMLButtonElement).click()
    await flush()
    await flush()
    const post = state.calls.find(c => c.init?.method === 'POST')!
    expect(JSON.parse(String(post.init!.body))).toEqual({
      invoice_id: INVOICE_ID,
      reason_code: 'return',
      reason: 'two boards split',
      lines: [{ invoice_line_id: LINE.id, quantity: '-2', restock: true }],
    })
    expect(window.location.pathname).toBe('/credit-memos/new-1')
  })

  it('shows the validation beside the input and sends nothing', async () => {
    const state = stubAll()
    el = await mountAsync<GableCreditMemoForm>('gable-credit-memo-form')
    const qty = el.querySelector('input[aria-label="Credit quantity for LUM-248-PREM"]') as HTMLInputElement
    qty.value = '99'
    qty.dispatchEvent(new Event('input'))
    await el.updateComplete
    ;(Array.from(el.querySelectorAll('button')).find(b => b.textContent?.includes('Create draft')) as HTMLButtonElement).click()
    await el.updateComplete
    expect(text(el)).toContain('Only 24 remain to credit')
    expect(text(el)).toContain('Give a reason')
    expect(state.calls.some(c => c.init?.method === 'POST')).toBe(false)
  })

  it('shows the server refusal (exceeds_billed) as a banner', async () => {
    stubAll({
      'POST /api/v1/credit-memos': () => new Response(JSON.stringify({ error: { code: 'conflict', message: 'line 1 would credit more than was billed', details: [{ code: 'exceeds_billed', message: 'x' }] }, meta: {} }), { status: 409, headers: { 'Content-Type': 'application/json' } }),
    })
    el = await mountAsync<GableCreditMemoForm>('gable-credit-memo-form')
    const qty = el.querySelector('input[aria-label="Credit quantity for LUM-248-PREM"]') as HTMLInputElement
    qty.value = '1'
    qty.dispatchEvent(new Event('input'))
    const reason = el.querySelector('#cm-reason') as HTMLTextAreaElement
    reason.value = 'x'
    reason.dispatchEvent(new Event('input'))
    await el.updateComplete
    ;(Array.from(el.querySelectorAll('button')).find(b => b.textContent?.includes('Create draft')) as HTMLButtonElement).click()
    await flush()
    await flush()
    await el.updateComplete
    expect(text(el.querySelector('[data-testid="form-error"]'))).toContain('more than was billed')
  })

  it('edits a draft: prefilled from its lines, saved on its revision', async () => {
    window.history.replaceState(null, '', '/credit-memos/m9/edit')
    const draft = creditMemo({ id: 'm9', revision: 5, lines: [creditLine({ quantity: '-2', restock: false })] })
    const state = stubAll({
      '/api/v1/credit-memos/m9': (_u, init) => (init?.method === 'PUT' ? creditMemo({ id: 'm9', revision: 6 }) : draft),
    })
    el = await mountAsync<GableCreditMemoForm>('gable-credit-memo-form', { routeId: 'm9' })
    expect(text(el.querySelector('h1'))).toBe('Edit draft credit memo')
    const qty = el.querySelector('input[aria-label="Credit quantity for LUM-248-PREM"]') as HTMLInputElement
    expect(qty.value).toBe('2')
    expect((el.querySelector('input[aria-label="Restock LUM-248-PREM"]') as HTMLInputElement).checked).toBe(false)
    ;(Array.from(el.querySelectorAll('button')).find(b => b.textContent?.includes('Save draft')) as HTMLButtonElement).click()
    await flush()
    await flush()
    const put = state.calls.find(c => c.init?.method === 'PUT')!
    const body = JSON.parse(String(put.init!.body))
    expect(body.revision).toBe(5)
    expect((put.init!.headers as Headers).get('If-Match')).toBe('"5"')
    expect(body.lines).toEqual([{ invoice_line_id: LINE.id, quantity: '-2', restock: false }])
  })

  it('refuses to edit a memo that is no longer a draft', async () => {
    window.history.replaceState(null, '', '/credit-memos/m9/edit')
    stubAll({ '/api/v1/credit-memos/m9': () => creditMemo({ id: 'm9', status: 'open', number: 'CM-000001' }) })
    el = await mountAsync<GableCreditMemoForm>('gable-credit-memo-form', { routeId: 'm9' })
    expect(text(el)).toContain('Only a draft credit memo can be edited')
  })
})
