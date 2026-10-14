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
})
