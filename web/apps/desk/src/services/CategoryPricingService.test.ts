// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The category pricing wire (ADR 0001, ADR 0006 section 7.3): lowercase types, the two readings of a
 * rule's value, a quoted If-Match on every rule PUT and DELETE, envelope lists and the error envelope.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  LIST_ALL_RULES_CAP,
  buildRuleQuery,
  categoryPricingService,
  formatRuleValue,
  percentInputText,
  ruleUpdateFromRule,
  ruleValueInputText,
  ruleValuesFromInput,
} from './CategoryPricingService'
import { ApiError } from './apiError'
import type { CategoryPricingRule } from '../types/category-pricing'

let fetchMock: ReturnType<typeof vi.fn>

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function errorResponse(status: number, code: string, message: string, details: { field: string; message: string }[] = []): Response {
  return jsonResponse({ error: { code, message, details }, meta: { request_id: 'req-1' } }, status)
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

function lastBody(): unknown {
  return JSON.parse(lastInit().body as string)
}

beforeEach(() => {
  fetchMock = vi.fn(async () => jsonResponse({}))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const RULE: CategoryPricingRule = {
  id: 'r-1',
  target_type: 'tier',
  customer_id: null,
  tier: 'GOLD',
  category_id: 'cat-1',
  rule_type: 'markdown',
  value_ten_thousandths: null,
  value_pct: '15.0000',
  margin_floor_pct: '20.0000',
  starts_at: '2026-03-01T00:00:00Z',
  expires_at: null,
  is_active: true,
  priority: 2,
  created_by: 'system',
  revision: 6,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

describe('buildRuleQuery', () => {
  it('sends the lowercase filters and the paging names only', () => {
    expect(buildRuleQuery()).toBe('')
    const q = new URLSearchParams(buildRuleQuery({ target_type: 'account', tier: 'GOLD', customer_id: 'c', category_id: 'k', is_active: false, limit: 200, cursor: 'x', includeTotal: true }).slice(1))
    expect(Object.fromEntries(q)).toEqual({ target_type: 'account', tier: 'GOLD', customer_id: 'c', category_id: 'k', is_active: 'false', limit: '200', cursor: 'x', include: 'total' })
    expect(buildRuleQuery({ cursor: null })).toBe('')
  })
})

describe('rule value text', () => {
  it('formats a percent rule by type and a fixed rule as a price', () => {
    expect(formatRuleValue(RULE)).toBe('-15%')
    expect(formatRuleValue({ ...RULE, rule_type: 'markup', value_pct: '12.5000' })).toBe('+12.5%')
    expect(formatRuleValue({ ...RULE, rule_type: 'margin', value_pct: '30.0000' })).toBe('M30%')
    expect(formatRuleValue({ ...RULE, rule_type: 'fixed', value_pct: null, value_ten_thousandths: 55000 })).toBe('$5.50')
    expect(formatRuleValue({ ...RULE, rule_type: 'fixed', value_pct: null, value_ten_thousandths: 13725 })).toBe('$1.3725')
  })

  it('shows a stored value in an input box without trailing zeros', () => {
    expect(percentInputText('15.0000')).toBe('15')
    expect(percentInputText('12.5000')).toBe('12.5')
    expect(percentInputText('100')).toBe('100')
    expect(percentInputText(null)).toBe('')
    expect(ruleValueInputText(RULE)).toBe('15')
    expect(ruleValueInputText({ rule_type: 'fixed', value_ten_thousandths: 55000, value_pct: null })).toBe('5.50')
    expect(ruleValueInputText(null)).toBe('')
    expect(ruleValueInputText({})).toBe('')
  })
})

describe('ruleValuesFromInput', () => {
  it('reads a percent rule as value_pct, a decimal string, and never a float', () => {
    expect(ruleValuesFromInput('markdown', '12.5')).toEqual({ ok: true, values: { rule_type: 'markdown', value_pct: '12.5' } })
    expect(ruleValuesFromInput('markup', '1.1')).toEqual({ ok: true, values: { rule_type: 'markup', value_pct: '1.1' } })
    expect(ruleValuesFromInput('margin', '30', '20.50')).toEqual({ ok: true, values: { rule_type: 'margin', value_pct: '30', margin_floor_pct: '20.5' } })
  })

  it('reads a fixed rule as dollars to value_ten_thousandths', () => {
    expect(ruleValuesFromInput('fixed', '4.25')).toEqual({ ok: true, values: { rule_type: 'fixed', value_ten_thousandths: 42500 } })
    expect(ruleValuesFromInput('fixed', '1.3725')).toEqual({ ok: true, values: { rule_type: 'fixed', value_ten_thousandths: 13725 } })
  })

  it('refuses blank, signed, malformed and over-precise values with a message', () => {
    for (const bad of ['', '-5', 'abc', '1.23456']) {
      const r = ruleValuesFromInput('markdown', bad)
      expect(r.ok).toBe(false)
      const f = ruleValuesFromInput('fixed', bad)
      expect(f.ok).toBe(false)
    }
    const floor = ruleValuesFromInput('markup', '10', 'x')
    expect(floor.ok).toBe(false)
  })
})

describe('ruleUpdateFromRule', () => {
  it('replaces with the form values and keeps the window and standing, never the target', () => {
    const body = ruleUpdateFromRule(RULE, { rule_type: 'markdown', value_pct: '18' })
    expect(body).toEqual({
      rule_type: 'markdown', value_pct: '18', starts_at: '2026-03-01T00:00:00Z', expires_at: null, is_active: true, priority: 2,
    })
    for (const f of ['target_type', 'customer_id', 'tier', 'category_id', 'revision', 'id']) {
      expect(body).not.toHaveProperty(f)
    }
  })
})

describe('rules', () => {
  it('lists the envelope', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [RULE], next_cursor: null, limit: 50 }))
    const page = await categoryPricingService.listRules({ target_type: 'tier' })
    expect(lastUrl().pathname).toBe('/api/v1/pricing/category-rules')
    expect(lastUrl().search).toBe('?target_type=tier')
    expect(page.items[0].value_pct).toBe('15.0000')
  })

  it('listAllRules pages by cursor and stops at the cap', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ items: [RULE], next_cursor: 'p2', limit: 200 }))
      .mockResolvedValueOnce(jsonResponse({ items: [{ ...RULE, id: 'r-2' }], next_cursor: null, limit: 200 }))
    expect((await categoryPricingService.listAllRules({ target_type: 'account' })).map(r => r.id)).toEqual(['r-1', 'r-2'])
    expect(new URL(fetchMock.mock.calls[1][0] as string, 'http://localhost').search).toBe('?target_type=account&limit=200&cursor=p2')

    const full = Array.from({ length: 200 }, (_, i) => ({ ...RULE, id: `r-${i}` }))
    fetchMock.mockImplementation(async () => jsonResponse({ items: full, next_cursor: 'more', limit: 200 }))
    expect(await categoryPricingService.listAllRules()).toHaveLength(LIST_ALL_RULES_CAP)
  })

  it('creates with POST, no If-Match', async () => {
    await categoryPricingService.createRule({ target_type: 'tier', tier: 'GOLD', category_id: 'cat-1', rule_type: 'markup', value_pct: '10', is_active: true })
    expect(lastUrl().pathname).toBe('/api/v1/pricing/category-rules')
    expect(lastInit().method).toBe('POST')
    expect(lastHeader('If-Match')).toBeUndefined()
    expect(lastBody()).toEqual({ target_type: 'tier', tier: 'GOLD', category_id: 'cat-1', rule_type: 'markup', value_pct: '10', is_active: true })
  })

  it('updates with PUT and the quoted revision', async () => {
    await categoryPricingService.updateRule('r-1', { rule_type: 'fixed', value_ten_thousandths: 55000 }, 6)
    expect(lastUrl().pathname).toBe('/api/v1/pricing/category-rules/r-1')
    expect(lastInit().method).toBe('PUT')
    expect(lastHeader('If-Match')).toBe('"6"')
    expect(lastBody()).toEqual({ rule_type: 'fixed', value_ten_thousandths: 55000 })
  })

  it('deletes with DELETE and the quoted revision', async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await categoryPricingService.deleteRule('r-1', 7)
    expect(lastUrl().pathname).toBe('/api/v1/pricing/category-rules/r-1')
    expect(lastInit().method).toBe('DELETE')
    expect(lastHeader('If-Match')).toBe('"7"')
  })

  it('bulk upserts an array whose elements may carry the id they replace', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ count: 2 }))
    const result = await categoryPricingService.bulkUpsertRules([
      { id: 'r-1', target_type: 'tier', tier: 'GOLD', category_id: 'cat-1', rule_type: 'markdown', value_pct: '5' },
      { target_type: 'tier', tier: 'SILVER', category_id: 'cat-1', rule_type: 'markdown', value_pct: '5' },
    ])
    expect(lastUrl().pathname).toBe('/api/v1/pricing/category-rules/bulk')
    expect(lastInit().method).toBe('POST')
    expect(Array.isArray(lastBody())).toBe(true)
    expect((lastBody() as { id?: string }[])[0].id).toBe('r-1')
    expect(result.count).toBe(2)
  })
})

