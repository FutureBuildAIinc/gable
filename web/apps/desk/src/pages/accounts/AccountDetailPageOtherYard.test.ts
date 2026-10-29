// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * A customer kept at another yard (ruling A, ADR 0007 section 5.8): the
 * summary and the customer record answer for every user, while the subledger
 * answers 404 outside the customer's yards. The page opens the account, shows
 * the figures and says where the ledger is kept in the ledger's place, with
 * no error toast. The Customer Statement report meets the statement's 404 the
 * same way.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './AccountDetailPage'
import '../reports/CustomerStatementPage'
import { mountAsync, text } from '../../test/dom'
import { routedFetch } from '../../test/salesFixtures'
import { ToastService } from '../../lib/toast-service'
import { branchContext } from '../../services/BranchContext'

const CUSTOMER_ID = '00000000-0000-0000-0000-0000000000c1'
const YARD_A = '00000000-0000-0000-0000-0000000000a1'
const YARD_B = '00000000-0000-0000-0000-0000000000b1'
const missing = () => new Response(JSON.stringify({ error: { code: 'not_found', message: 'account not found' } }),
  { status: 404, headers: { 'Content-Type': 'application/json' } })

const customer = () => ({ id: CUSTOMER_ID, name: 'Other Yard Co', account_number: 'OY-1', credit_limit_cents: 50000, balance_cents: 12000,
  payment_terms: { code: 'NET30', name: 'Net 30' }, po_required: false, effective_currency: 'USD', currency: null,
  tier: 'retail', revision: 1, primary_branch_id: YARD_A, branch_ids: [YARD_A] })

