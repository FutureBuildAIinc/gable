// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The Counter rules screen: the check limit, the ID rule's three choices with their validation, and the restocking fees. */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './CounterRules'
import type { GableCounterRules } from './CounterRules'
import { mount, update, flush, q, text, jsonResponse } from '../../test/dom'

const REASONS = [
  { code: 'defective', label: 'Defective', restocking_fee_bps: 0, needs_note: false },
  { code: 'damaged', label: 'Damaged', restocking_fee_bps: 0, needs_note: false },
  { code: 'wrong_item', label: 'Wrong item given', restocking_fee_bps: 0, needs_note: false },
  { code: 'ordered_wrong', label: 'Ordered the wrong item', restocking_fee_bps: 0, needs_note: false },
  { code: 'changed_mind', label: 'Changed their mind', restocking_fee_bps: 0, needs_note: false },
  { code: 'overbought', label: 'Bought too much', restocking_fee_bps: 0, needs_note: false },
  { code: 'other', label: 'Other (needs a note)', restocking_fee_bps: 0, needs_note: true },
]

const SUGGESTED: Record<string, number> = {
  defective: 0, damaged: 0, wrong_item: 0,
  ordered_wrong: 1500, changed_mind: 1500, overbought: 1500, other: 0,
}

const BASE = {
  revision: 4,
  rules: {
    check_limit_cents: null,
    check_id_required: { mode: 'never', over_cents: null },
    restocking_fees: Object.fromEntries(REASONS.map((r) => [r.code, 0])),
    restocking_fee_defaults: { ...SUGGESTED },
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
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).includes('return-reasons')) {
      const fees = (current as { rules?: { restocking_fees?: Record<string, number> } }).rules?.restocking_fees ?? {}
      return jsonResponse({ items: REASONS.map((r) => ({ ...r, restocking_fee_bps: fees[r.code] ?? 0 })) })
    }
    if ((init?.method ?? 'GET') === 'PUT') {
      puts.push(JSON.parse(String(init!.body)))
      if (refuse) return refuse
      const body = puts[puts.length - 1] as { rules: Record<string, unknown> }
      current = { revision: (current.revision as number) + 1, rules: { ...(current as { rules: object }).rules, ...body.rules } }
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

  it('shows the tint charge rule and the no return policy the dealer saved', async () => {
    current = {
      revision: 9,
      rules: {
        ...BASE.rules,
        tint_charge: { flat_cents: 250, tiers: [{ base_tier: 'deep', min_colorant_oz: '2.0000', charge_cents: 4000 }] },
        tinted_no_return: true,
      },
    }
    const el = await open()
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-tint-flat"]').value).toBe('2.50')
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-tint-floor-0"]').value).toBe('2.0000')
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-tint-charge-0"]').value).toBe('40.00')
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-tint-no-return"] input').checked).toBe(true)
  })

  it('writes the tint tiers and the policy with the rest of the rules', async () => {
    const el = await open()
    await type(el, '[data-testid="counter-rules-tint-flat"]', '2.50')
    await click(el, '[data-testid="counter-rules-tint-add-tier"]')
    await type(el, '[data-testid="counter-rules-tint-floor-0"]', '2')
    await type(el, '[data-testid="counter-rules-tint-charge-0"]', '40')
    const policy = q<HTMLInputElement>(el, '[data-testid="counter-rules-tint-no-return"] input')
    policy.checked = true
    policy.dispatchEvent(new Event('change'))
    await update(el, {})
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts).toHaveLength(1)
    expect(puts[0]).toMatchObject({
      rules: {
        tint_charge: { flat_cents: 250, tiers: [{ base_tier: '', min_colorant_oz: '2', charge_cents: 4000 }] },
        tinted_no_return: true,
      },
    })
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

  it('shows every reason with its fee and the suggested figures', async () => {
    const el = await open()
    expect(q(el, '[data-testid="counter-rules-fees"]')).not.toBeNull()
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-fee-changed_mind"]').value).toBe('0')
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-fee-defective"]').value).toBe('0')
    expect(text(q(el, '[data-testid="counter-rules-fee-suggested-changed_mind"]'))).toBe('Suggested 15%')
    expect(el.querySelector('[data-testid="counter-rules-fee-suggested-defective"]')).toBeNull()
  })

  it('saves the fees in basis points beside the check rules', async () => {
    const el = await open()
    await type(el, '[data-testid="counter-rules-fee-changed_mind"]', '15')
    await type(el, '[data-testid="counter-rules-fee-ordered_wrong"]', '12.5')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts[0]).toMatchObject({
      rules: {
        restocking_fees: {
          changed_mind: 1500,
          ordered_wrong: 1250,
          defective: 0,
          other: 0,
        },
      },
    })
  })

  it('fills every fee from the suggested figures', async () => {
    const el = await open()
    await click(el, '[data-testid="counter-rules-fees-suggested"]')
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-fee-changed_mind"]').value).toBe('15')
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-fee-defective"]').value).toBe('0')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts[0]).toMatchObject({ rules: { restocking_fees: { changed_mind: 1500, defective: 0 } } })
  })

  it('refuses a fee that is not a percent from 0 to 100', async () => {
    const el = await open()
    await type(el, '[data-testid="counter-rules-fee-overbought"]', 'lots')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts).toHaveLength(0)
    expect(text(q(el, '[data-testid="counter-rules-error"]'))).toContain('must be a percent from 0 to 100')

    await type(el, '[data-testid="counter-rules-fee-overbought"]', '150')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts).toHaveLength(0)
    expect(text(q(el, '[data-testid="counter-rules-error"]'))).toContain('must be a percent from 0 to 100')
  })

  it('keeps a saved fee on the screen after the save', async () => {
    const el = await open()
    await type(el, '[data-testid="counter-rules-fee-changed_mind"]', '15')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-fee-changed_mind"]').value).toBe('15')
  })

  it('does not offer the card fee while no branch is known', async () => {
    const el = await open()
    expect(el.querySelector('[data-testid="counter-rules-card-fee"]')).toBeNull()
    await click(el, '[data-testid="counter-rules-save"]')
    expect((puts[0] as { rules: Record<string, unknown> }).rules.card_surcharge).toBeUndefined()
  })

  it('shows the card fee off by default and writes it on at 3 percent with the disclosure', async () => {
    current = { revision: 4, rules: { ...BASE.rules, card_surcharge: { branch_id: 'b-1', enabled: false, rate_bps: 0 } } }
    const el = await open()
    const sw = q<HTMLInputElement>(el, '[data-testid="counter-rules-card-fee-switch"] input')
    expect(sw.checked).toBe(false)
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-card-fee-rate"]').disabled).toBe(true)
    expect(text(q(el, '[data-testid="counter-rules-card-fee-disclosure"]'))).toContain('never more than 3 percent')
    sw.checked = true
    sw.dispatchEvent(new Event('change'))
    await update(el, {})
    await type(el, '[data-testid="counter-rules-card-fee-rate"]', '3')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts).toHaveLength(1)
    expect(puts[0]).toMatchObject({ rules: { card_surcharge: { enabled: true, rate_bps: 300 } } })
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-card-fee-switch"] input').checked).toBe(true)
    expect(q<HTMLInputElement>(el, '[data-testid="counter-rules-card-fee-rate"]').value).toBe('3')
  })

  it('refuses a card fee above 3 percent before it is sent', async () => {
    current = { revision: 4, rules: { ...BASE.rules, card_surcharge: { branch_id: 'b-1', enabled: true, rate_bps: 300 } } }
    const el = await open()
    await type(el, '[data-testid="counter-rules-card-fee-rate"]', '3.5')
    await click(el, '[data-testid="counter-rules-save"]')
    expect(puts).toHaveLength(0)
    expect(text(q(el, '[data-testid="counter-rules-error"]'))).toContain('0 to 3')
  })
})