describe('categories, audit, matrix and resolve', () => {
  it('reads categories and the audit as envelopes and returns their items', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [{ id: 'cat-1' }], next_cursor: null, limit: 200 }))
    expect(await categoryPricingService.listCategories('flat')).toHaveLength(1)
    expect(lastUrl().pathname).toBe('/api/v1/pricing/categories')
    expect(lastUrl().search).toBe('?view=flat')
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [{ id: 'a-1' }], next_cursor: null, limit: 200 }))
    expect(await categoryPricingService.getRuleAudit('r-1')).toHaveLength(1)
    expect(lastUrl().pathname).toBe('/api/v1/pricing/category-rules/r-1/audit')
  })

  it('writes categories with the unchanged body (no If-Match)', async () => {
    await categoryPricingService.createCategory({ name: 'Framing', slug: 'framing', path: 'lumber.framing' })
    expect(lastInit().method).toBe('POST')
    await categoryPricingService.updateCategory('cat-1', { name: 'Framing', slug: 'framing', path: 'lumber.framing' })
    expect(lastUrl().pathname).toBe('/api/v1/pricing/categories/cat-1')
    expect(lastInit().method).toBe('PUT')
    expect(lastHeader('If-Match')).toBeUndefined()
  })

  it('turns the matrix nulls into empty arrays', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ categories: null, tiers: null, cells: null }))
    expect(await categoryPricingService.getMatrix()).toEqual({ categories: [], tiers: [], cells: [] })
  })

  it('resolves with the product, customer and tier', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ match_type: 'none', category_path: '', cost_price_ten_thousandths: 31000 }))
    const r = await categoryPricingService.resolvePreview('p-1', 'c-1', 'GOLD')
    expect(Object.fromEntries(lastUrl().searchParams)).toEqual({ product_id: 'p-1', customer_id: 'c-1', tier: 'GOLD' })
    expect(r.cost_price_ten_thousandths).toBe(31000)
  })
})

describe('errors', () => {
  it('throws an ApiError, with stale_revision recognised', async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(409, 'stale_revision', 'the rule changed since it was loaded'))
    const err = await categoryPricingService.updateRule('r-1', { rule_type: 'markup', value_pct: '1' }, 1).catch(e => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.isStaleRevision).toBe(true)
  })

  it('a target field on a PUT is a 400 carried with its field', async () => {
    fetchMock.mockResolvedValueOnce(errorResponse(400, 'validation_failed', 'bad body', [{ field: 'tier', message: 'is fixed at create' }]))
    const err = await categoryPricingService.updateRule('r-1', {}, 1).catch(e => e)
    expect(err.details[0]).toMatchObject({ field: 'tier' })
  })
})