describe('an account kept at another yard', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    ;(branchContext as unknown as { _branches: unknown[] })._branches = []
  })

  it('shows the summary and a calm note in the ledger place, with no toast', async () => {
    ;(branchContext as unknown as { _branches: unknown[] })._branches = [
      { id: YARD_A, name: 'West Kelowna Satellite', code: 'WKS', is_home: false },
      { id: YARD_B, name: 'Main Yard', code: 'MY', is_home: true },
    ]
    const toast = vi.spyOn(ToastService, 'show')
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: () => customer(),
      [`/api/v1/accounts/${CUSTOMER_ID}`]: () => ({ customer_id: CUSTOMER_ID, currency: 'USD', balance_cents: 12000, credit_limit_cents: 50000,
        available_credit_cents: 38000, unapplied_cents: 0 }),
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: () => missing(),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
    const el = await mountAsync('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    expect(text(el)).toContain('Other Yard Co')
    expect(text(el)).toContain('$120.00')
    const note = el.querySelector('[data-testid="ledger-yard-note"]')
    expect(note?.textContent).toContain("This account's ledger is kept at West Kelowna Satellite. Switch the yard to see it.")
    const editBtn = Array.from(el.querySelectorAll('button')).find(b => b.textContent?.trim() === 'Edit Profile') as HTMLButtonElement | undefined
    expect(editBtn?.disabled).toBe(true)
    expect(text(el)).not.toContain('Account not found')
    expect(toast).not.toHaveBeenCalled()
  })

  it('shows the same note on the statement report instead of an error toast', async () => {
    ;(branchContext as unknown as { _branches: unknown[] })._branches = [{ id: YARD_A, name: 'West Kelowna Satellite', code: 'WKS', is_home: false }]
    const toast = vi.spyOn(ToastService, 'show')
    window.history.pushState({}, '', `/reports/customer-statement?customer=${CUSTOMER_ID}`)
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: () => customer(),
      [`/api/v1/ar/customers/${CUSTOMER_ID}/statement`]: () => missing(),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
    const el = await mountAsync('gable-customer-statement')
    await new Promise(r => setTimeout(r, 0))
    await (el as unknown as { updateComplete: Promise<unknown> }).updateComplete
    const note = el.querySelector('[data-testid="statement-yard-note"]')
    expect(note?.textContent).toContain('West Kelowna Satellite')
    expect(toast).not.toHaveBeenCalled()
    window.history.pushState({}, '', '/')
  })

  const summary = () => ({ customer_id: CUSTOMER_ID, currency: 'USD', balance_cents: 12000, credit_limit_cents: 50000,
    available_credit_cents: 38000, unapplied_cents: -7500 })

  async function openTab(el: Element, tab: string) {
    const btn = Array.from(el.querySelectorAll('button')).find(b => b.textContent?.trim() === tab) as HTMLButtonElement
    btn.click()
    await new Promise(r => setTimeout(r, 0))
    await (el as unknown as { updateComplete: Promise<unknown> }).updateComplete
  }

  it('answers the payments tab with the same calm note, never an error toast', async () => {
    ;(branchContext as unknown as { _branches: unknown[] })._branches = [{ id: YARD_A, name: 'West Kelowna Satellite', code: 'WKS', is_home: false }]
    const toast = vi.spyOn(ToastService, 'show')
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: () => customer(),
      [`/api/v1/accounts/${CUSTOMER_ID}`]: () => summary(),
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: () => missing(),
      // The walled lists answer an empty page for a customer kept at another
      // yard, never 404; the tab must not read them at all.
      '/api/v1/payments': () => ({ items: [], next_cursor: null }),
      '/api/v1/invoices': () => ({ items: [], next_cursor: null }),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
    const el = await mountAsync('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    await openTab(el, 'Payments')
    const note = el.querySelector('[data-testid="payments-yard-note"]')
    expect(note?.textContent).toContain('West Kelowna Satellite')
    expect(text(el)).not.toContain('No unapplied cash on this account')
    expect(state.calls.filter(c => c.url.pathname === '/api/v1/payments' || c.url.pathname === '/api/v1/invoices')).toHaveLength(0)
    expect(toast).not.toHaveBeenCalled()
  })

  it('answers the CRM tab with the calm note from the page itself, never reading the walled activities', async () => {
    ;(branchContext as unknown as { _branches: unknown[] })._branches = [{ id: YARD_A, name: 'West Kelowna Satellite', code: 'WKS', is_home: false }]
    const toast = vi.spyOn(ToastService, 'show')
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: () => customer(),
      [`/api/v1/accounts/${CUSTOMER_ID}`]: () => summary(),
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: () => missing(),
      // what the server answers for a customer kept at another yard: an empty page, not 404
      [`/api/v1/customers/${CUSTOMER_ID}/activities`]: () => ({ items: [], next_cursor: null }),
      [`/api/v1/customers/${CUSTOMER_ID}/contacts`]: () => ({ items: [], next_cursor: null }),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
    const el = await mountAsync('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    await openTab(el, 'CRM Activity')
    await new Promise(r => setTimeout(r, 0))
    expect(el.querySelector('[data-testid="activity-yard-note"]')?.textContent).toContain('West Kelowna Satellite')
    expect(state.calls.filter(c => c.url.pathname.endsWith('/activities'))).toHaveLength(0)
    expect(toast).not.toHaveBeenCalled()
  })

  it('keeps a missing customer on the CRM tab an honest error, not a yard note', async () => {
    ;(branchContext as unknown as { _branches: unknown[] })._branches = [{ id: YARD_A, name: 'West Kelowna Satellite', code: 'WKS', is_home: false }]
    const toast = vi.spyOn(ToastService, 'show')
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: () => customer(),
      [`/api/v1/accounts/${CUSTOMER_ID}`]: () => summary(),
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: () => ({ items: [], next_cursor: null }),
      [`/api/v1/customers/${CUSTOMER_ID}/activities`]: () => ({ items: [], next_cursor: null }),
      // the customer was removed after the page opened
      [`/api/v1/customers/${CUSTOMER_ID}/contacts`]: () => missing(),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
    const el = await mountAsync('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    await openTab(el, 'CRM Activity')
    await new Promise(r => setTimeout(r, 0))
    await new Promise(r => setTimeout(r, 0))
    expect(el.querySelector('[data-testid="activity-yard-note"]')).toBeNull()
    expect(toast).toHaveBeenCalledWith('Failed to load activity data', 'error')
  })

  it('follows the yard switcher: the ledger shows once the person is on a yard that keeps it', async () => {
    ;(branchContext as unknown as { _branches: unknown[] })._branches = [{ id: YARD_A, name: 'West Kelowna Satellite', code: 'WKS', is_home: false }]
    let walled = true
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: () => customer(),
      [`/api/v1/accounts/${CUSTOMER_ID}`]: () => summary(),
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: () => walled ? missing() : ({ items: [], next_cursor: null }),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
    const el = await mountAsync('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    expect(el.querySelector('[data-testid="ledger-yard-note"]')).not.toBeNull()
    walled = false
    window.dispatchEvent(new CustomEvent('gable:branch-changed'))
    await new Promise(r => setTimeout(r, 0))
    await new Promise(r => setTimeout(r, 0))
    await (el as unknown as { updateComplete: Promise<unknown> }).updateComplete
    expect(el.querySelector('[data-testid="ledger-yard-note"]')).toBeNull()
    expect(state.calls.filter(c => c.url.pathname.endsWith('/transactions'))).toHaveLength(2)
  })

  it('does not advise a yard switch when none of the person\'s yards keeps the ledger', async () => {
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: () => customer(),
      [`/api/v1/accounts/${CUSTOMER_ID}`]: () => summary(),
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: () => missing(),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
    const el = await mountAsync('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    const note = el.querySelector('[data-testid="ledger-yard-note"]')?.textContent ?? ''
    expect(note).toContain("This account's ledger is kept at another yard.")
    expect(note).not.toContain('Switch')
  })

  const payment = (id: string, number: string) => ({ id, number, customer_id: CUSTOMER_ID, customer_name: 'Other Yard Co',
    branch_id: YARD_A, status: 'posted', revision: 1, currency: 'USD', method: 'check', amount_cents: 7500,
    unapplied_cents: 7500, received_on: '', reference: null, notes: null })

  it('reads the Payments tab again after a yard switch while it is open', async () => {
    ;(branchContext as unknown as { _branches: unknown[] })._branches = [{ id: YARD_A, name: 'West Kelowna Satellite', code: 'WKS', is_home: false }]
    let switched = false
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: () => customer(),
      [`/api/v1/accounts/${CUSTOMER_ID}`]: () => summary(),
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: () => ({ items: [], next_cursor: null }),
      '/api/v1/payments': () => ({ items: [switched ? payment('p2', 'PAY-000002') : payment('p1', 'PAY-000001')], next_cursor: null }),
      '/api/v1/invoices': () => ({ items: [], next_cursor: null }),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
    const el = await mountAsync('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    await openTab(el, 'Payments')
    expect(text(el)).toContain('PAY-000001')
    switched = true
    window.dispatchEvent(new CustomEvent('gable:branch-changed'))
    for (let i = 0; i < 4; i++) await new Promise(r => setTimeout(r, 0))
    await (el as unknown as { updateComplete: Promise<unknown> }).updateComplete
    expect(text(el)).toContain('PAY-000002')
    expect(text(el)).not.toContain('No unapplied cash on this account')
  })

  it('drops a payments answer for the yard the person has just left', async () => {
    ;(branchContext as unknown as { _branches: unknown[] })._branches = [{ id: YARD_A, name: 'West Kelowna Satellite', code: 'WKS', is_home: false }]
    const held: Array<() => void> = []
    let holdPayments = false
    let switched = false
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: () => customer(),
      [`/api/v1/accounts/${CUSTOMER_ID}`]: () => summary(),
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: () => ({ items: [], next_cursor: null }),
      '/api/v1/payments': () => ({ items: [switched ? payment('p2', 'PAY-000002') : payment('p1', 'PAY-000001')], next_cursor: null }),
      '/api/v1/invoices': () => ({ items: [], next_cursor: null }),
    })
    vi.stubGlobal('fetch', vi.fn(async (raw: string, init?: RequestInit) => {
      if (holdPayments && raw.includes('/api/v1/payments') && !switched) {
        const answer = await state.fn(raw, init)
        await new Promise<void>(resolve => held.push(resolve))
        return answer
      }
      return state.fn(raw, init)
    }))
    const el = await mountAsync('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    holdPayments = true
    await openTab(el, 'Payments')
    switched = true
    window.dispatchEvent(new CustomEvent('gable:branch-changed'))
    for (let i = 0; i < 4; i++) await new Promise(r => setTimeout(r, 0))
    held.splice(0).forEach(release => release())
    for (let i = 0; i < 4; i++) await new Promise(r => setTimeout(r, 0))
    await (el as unknown as { updateComplete: Promise<unknown> }).updateComplete
    expect(text(el)).toContain('PAY-000002')
    expect(text(el)).not.toContain('PAY-000001')
  })

  it('a cleared pick retires a statement read still in flight', async () => {
    const held: Array<() => void> = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes('/statement')) {
        await new Promise<void>(resolve => held.push(resolve))
        return new Response(JSON.stringify({ customer_id: CUSTOMER_ID, customer_name: 'Old pick', from: '', to: '', job_id: null, currencies: [] }),
          { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return new Response(JSON.stringify({ items: [], next_cursor: null }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    const el = await mountAsync('gable-customer-statement')
    const anyEl = el as unknown as { customerId: string; statement: { customer_name: string } | null; _loadStatement: () => Promise<void> }
    anyEl.customerId = CUSTOMER_ID
    const first = anyEl._loadStatement()
    await new Promise(r => setTimeout(r, 0))
    anyEl.customerId = ''
    await anyEl._loadStatement()
    held.splice(0).forEach(release => release())
    await first
    expect(anyEl.statement).toBeNull()
  })

  it('keeps an unknown customer id on the statement page an honest error, not a yard note', async () => {
    const toast = vi.spyOn(ToastService, 'show')
    window.history.pushState({}, '', '/reports/customer-statement')
    const state = routedFetch({
      '/api/v1/ar/customers/': () => missing(),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
    const el = await mountAsync('gable-customer-statement')
    ;(el as unknown as { customerId: string }).customerId = '00000000-0000-0000-0000-00000000dead'
    await (el as unknown as { _loadStatement: () => Promise<void> })._loadStatement()
    await (el as unknown as { updateComplete: Promise<unknown> }).updateComplete
    expect(el.querySelector('[data-testid="statement-yard-note"]')).toBeNull()
    expect(toast).toHaveBeenCalledWith('Statement not found for that customer', 'error')
    window.history.pushState({}, '', '/')
  })
})
