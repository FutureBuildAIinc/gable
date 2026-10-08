// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The customer wire (ADR 0001, ADR 0005 section 7): cursor lists, quoted If-Match on every write,
 * the escalation policy's lowercase vocabulary, and the one error envelope. A renamed parameter
 * or a dropped If-Match is a 400 or 428 at runtime, not a compile error, so these tests pin the
 * exact URLs, query strings, headers and bodies.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  CustomerService,
  LIST_ALL_CUSTOMERS_CAP,
  buildCustomerQuery,
  buildChildQuery,
  creditLimitInputText,
  customerRequestFromCustomer,
  parseCreditLimitCents,
} from './CustomerService'
import { ApiError, apiErrorMessage, fieldErrorMap, ifMatch } from './apiError'
import type { Customer } from '../types/customer'

let fetchMock: ReturnType<typeof vi.fn>

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function lastUrl(): URL {
  const raw = fetchMock.mock.calls[fetchMock.mock.calls.length - 1][0] as string
  return new URL(raw, 'http://localhost')
}

function lastInit(): RequestInit {
  return (fetchMock.mock.calls[fetchMock.mock.calls.length - 1][1] ?? {}) as RequestInit
}

function lastHeader(name: string): string | undefined {
  // fetchWithAuth hands fetch a Headers object.
  return (lastInit().headers as Headers).get(name) ?? undefined
}

function lastBody(): Record<string, unknown> {
  return JSON.parse(lastInit().body as string) as Record<string, unknown>
}

const CUSTOMER: Customer = {
  id: 'c-1',
  account_number: 'A-100',
  name: 'Acme Lumber',
  email: null,
  phone: '555-0100',
  address: null,
  tier: 'gold',
  is_active: true,
  primary_branch_id: 'b-1',
  price_level_id: null,
  price_level: null,
  salesperson_id: 's-1',
  salesperson_name: 'Sam',
  credit_limit_cents: 500050,
  balance_cents: 1200,
  currency: null,
  effective_currency: 'USD',
  payment_terms_id: 't-1',
  payment_terms: { id: 't-1', code: 'NET30', name: 'Net 30' },
  po_required: true,
  revision: 4,
  created_at: '2026-01-01T00:00:00.000000Z',
  updated_at: '2026-01-01T00:00:00.000000Z',
}

