// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The tint catalogue screen: a base links to a product and unlinks, and a base, a colorant, a colour and a formula are added. */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './TintCatalogue'
import type { GableTintCatalogue } from './TintCatalogue'
import { mount, update, flush, q, text, jsonResponse, chooseOption } from '../../test/dom'

const SYSTEM = { id: 's1', code: 'DEMO', name: 'Demo tint', manufacturer: '', unit_denominator: 384, unit_label: '' }
const PRODUCT = { product_id: 'p1', sku: 'PAINT-GAL', description: 'Base paint gallon', unit_price_cents: 4000, uom: 'EA', in_stock: '4' }

let base: Record<string, unknown>
let calls: { method: string; url: string; body: unknown }[]
let refuse: Response | null
let formulas: Record<string, unknown>[]

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  base = { id: 'b1', tint_system_id: 's1', code: 'DEEP', name: 'Deep base', manufacturer_base_code: '', product_id: null, fill_volume_oz: 122, is_deepest: true }
  calls = []
  refuse = null
  formulas = [
    { id: 'f1', color_id: 'c1', base_id: 'b1', container_size_code: 'GAL', container_volume_oz: 128, components: [{}, {}] },
    { id: 'f2', color_id: 'c1', base_id: 'other-system-base', container_size_code: 'QT', container_volume_oz: 32, components: [{}] },
  ]
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ method, url, body })
    if (method === 'PATCH') {
      if (refuse) return refuse
      base = { ...base, product_id: body.product_id }
      return jsonResponse(base)
    }
    if (method === 'GET' && url.includes('/tint/formulas')) return jsonResponse({ items: formulas })
    if (method === 'POST') return jsonResponse({ id: 'new', ...body }, 201)
    if (url.includes('/pos/products/search')) return jsonResponse([PRODUCT])
    if (url.includes('/tint/systems')) return jsonResponse({ items: [SYSTEM] })
    if (url.includes('/tint/bases')) return jsonResponse({ items: [base] })
    if (url.includes('/tint/colorants')) return jsonResponse({ items: [{ id: 'k1', tint_system_id: 's1', code: 'B1', name: 'Lamp Black' }] })
    if (url.includes('/tint/colors')) return jsonResponse({ items: [{ id: 'c1', tint_system_id: 's1', manufacturer_code: 'HC-188', name: 'Essex Green', is_custom: false }] })
    return jsonResponse({})
  }))
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

async function open(): Promise<GableTintCatalogue> {
  const el = await mount<GableTintCatalogue>('gable-tint-catalogue')
  await flush()
  await update(el, {})
  return el
}

async function click(el: GableTintCatalogue, selector: string) {
  q<HTMLButtonElement>(el, selector).click()
  await flush()
  await update(el, {})
  await flush()
  await update(el, {})
}

async function type(el: GableTintCatalogue, selector: string, value: string) {
  const input = q<HTMLInputElement>(el, selector)
  input.value = value
  input.dispatchEvent(new Event('input'))
  await flush()
  await update(el, {})
}

