// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The Counter rules screen: the check limit, and the ID rule's three choices with their validation. */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './CounterRules'
import type { GableCounterRules } from './CounterRules'
import { mount, update, flush, q, text, jsonResponse } from '../../test/dom'

const BASE = {
  revision: 4,
  rules: {
    check_limit_cents: null,
    check_id_required: { mode: 'never', over_cents: null },
  },
}

let current: Record<string, unknown>
let puts: unknown[]
let refuse: Response | null

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  current = structuredClone(BASE)
  puts = []
  refuse = null
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    if ((init?.method ?? 'GET') === 'PUT') {
      puts.push(JSON.parse(String(init!.body)))
      if (refuse) return refuse
      const body = puts[puts.length - 1] as { rules: Record<string, unknown> }
      current = { revision: (current.revision as number) + 1, rules: body.rules }
      return jsonResponse(current)
    }
    return jsonResponse(current)
  }))
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

async function open(): Promise<GableCounterRules> {
  const el = await mount<GableCounterRules>('gable-counter-rules')
  await flush()
  await update(el, {})
  return el
}

async function click(el: GableCounterRules, selector: string) {
  q<HTMLButtonElement>(el, selector).click()
  await flush()
  await update(el, {})
  await flush()
  await update(el, {})
}

async function type(el: GableCounterRules, selector: string, value: string) {
  const input = q<HTMLInputElement>(el, selector)
  input.value = value
  input.dispatchEvent(new Event('input'))
  await update(el, {})
}

async function pick(el: GableCounterRules, mode: string) {
  const radio = q<HTMLInputElement>(el, `[data-testid="counter-rules-id-choice-${mode}"] input`)
  radio.checked = true
  radio.dispatchEvent(new Event('change'))
  await update(el, {})
}

describe('Counter rules screen', () => {
  it('shows the saved rules: no limit and never', async () => {
    const el = await open()
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-limit"]').value).toBe('')
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-id-choice-never"] input').checked).toBe(true)
    expect(el.querySelector('[data-testid="counter-rules-id-over"]')).toBeNull()
  })

  it('shows a saved limit and an over amount rule with its figure', async () => {
    current = { revision: 7, rules: { check_limit_cents: 10000, check_id_required: { mode: 'over_amount', over_cents: 5000 } } }
    const el = await open()
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-limit"]').value).toBe('100.00')
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-id-choice-over_amount"] input').checked).toBe(true)
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-id-over"]').value).toBe('50.00')
  })

  it('saves the limit and the over amount rule in cents on the revision', async () => {
    const el = await open()
    await type(el, '[data-testid="counter-rules-limit"]', '100.00')
    await pick(el, 'over_amount')
    await type(el, '[data-testid="counter-rules-id-over"]', '50.00')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts).toHaveLength(1)
    expect(puts[0]).toMatchObject({
      revision: 4,
      rules: {
        check_limit_cents: 10000,
        check_id_required: { mode: 'over_amount', over_cents: 5000 },
      },
    })
    expect(text(q(el, '[data-testid="counter-rules-saved"]'))).toBe('Counter rules saved')
  })

  it('saves every check, and never, without a figure', async () => {
    const el = await open()
    await pick(el, 'every_check')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts[0]).toMatchObject({ rules: { check_id_required: { mode: 'every_check', over_cents: null } } })

    await pick(el, 'never')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts[1]).toMatchObject({ rules: { check_id_required: { mode: 'never', over_cents: null } } })
  })

  it('refuses an over amount rule with no figure', async () => {
    const el = await open()
    await pick(el, 'over_amount')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts).toHaveLength(0)
    expect(text(q(el, '[data-testid="counter-rules-error"]'))).toContain('Name the check amount the ID rule starts at')
  })

  it('refuses a check limit that is not a dollar amount', async () => {
    const el = await open()
    await type(el, '[data-testid="counter-rules-limit"]', 'lots')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts).toHaveLength(0)
    expect(text(q(el, '[data-testid="counter-rules-error"]'))).toContain('The check limit must be a dollar amount')
  })

  it('shows the server words on a refusal', async () => {
    refuse = jsonResponse({ error: { code: 'validation_failed', message: 'one or more fields failed validation',
      details: [{ field: 'rules.check_limit_cents', message: 'must not be negative; send null for no limit' }] } }, 400)
    const el = await open()
    await click(el, '[data-testid="counter-rules-save"]')
    expect(text(q(el, '[data-testid="counter-rules-error"]'))).toContain('one or more fields failed validation')
  })
})