beforeEach(() => {
  fetchMock = vi.fn(async () => jsonResponse({}))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('queries', () => {
  it('are empty with no params', () => {
    expect(buildCustomerQuery()).toBe('')
    expect(buildChildQuery()).toBe('')
  })

  it('send the declared names only: q, tier, is_active, salesperson_id, limit, cursor, include', () => {
    const q = new URLSearchParams(
      buildCustomerQuery({ q: ' acme ', tier: 'gold', isActive: false, salespersonId: 's-1', limit: 25, cursor: 'abc', includeTotal: true }).slice(1),
    )
    expect(Object.fromEntries(q)).toEqual({
      q: 'acme', tier: 'gold', is_active: 'false', salesperson_id: 's-1', limit: '25', cursor: 'abc', include: 'total',
    })
  })

  it('never sends offset, and drops blank values', () => {
    expect(buildCustomerQuery({ q: '  ', cursor: null })).toBe('')
    expect(buildChildQuery({ isActive: null })).toBe('')
    expect(buildChildQuery({ isActive: true, limit: 200 })).toBe('?is_active=true&limit=200')
  })
})

describe('customers', () => {
  it('lists with the query and returns the page', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [CUSTOMER], next_cursor: 'n1', limit: 50, total: 1 }))
    const page = await CustomerService.listCustomers({ q: 'acme', tier: 'gold', limit: 50, includeTotal: true })
    expect(lastUrl().pathname).toBe('/api/v1/customers')
    expect(lastUrl().search).toBe('?q=acme&tier=gold&limit=50&include=total')
    expect(page.next_cursor).toBe('n1')
    expect(page.items[0].credit_limit_cents).toBe(500050)
  })

  it('listAllCustomers pages by cursor until the last page', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ items: [CUSTOMER], next_cursor: 'p2', limit: 200 }))
      .mockResolvedValueOnce(jsonResponse({ items: [{ ...CUSTOMER, id: 'c-2' }], next_cursor: null, limit: 200 }))
    const all = await CustomerService.listAllCustomers()
    expect(all.map(c => c.id)).toEqual(['c-1', 'c-2'])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(new URL(fetchMock.mock.calls[0][0] as string, 'http://localhost').search).toBe('?limit=200')
    expect(new URL(fetchMock.mock.calls[1][0] as string, 'http://localhost').search).toBe('?limit=200&cursor=p2')
  })

  it('listAllCustomers stops at the cap even when the server keeps offering a cursor', async () => {
    const full = Array.from({ length: 200 }, (_, i) => ({ ...CUSTOMER, id: `c-${i}` }))
    fetchMock.mockImplementation(async () => jsonResponse({ items: full, next_cursor: 'more', limit: 200 }))
    const all = await CustomerService.listAllCustomers()
    expect(all).toHaveLength(LIST_ALL_CUSTOMERS_CAP)
    expect(fetchMock).toHaveBeenCalledTimes(LIST_ALL_CUSTOMERS_CAP / 200)
  })

  it('gets one customer', async () => {
    await CustomerService.getCustomer('c-1')
    expect(lastUrl().pathname).toBe('/api/v1/customers/c-1')
  })

  it('creates with POST and no If-Match', async () => {
    await CustomerService.createCustomer({ account_number: 'A-1', name: 'N', credit_limit_cents: null, payment_terms_id: 't-2' })
    expect(lastUrl().pathname).toBe('/api/v1/customers')
    expect(lastInit().method).toBe('POST')
    expect(lastHeader('If-Match')).toBeUndefined()
    expect(lastBody()).toEqual({ account_number: 'A-1', name: 'N', credit_limit_cents: null, payment_terms_id: 't-2' })
  })

  it('replaces the header with PUT and the quoted revision, the whole header', async () => {
    await CustomerService.updateCustomer('c-1', customerRequestFromCustomer(CUSTOMER), 4)
    expect(lastUrl().pathname).toBe('/api/v1/customers/c-1')
    expect(lastInit().method).toBe('PUT')
    expect(lastHeader('If-Match')).toBe('"4"')
    const body = lastBody()
    expect(body).toEqual({
      account_number: 'A-100', name: 'Acme Lumber', email: null, phone: '555-0100', address: null, tier: 'gold', is_active: true,
      price_level_id: null, salesperson_id: 's-1', credit_limit_cents: 500050, currency: null, payment_terms_id: 't-1', po_required: true,
    })
    // Read only and create only fields never travel on a PUT.
    for (const f of ['balance_cents', 'revision', 'primary_branch_id', 'id', 'effective_currency', 'payment_terms']) {
      expect(body).not.toHaveProperty(f)
    }
  })

  it('assigns a salesperson with PATCH, the key present, and the revision; null unassigns', async () => {
    await CustomerService.updateSalesperson('c-1', 's-2', 5)
    expect(lastUrl().pathname).toBe('/api/v1/customers/c-1/salesperson')
    expect(lastInit().method).toBe('PATCH')
    expect(lastHeader('If-Match')).toBe('"5"')
    expect(lastBody()).toEqual({ salesperson_id: 's-2' })
    await CustomerService.updateSalesperson('c-1', null, 6)
    expect(lastBody()).toEqual({ salesperson_id: null })
  })
})

describe('escalation policy', () => {
  it('reads the policy with GET', async () => {
    await CustomerService.getEscalationPolicy('c-1')
    expect(lastUrl().pathname).toBe('/api/v1/customers/c-1/escalation-policy')
    expect(lastInit().method ?? 'GET').toBe('GET')
  })

  it('writes the policy with PUT, the lowercase vocabulary, a decimal string percent and the customer revision', async () => {
    // The backend registers PUT only; a POST would 405 and the operator would see "saved" while nothing changed.
    await CustomerService.setEscalationPolicy('c-1', { policy: 'require_ack', threshold_percent: '4.5' }, 7)
    expect(lastInit().method).toBe('PUT')
    expect(lastHeader('If-Match')).toBe('"7"')
    expect(lastBody()).toEqual({ policy: 'require_ack', threshold_percent: '4.5' })
  })
})

