// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The ledger tab walks the subledger by cursor: the first page shows with a
 * "Load more" while the account has older rows, the button reads the next page
 * with the cursor and appends it, and it goes away on the last page. Amounts
 * carry the thousands separator (formatCents), not a bare toFixed.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './AccountDetailPage'
import { mountAsync, text, flush, q } from '../../test/dom'
import { routedFetch } from '../../test/salesFixtures'

const CUSTOMER_ID = '00000000-0000-0000-0000-0000000000c1'

function txn(n: number, cents: number) {
  return { id: `t-${n}`, customer_id: CUSTOMER_ID, type: 'INVOICE', amount_cents: cents, balance_after_cents: cents,
    reference_id: null, description: `row ${n}`, created_at: '2031-03-09T08:00:00Z' }
}

describe('gable-account-detail ledger', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('offers Load more on a first page with a cursor and appends the next page', async () => {
    const state = routedFetch({
      [`/api/v1/customers/${CUSTOMER_ID}`]: () => ({ id: CUSTOMER_ID, name: 'Ledger Co', account_number: 'L-1', credit_limit_cents: null, balance_cents: 0,
        payment_terms: { code: 'NET30', name: 'Net 30' }, po_required: false, effective_currency: 'USD', currency: null }),
      [`/api/v1/accounts/${CUSTOMER_ID}`]: () => ({ customer_id: CUSTOMER_ID, currency: 'USD', balance_cents: 0, credit_limit_cents: null, available_credit_cents: null, unapplied_cents: 0 }),
      [`/api/v1/accounts/${CUSTOMER_ID}/transactions`]: (url) => url.searchParams.get('cursor') === 'p2'
        ? { items: [txn(3, -250)], next_cursor: null, limit: 50 }
        : { items: [txn(1, 123456), txn(2, 100)], next_cursor: 'p2', limit: 50 },
    })
    vi.stubGlobal('fetch', vi.fn(state.fn))
    const el = await mountAsync('gable-account-detail', { routeId: CUSTOMER_ID } as never)
    expect(text(el)).toContain('row 1')
    expect(text(el)).toContain('+$1,234.56')
    expect(text(el)).not.toContain('row 3')

    q<HTMLButtonElement>(el, '[data-testid="ledger-load-more"]').click()
    await flush()
    expect(text(el)).toContain('row 3')
    expect(text(el)).toContain('row 1')
    expect(el.querySelector('[data-testid="ledger-load-more"]')).toBeNull()
    const cursors = state.calls.filter(c => c.url.pathname.endsWith('/transactions')).map(c => c.url.searchParams.get('cursor'))
    expect(cursors).toEqual([null, 'p2'])
  })
})
