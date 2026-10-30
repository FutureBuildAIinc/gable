// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The accounts list's credit/cash split: the server has no credit filter, so the page splits the
 * loaded rows. Credit means no limit (null) or a limit above 0; cash means a limit of exactly 0.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { matchesCreditFilter } from './AccountsPage'
import type { GableAccountsPage } from './AccountsPage'
import { CustomerService } from '../../services/CustomerService'
import { flush, mountAsync, q, update } from '../../test/dom'
import type { Customer } from '../../types/customer'

afterEach(() => {
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('matchesCreditFilter', () => {
  it('all keeps every row', () => {
    for (const v of [null, 0, 1, 500000]) expect(matchesCreditFilter('all', v)).toBe(true)
  })

  it('credit is a null limit or a limit above zero', () => {
    expect(matchesCreditFilter('credit', null)).toBe(true)
    expect(matchesCreditFilter('credit', 1)).toBe(true)
    expect(matchesCreditFilter('credit', 0)).toBe(false)
  })

  it('cash is a limit of exactly zero, never a null limit', () => {
    expect(matchesCreditFilter('cash', 0)).toBe(true)
    expect(matchesCreditFilter('cash', null)).toBe(false)
    expect(matchesCreditFilter('cash', 100)).toBe(false)
  })
})

describe('the accounts list at 390', () => {
  const customer = {
    id: 'c-1', name: 'Northside Builders', email: 'ap@northside.test', account_number: 'ACC-0001',
    balance_cents: 125000, credit_limit_cents: 500000, is_active: true,
  } as Customer

  it('stacks each row under sm as labelled lines, and shows the header row from sm', async () => {
    vi.spyOn(CustomerService, 'listCustomers').mockResolvedValue({ items: [customer], next_cursor: null, total: 1 } as never)
    const el = await mountAsync<GableAccountsPage>('gable-accounts-page')
    await flush()
    await update(el, {})
    const row = q(el, '[data-testid="account-row-c-1"]')
    expect(row.className).toContain('flex-col')
    expect(row.className).toContain('sm:grid')
    expect(row.className).toContain('sm:grid-cols-12')
    const labels = [...row.querySelectorAll('[data-label]')].map((c) => c.getAttribute('data-label'))
    expect(labels).toEqual(['Account #', 'Balance Due', 'Credit Limit', 'Status'])
    expect(q(el, '[data-testid="accounts-head"]').className).toContain('hidden sm:grid')
  })
})