describe('ship-tos', () => {
  it('lists under the customer, with the active filter', async () => {
    await CustomerService.listShipTos('c-1', { isActive: true, limit: 200 })
    expect(lastUrl().pathname).toBe('/api/v1/customers/c-1/ship-tos')
    expect(lastUrl().search).toBe('?is_active=true&limit=200')
  })

  it('reads one at /api/v1/ship-tos/{id}', async () => {
    await CustomerService.getShipTo('st-1')
    expect(lastUrl().pathname).toBe('/api/v1/ship-tos/st-1')
  })

  it('creates with POST and no If-Match', async () => {
    await CustomerService.createShipTo('c-1', { code: 'YARD1', name: 'Yard', line1: '1 Main', tax_rate_percent: '8.875', is_default: true })
    expect(lastUrl().pathname).toBe('/api/v1/customers/c-1/ship-tos')
    expect(lastInit().method).toBe('POST')
    expect(lastHeader('If-Match')).toBeUndefined()
    expect(lastBody()).toEqual({ code: 'YARD1', name: 'Yard', line1: '1 Main', tax_rate_percent: '8.875', is_default: true })
  })

  it('replaces with PUT and the ship-to revision', async () => {
    await CustomerService.updateShipTo('st-1', { code: 'YARD1', name: 'Yard', line1: '1 Main', is_active: false }, 3)
    expect(lastUrl().pathname).toBe('/api/v1/ship-tos/st-1')
    expect(lastInit().method).toBe('PUT')
    expect(lastHeader('If-Match')).toBe('"3"')
    expect(lastBody().is_active).toBe(false)
  })
})

describe('contacts', () => {
  it('lists under the customer', async () => {
    await CustomerService.listContacts('c-1', { limit: 200 })
    expect(lastUrl().pathname).toBe('/api/v1/customers/c-1/contacts')
    expect(lastUrl().search).toBe('?limit=200')
  })

  it('creates with POST, carrying the authority fields', async () => {
    await CustomerService.createContact('c-1', { first_name: 'A', last_name: 'B', can_place_orders: false, order_limit_cents: 10000 })
    expect(lastInit().method).toBe('POST')
    expect(lastBody()).toEqual({ first_name: 'A', last_name: 'B', can_place_orders: false, order_limit_cents: 10000 })
  })

  it('replaces with PUT and the contact revision', async () => {
    await CustomerService.updateContact('ct-1', { first_name: 'A', last_name: 'B' }, 2)
    expect(lastUrl().pathname).toBe('/api/v1/contacts/ct-1')
    expect(lastInit().method).toBe('PUT')
    expect(lastHeader('If-Match')).toBe('"2"')
  })

  it('deletes with the revision in If-Match and no body, and accepts the 204', async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await expect(CustomerService.deleteContact('ct-1', 2)).resolves.toBeUndefined()
    expect(lastUrl().pathname).toBe('/api/v1/contacts/ct-1')
    expect(lastInit().method).toBe('DELETE')
    expect(lastHeader('If-Match')).toBe('"2"')
    expect(lastInit().body).toBeUndefined()
  })
})

describe('reference lists', () => {
  it('payment terms are the active ones by default', async () => {
    await CustomerService.listPaymentTerms()
    expect(lastUrl().pathname).toBe('/api/v1/payment-terms')
    expect(lastUrl().search).toBe('?is_active=true')
    await CustomerService.listPaymentTerms({ limit: 200 })
    expect(lastUrl().search).toBe('?is_active=true&limit=200')
  })

  it('payment terms can ask for all of them', async () => {
    await CustomerService.listPaymentTerms({ isActive: null })
    expect(lastUrl().search).toBe('')
  })

  it('price levels are an envelope and take no is_active', async () => {
    await CustomerService.listPriceLevels({ isActive: true, limit: 10 })
    expect(lastUrl().pathname).toBe('/api/v1/price_levels')
    expect(lastUrl().search).toBe('?limit=10')
  })
})