describe('Tint catalogue screen', () => {
  it('shows the base, its fill and that it has no product yet', async () => {
    const el = await open()
    expect(text(q(el, '[data-testid="tint-base-DEEP"]'))).toContain('Deep base')
    expect(text(q(el, '[data-testid="tint-base-link-DEEP"]'))).toContain('Not linked to a product')
  })

  it('links a base to the product found and sends only the product id', async () => {
    const el = await open()
    await type(el, '[data-testid="tint-base-find-DEEP"]', 'paint')
    await click(el, '[data-testid="tint-base-pick-DEEP-PAINT-GAL"]')
    const patch = calls.find((c) => c.method === 'PATCH')
    expect(patch?.url).toContain('/api/v1/tint/bases/b1')
    expect(patch?.body).toEqual({ product_id: 'p1' })
    expect(text(q(el, '[data-testid="tint-base-link-DEEP"]'))).toContain('PAINT-GAL')
    expect(text(q(el, '[data-testid="tint-success"]'))).toContain('Base linked')
  })

  it('unlinks a linked base by sending null', async () => {
    base = { ...base, product_id: 'p1' }
    const el = await open()
    await click(el, '[data-testid="tint-base-unlink-DEEP"]')
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ product_id: null })
    expect(text(q(el, '[data-testid="tint-base-link-DEEP"]'))).toContain('Not linked')
  })

  it('says why a refused link failed and keeps the base unlinked', async () => {
    refuse = jsonResponse({ error: { message: 'no such product' } }, 422)
    const el = await open()
    await type(el, '[data-testid="tint-base-find-DEEP"]', 'paint')
    await click(el, '[data-testid="tint-base-pick-DEEP-PAINT-GAL"]')
    expect(el.querySelector('[data-testid="tint-error"]')).not.toBeNull()
    expect(text(q(el, '[data-testid="tint-base-link-DEEP"]'))).toContain('Not linked')
  })

  it('shows each outcome in the card that asked for it, and only one at a time', async () => {
    refuse = jsonResponse({ error: { message: 'no such product' } }, 422)
    const el = await open()
    await type(el, '[data-testid="tint-base-find-DEEP"]', 'paint')
    await click(el, '[data-testid="tint-base-pick-DEEP-PAINT-GAL"]')
    const error = q(el, '[data-testid="tint-error"]')
    expect(error.closest('[data-testid="tint-base-DEEP"]')).not.toBeNull()
    await type(el, '[data-testid="tint-new-colorant-code"]', 'R1')
    await type(el, '[data-testid="tint-new-colorant-name"]', 'Red')
    await click(el, '[data-testid="tint-add-colorant"]')
    expect(el.querySelector('[data-testid="tint-error"]')).toBeNull()
    expect(q(el, '[data-testid="tint-success"]').closest('[data-testid="tint-colorants"]')).not.toBeNull()
  })

  it('lists the colorants, colours and this system\'s formulas as rows', async () => {
    const el = await open()
    expect(el.querySelectorAll('[data-testid="tint-colorant-list"] li')).toHaveLength(1)
    expect(text(q(el, '[data-testid="tint-colorant-list"] li'))).toContain('Lamp Black')
    expect(text(q(el, '[data-testid="tint-color-list"] li'))).toContain('HC-188')
    const rows = el.querySelectorAll('[data-testid="tint-formula-list"] li')
    expect(rows).toHaveLength(1)
    expect(text(rows[0] as HTMLElement)).toContain('Essex Green on Deep base')
    expect(text(rows[0] as HTMLElement)).toContain('GAL, 128 oz, 2 colorants')
  })

  it('says so when there are no formulas yet', async () => {
    formulas = []
    const el = await open()
    expect(text(q(el, '[data-testid="tint-formula-list"]'))).toBe('No formulas yet.')
  })

  it('gives every button, field and the checkbox a focus ring, and draws with tokens', async () => {
    base = { ...base, product_id: 'p1' }
    const el = await open()
    await type(el, '[data-testid="tint-base-find-DEEP"]', 'paint')
    const controls = el.querySelectorAll('button, input[type="text"], input[type="checkbox"]')
    expect(controls.length).toBeGreaterThan(8)
    for (const c of controls) if (!c.closest('gable-select')) expect(c.className, c.getAttribute('data-testid') ?? c.tagName).toContain('focus-visible:ring-2')
    expect(q(el, '[data-testid="tint-base-unlink-DEEP"]').className).toContain('focus-visible:ring-2')
    expect(q(el, '[data-testid="tint-base-pick-DEEP-PAINT-GAL"]').className).toContain('focus-visible:ring-2')
    expect(el.innerHTML.replace(/<gable-select.*?<\/gable-select>/gs, '')).not.toMatch(/(?:text|bg|border|divide)-(?:white|slate-\d|red-\d)/)
  })

  it('marks required fields and says what a disabled button is waiting for', async () => {
    const el = await open()
    expect(text(q(el, 'label:has([data-testid="tint-new-base-code"])'))).toContain('*')
    expect(q<HTMLInputElement>(el, '[data-testid="tint-new-base-code"]').required).toBe(true)
    expect(q<HTMLButtonElement>(el, '[data-testid="tint-add-base"]').disabled).toBe(true)
    expect(text(q(el, '[data-testid="tint-add-base-needs"]'))).toBe('Needs a code and a name.')
    expect(q(el, '[data-testid="tint-add-base"]').getAttribute('aria-describedby')).toBe('tint-add-base-needs')
    await type(el, '[data-testid="tint-new-base-code"]', 'MID')
    expect(text(q(el, '[data-testid="tint-add-base-needs"]'))).toBe('Needs a name.')
    await type(el, '[data-testid="tint-new-base-name"]', 'Mid base')
    expect(el.querySelector('[data-testid="tint-add-base-needs"]')).toBeNull()
    expect(text(q(el, '[data-testid="tint-add-formula-needs"]'))).toBe('Needs a colour, a base and at least one colorant.')
  })

  it('adds a base, a colorant and a colour to the chosen system', async () => {
    const el = await open()
    await type(el, '[data-testid="tint-new-base-code"]', 'MID')
    await type(el, '[data-testid="tint-new-base-name"]', 'Mid base')
    await click(el, '[data-testid="tint-add-base"]')
    await type(el, '[data-testid="tint-new-colorant-code"]', 'R1')
    await type(el, '[data-testid="tint-new-colorant-name"]', 'Red')
    await click(el, '[data-testid="tint-add-colorant"]')
    await type(el, '[data-testid="tint-new-color-name"]', 'Barn')
    await click(el, '[data-testid="tint-add-color"]')
    const posts = calls.filter((c) => c.method === 'POST')
    expect(posts.map((c) => c.url.replace(/^.*\/api\/v1\/tint\//, ''))).toEqual(['bases', 'colorants', 'colors'])
    expect(posts[0].body).toMatchObject({ tint_system_id: 's1', code: 'MID', fill_volume_oz: 122 })
  })

  it('adds a formula with the system denominator on every component', async () => {
    const el = await open()
    await chooseOption(q(el, '[data-testid="tint-formula-color"]'), 'c1')
    await chooseOption(q(el, '[data-testid="tint-formula-base"]'), 'b1')
    await update(el, {})
    await click(el, '[data-testid="tint-formula-add-colorant"]')
    await type(el, '[data-testid="tint-formula-whole-0"]', '1')
    await type(el, '[data-testid="tint-formula-coarse-0"]', '12')
    await click(el, '[data-testid="tint-add-formula"]')
    const post = calls.find((c) => c.method === 'POST' && c.url.endsWith('/formulas'))
    expect(post?.body).toMatchObject({
      color_id: 'c1', base_id: 'b1', container_size_code: 'GAL', container_volume_oz: 128, source: 'manual',
      components: [{ colorant_id: 'k1', whole_oz: 1, increments_coarse: 12, increments_fine: 0, unit_denominator: 384 }],
    })
  })
})
