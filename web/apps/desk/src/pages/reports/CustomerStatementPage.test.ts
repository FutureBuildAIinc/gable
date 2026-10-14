// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The customer statement page's lookup form. The Old version took a UUID pasted
 * into a text field: a backend-typed ID, not a name or account number, and a
 * dead end for any user who did not have the UUID at hand. The new form is a
 * picker: type a name or account number, pick from the matches (the picker prints
 * both name and account number), and Generate runs for the chosen customer.
 *
 * The page also reads `?customer=<id>` from the URL and preselects that
 * customer (the Account page's "Statement" action lands the user here with
 * that query string set). The preselect runs the statement for the picked
 * customer without making the user type and pick again.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './CustomerStatementPage'
import type { CustomerStatementPage } from './CustomerStatementPage'
import { mountAsync, text as textOf, jsonResponse } from '../../test/dom'

const CUSTOMER_ID = '00000000-0000-4000-8000-0000000000c1'
const OTHER_ID = '00000000-0000-4000-8000-0000000000c2'

function customer(overrides: Record<string, unknown> = {}) {
  return {
    id: CUSTOMER_ID,
    account_number: 'AC-001',
    name: 'Ridgeview Framing',
    email: null,
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
    ...overrides,
  }
}

function statement(customerId: string) {
  return {
    customer_id: customerId,
    customer_name: 'Ridgeview Framing',
    from: '2031-01-01',
    to: '2031-12-31',
    currencies: [
      {
        currency: 'USD',
        opening_balance_cents: 0,
        closing_balance_cents: 0,
        lines: [],
        open_documents: [],
        unapplied_payments: [] as { id: string; number: string; received_on: string; unapplied_cents: number }[],
        unapplied_cents: 0,
        net_balance_cents: 0,
      },
    ],
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

function stubRoutes(routes: Record<string, unknown>) {
  // Specific fragments must be checked before broad ones, or a stub for
  // `/customers?q=Ridge` will swallow every unrelated customers call.
  const keys = Object.keys(routes).sort((a, b) => b.length - a.length)
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    for (const fragment of keys) {
      if (url.includes(fragment)) return jsonResponse(routes[fragment])
    }
    return jsonResponse([])
  }))
}

describe('customer statement lookup', () => {
  it('does not ask for a UUID: it asks for a name or account number', async () => {
    stubRoutes({})
    const el = await mountAsync<CustomerStatementPage>('gable-customer-statement')
    expect(textOf(el)).toMatch(/name|account/i)
    expect(textOf(el)).not.toMatch(/uuid/i)
  })
})

describe('customer statement search and pick', () => {
  it('matches by name and lists each candidate with its account number', async () => {
    stubRoutes({
      '/customers?q=Ridge': {
        items: [
          customer({ id: CUSTOMER_ID, name: 'Ridgeview Framing', account_number: 'AC-001' }),
          customer({ id: OTHER_ID, name: 'Ridge Stone LLC', account_number: 'AC-002' }),
        ],
        next_cursor: null,
        limit: 200,
      },
    })
    const el = await mountAsync<CustomerStatementPage>('gable-customer-statement')
    const input = el.querySelector('input[type="text"]') as HTMLInputElement
    expect(input, 'expected a text input for the search').toBeDefined()
    input.value = 'Ridge'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await el.updateComplete
    await new Promise((r) => setTimeout(r, 250))
    await el.updateComplete
    await new Promise((r) => setTimeout(r, 0))
    await el.updateComplete

    expect(textOf(el)).toContain('Ridgeview Framing')
    expect(textOf(el)).toContain('AC-001')
    expect(textOf(el)).toContain('AC-002')
  })

  it('runs the statement for the picked customer, not the typed text', async () => {
    stubRoutes({
      '/customers?q=Ridge': {
        items: [
          customer({ id: CUSTOMER_ID, name: 'Ridgeview Framing', account_number: 'AC-001' }),
          customer({ id: OTHER_ID, name: 'Ridge Stone LLC', account_number: 'AC-002' }),
        ],
        next_cursor: null,
        limit: 200,
      },
      [`/ar/customers/${CUSTOMER_ID}/statement`]: statement(CUSTOMER_ID),
    })
    const el = await mountAsync<CustomerStatementPage>('gable-customer-statement')

    // Type and pick.
    const input = el.querySelector('input[type="text"]') as HTMLInputElement
    input.value = 'Ridge'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await el.updateComplete
    await new Promise((r) => setTimeout(r, 250))
    await el.updateComplete
    await new Promise((r) => setTimeout(r, 0))
    await el.updateComplete

    const pick = Array.from(el.querySelectorAll('[data-testid="customer-match"]')).find(
      (b) => textOf(b as Element).includes('Ridgeview Framing'),
    ) as HTMLButtonElement
    expect(pick, 'expected a pick button for the customer').toBeDefined()
    pick.click()
    await el.updateComplete
    await new Promise((r) => setTimeout(r, 0))
    await el.updateComplete

    // Generate is the trigger that fetches the statement.
    const generate = el.querySelector('[data-testid="generate-statement"]') as HTMLButtonElement
    expect(generate).toBeDefined()
    generate.click()
    await el.updateComplete
    await new Promise((r) => setTimeout(r, 0))
    await el.updateComplete

    const calls = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls as [string, ...unknown[]][]
    const statementCall = calls.find(([url]) => typeof url === 'string' && url.includes(`/ar/customers/${CUSTOMER_ID}/statement`))
    expect(statementCall, 'expected the statement to be fetched for the picked customer').toBeDefined()
    expect(calls.find(([url]) => typeof url === 'string' && url.includes(`/ar/customers/${OTHER_ID}/statement`))).toBeUndefined()
  })
})