describe('error envelope', () => {
  it('parses code, message, details and request id into the one error type', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({
      error: {
        code: 'validation_failed',
        message: 'one or more fields failed validation',
        details: [
          { field: 'account_number', message: 'is required' },
          { field: 'tax_rate_percent', message: 'must be between 0 and 100' },
        ],
      },
      meta: { request_id: 'req-3' },
    }, 400))
    const err = await CustomerService.createCustomer({ account_number: '', name: 'x' }).catch(e => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(400)
    expect(err.code).toBe('validation_failed')
    expect(err.requestId).toBe('req-3')
    expect(fieldErrorMap(err)).toEqual({ account_number: 'is required', tax_rate_percent: 'must be between 0 and 100' })
    expect(apiErrorMessage(err)).toContain('account_number: is required')
  })

  it('flags a stale revision', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { code: 'stale_revision', message: 'changed' }, meta: { request_id: 'r' } }, 409))
    const err = await CustomerService.updateCustomer('c-1', { account_number: 'A', name: 'N' }, 1).catch(e => e)
    expect(err.isStaleRevision).toBe(true)
    expect(err.status).toBe(409)
  })

  it('names a duplicate account number', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({
      error: { code: 'duplicate', message: 'account number already in use', details: [{ code: 'account_number_taken', message: 'taken' }] },
      meta: { request_id: 'r' },
    }, 409))
    const err = await CustomerService.createCustomer({ account_number: 'A', name: 'N' }).catch(e => e)
    expect(err.code).toBe('duplicate')
    expect(err.details[0].code).toBe('account_number_taken')
  })

  it('falls back when the body is not JSON', async () => {
    fetchMock.mockResolvedValueOnce(new Response('upstream down', { status: 502 }))
    const err = await CustomerService.getCustomer('c-1').catch(e => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.message).toBe('Failed to fetch customer')
    expect(err.code).toBe('unknown_error')
  })

  it('has no field map for an error with no details or a plain Error', () => {
    expect(fieldErrorMap(new Error('x'))).toEqual({})
    expect(fieldErrorMap(new ApiError(409, 'conflict', 'm'))).toEqual({})
  })
})

describe('ifMatch', () => {
  it('quotes the revision as an ETag is', () => {
    expect(ifMatch(12)).toBe('"12"')
  })
})

describe('parseCreditLimitCents', () => {
  const cents = (t: string) => {
    const r = parseCreditLimitCents(t)
    return r.ok ? r.cents : `error: ${r.message}`
  }

  it('reads empty and blank as no limit (null), and 0 as no credit', () => {
    expect(cents('')).toBeNull()
    expect(cents('   ')).toBeNull()
    expect(cents('$ ')).toBeNull()
    expect(cents('0')).toBe(0)
    expect(cents('0.00')).toBe(0)
  })

  it('converts dollars to cents with no float error', () => {
    expect(cents('5000')).toBe(500000)
    expect(cents('5000.5')).toBe(500050)
    expect(cents('5000.50')).toBe(500050)
    expect(cents('0.07')).toBe(7)
    expect(cents('0.29')).toBe(29)
    expect(cents('19.99')).toBe(1999)
    expect(cents('1.10')).toBe(110)
    expect(cents('.5')).toBe(50)
    expect(cents('5.')).toBe(500)
    expect(cents('007')).toBe(700)
  })

  it('tolerates a dollar sign, commas and surrounding spaces', () => {
    expect(cents(' $1,234,567.89 ')).toBe(123456789)
  })

  it('refuses a negative, a third fraction digit, letters, a second point and a lone point', () => {
    expect(cents('-1')).toMatch(/^error: .*negative/)
    expect(cents('1.005')).toMatch(/^error: .*two decimal/)
    expect(cents('12abc')).toMatch(/^error/)
    expect(cents('1.2.3')).toMatch(/^error/)
    expect(cents('.')).toMatch(/^error/)
    expect(cents('1e3')).toMatch(/^error/)
  })

  it('refuses an amount too large for safe integer cents', () => {
    expect(cents('9999999999999.99')).toBe(999999999999999)
    expect(cents('10000000000000')).toMatch(/^error: .*too large/)
  })
})

describe('creditLimitInputText', () => {
  it('round trips what the parser reads', () => {
    expect(creditLimitInputText(null)).toBe('')
    expect(creditLimitInputText(undefined)).toBe('')
    expect(creditLimitInputText(0)).toBe('0.00')
    expect(creditLimitInputText(7)).toBe('0.07')
    expect(creditLimitInputText(500050)).toBe('5000.50')
    for (const c of [0, 1, 99, 100, 123456789]) {
      const r = parseCreditLimitCents(creditLimitInputText(c))
      expect(r.ok && r.cents).toBe(c)
    }
  })
})
