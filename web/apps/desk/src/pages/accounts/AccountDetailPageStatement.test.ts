// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The account detail page's Statement action. Until now, going from an
 * account to a statement took pasting the customer's UUID into the statement
 * page's lookup box: the bridge that disappears when the URL takes the user
 * somewhere with no copy. The Statement button on the account detail page
 * goes straight to the statement with `?customer=<id>` set, and the
 * statement page preselects from that.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './AccountDetailPage'
import type { GableAccountDetail } from './AccountDetailPage'
import { mountAsync, text as textOf } from '../../test/dom'
import { routedFetch } from '../../test/salesFixtures'

const CUSTOMER_ID = '00000000-0000-4000-8000-0000000000c1'

function customer() {
  return {
    id: CUSTOMER_ID,
    name: 'Ridgeview Framing',
    account_number: 'AC-001',
    email: 'ap@ridge.test',
    phone: null,
    address: null,
    tier: 'retail',
    is_active: true,
    primary_branch_id: '00000000-0000-4000-8000-0000000000b1',
    price_level_id: null,
    price_level: null,
    salesperson_id: null,
    salesperson_name: null,
    credit_limit_cents: null,
    balance_cents: 0,
    currency: null,
    effective_currency: 'USD',
    payment_terms_id: '00000000-0000-4000-8000-0000000000t1',
    payment_terms: { code: 'NET30', name: 'Net 30' },
    po_required: false,
    revision: 1,
    created_at: '2031-03-09T08:00:00Z',
    updated_at: '2031-03-09T08:00:00Z',
  }
}

function summary() {
  return {
    customer_id: CUSTOMER_ID,
    currency: 'USD',
    balance_cents: 0,
    credit_limit_cents: null,
    available_credit_cents: null,
    unapplied_cents: 0,
  }
}

describe('account detail page: Statement action', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    history.replaceState(null, '', '/')
  })

  it('offers a Statement action on the header next to Edit Profile', async () => {
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: customer,
      [`/api/v1/accounts/${CUSTOMER_ID}`]: summary,
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: () => ({ items: [], next_cursor: null, limit: 50 }),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))

    const el = await mountAsync<GableAccountDetail>('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    expect(textOf(el)).toMatch(/statement/i)
  })

  it('the Statement action is a link with ?customer=<id> set, not a dead button', async () => {
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: customer,
      [`/api/v1/accounts/${CUSTOMER_ID}`]: summary,
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: () => ({ items: [], next_cursor: null, limit: 50 }),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))

    const el = await mountAsync<GableAccountDetail>('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    const statementLink = Array.from(el.querySelectorAll('a')).find((a) =>
      textOf(a).toLowerCase().includes('statement'),
    ) as HTMLAnchorElement | undefined
    expect(statementLink, 'expected a Statement link').toBeDefined()
    expect(statementLink!.getAttribute('href')).toBe(`/reports/customer-statement?customer=${CUSTOMER_ID}`)
  })
  it('offers no control without a target and no unfinished tab', async () => {
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: customer,
      [`/api/v1/accounts/${CUSTOMER_ID}`]: summary,
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: () => ({ items: [], next_cursor: null, limit: 50 }),
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))

    const el = await mountAsync<GableAccountDetail>('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    const buttons = Array.from(el.querySelectorAll('button')).map((b) => textOf(b).trim())
    expect(buttons).not.toContain('New Transaction')
    expect(buttons).not.toContain('Invoices')
    expect(textOf(el)).not.toMatch(/under construction/i)
  })
})