describe('customer statement preselect via ?customer=<id>', () => {
  it('preselects the customer from the URL and runs the statement for them', async () => {
    stubRoutes({
      [`/api/v1/customers/${CUSTOMER_ID}`]: customer({ id: CUSTOMER_ID }),
      [`/ar/customers/${CUSTOMER_ID}/statement`]: statement(CUSTOMER_ID),
    })
    // Mount with a location whose search string carries the preselect id.
    history.replaceState(null, '', `?customer=${CUSTOMER_ID}`)
    try {
      const el = await mountAsync<CustomerStatementPage>('gable-customer-statement')
      await new Promise((r) => setTimeout(r, 0))
      await el.updateComplete
      await new Promise((r) => setTimeout(r, 0))
      await el.updateComplete

      const calls = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls as [string, ...unknown[]][]
      const statementCall = calls.find(
        ([url]) => typeof url === 'string' && url.includes(`/ar/customers/${CUSTOMER_ID}/statement`),
      )
      expect(statementCall, 'expected the statement to be fetched for the URL customer').toBeDefined()

      // The picker shows the preselect (name and account number) so the user
      // can see who the statement is for before it loads.
      expect(textOf(el)).toContain('Ridgeview Framing')
      expect(textOf(el)).toContain('AC-001')
    } finally {
      history.replaceState(null, '', '/')
    }
  })
})

describe('customer statement print', () => {
  it('opens the statement document for the period shown and never prints the page', async () => {
    stubRoutes({
      [`/api/v1/customers/${CUSTOMER_ID}`]: customer({ id: CUSTOMER_ID }),
      [`/ar/customers/${CUSTOMER_ID}/statement`]: statement(CUSTOMER_ID),
    })
    const open = vi.fn()
    const print = vi.fn()
    vi.stubGlobal('open', open)
    vi.stubGlobal('print', print)
    history.replaceState(null, '', `?customer=${CUSTOMER_ID}`)
    try {
      const el = await mountAsync<CustomerStatementPage>('gable-customer-statement')
      for (let i = 0; i < 4; i++) {
        await new Promise((r) => setTimeout(r, 0))
        await el.updateComplete
      }
      const button = Array.from(el.querySelectorAll('button')).find((b) => textOf(b).trim() === 'Print') as HTMLButtonElement
      expect(button, 'expected a Print button once the statement is shown').toBeDefined()
      button.click()

      expect(print).not.toHaveBeenCalled()
      expect(open).toHaveBeenCalledTimes(1)
      const url = String(open.mock.calls[0][0])
      expect(url).toContain(`/api/v1/documents/print/statement/${CUSTOMER_ID}`)
      expect(url).toContain('from=2031-01-01')
      expect(url).toContain('to=2031-12-31')
    } finally {
      history.replaceState(null, '', '/')
    }
  })
})

describe('customer statement unapplied cash', () => {
  it('lists the payments still unapplied and states the net balance beside the closing balance', async () => {
    const st = statement(CUSTOMER_ID)
    st.currencies[0] = {
      ...st.currencies[0],
      closing_balance_cents: 100000,
      unapplied_payments: [{ id: '00000000-0000-4000-8000-0000000000p1', number: 'PAY-000042', received_on: '2031-12-20', unapplied_cents: 50000 }],
      unapplied_cents: 50000,
      net_balance_cents: 50000,
    }
    stubRoutes({
      [`/api/v1/customers/${CUSTOMER_ID}`]: customer({ id: CUSTOMER_ID }),
      [`/ar/customers/${CUSTOMER_ID}/statement`]: st,
    })
    history.replaceState(null, '', `?customer=${CUSTOMER_ID}`)
    try {
      const el = await mountAsync<CustomerStatementPage>('gable-customer-statement')
      for (let i = 0; i < 4; i++) {
        await new Promise((r) => setTimeout(r, 0))
        await el.updateComplete
      }
      const body = textOf(el)
      expect(body).toContain('PAY-000042')
      expect(body).toContain('2031-12-20')
      expect(body).toContain('$500.00')
      expect(body).toMatch(/net balance/i)
    } finally {
      history.replaceState(null, '', '/')
    }
  })
})
