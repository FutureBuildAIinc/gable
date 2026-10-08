// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The product geometry editor — the surface that fills in the length/width/
 * height/stackable columns AI_LM's load planner reads over
 * `GET /api/integration/products`.
 *
 * One property matters more than everything else here: **blank is null, not
 * zero.** The planner branches on null (fall back to its own overrides) versus
 * a number (trust it as a real measurement), so a UI that helpfully turns an
 * empty input into `0` publishes a phantom zero-volume box for every SKU nobody
 * has measured — and the planner has no way to tell that apart from a genuine
 * measurement. `stackable` has the same shape: null is "unknown", `false` is a
 * deliberate "keep the top clear".
 *
 * These tests assert on the exact JSON body sent to the PATCH, because that is
 * the only place the distinction is observable from the frontend.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './ProductGeometryTab'
import type { GableProductGeometryTab } from './ProductGeometryTab'
import { mountAsync, update, flush, q, jsonResponse } from '../../../test/dom'

const PRODUCT_ID = '33333333-3333-4333-8333-333333333333'

/** A product row as `GET /api/v1/products/{id}` returns it. */
function productRow(overrides: Record<string, unknown> = {}) {
  return {
    id: PRODUCT_ID,
    sku: 'LUM-248-PREM',
    description: '2x4x8 SPF Premium',
    uom_primary: 'PCS',
    base_price: 7.99,
    average_unit_cost: 5,
    target_margin: 0.3,
    commission_rate: 0,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    length_in: 96,
    width_in: 3.5,
    height_in: 1.5,
    stackable: true,
    geometry_source: 'parametric',
    ...overrides,
  }
}

let fetchMock: ReturnType<typeof vi.fn>

/**
 * Stubs fetch: the first call is the geometry load, every later call is the
 * PATCH. Returns a reader for the body of the most recent PATCH.
 */
function stubFetch(row: Record<string, unknown>) {
  const patches: Record<string, unknown>[] = []
  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (init?.method === 'PATCH') {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>
      patches.push(body)
      return jsonResponse(body)
    }
    if (url.includes(`/api/v1/products/${PRODUCT_ID}`)) return jsonResponse(row)
    throw new Error(`unexpected fetch: ${init?.method ?? 'GET'} ${url}`)
  })
  vi.stubGlobal('fetch', fetchMock)
  return {
    patches,
    last: () => patches[patches.length - 1],
  }
}

async function mountTab(row = productRow()) {
  const stub = stubFetch(row)
  const el = await mountAsync<GableProductGeometryTab>('gable-product-geometry-tab', {
    productId: PRODUCT_ID,
  })
  return { el, stub }
}

async function typeInto(el: GableProductGeometryTab, label: string, value: string) {
  const input = q<HTMLInputElement>(el, `input[aria-label="${label}"]`)
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await update(el, {})
}

async function save(el: GableProductGeometryTab) {
  const button = Array.from(el.querySelectorAll('button')).find((b) =>
    (b.textContent ?? '').includes('Save Dimensions'),
  )
  if (!button) throw new Error('no Save Dimensions button')
  button.click()
  await flush()
  await update(el, {})
}

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('ProductGeometryTab: blank is null, not zero', () => {
  it('sends null — never 0 — for a dimension the operator cleared', async () => {
    const { el, stub } = await mountTab()

    await typeInto(el, 'Length', '')
    await save(el)

    const body = stub.last()
    expect(body.length_in).toBeNull()
    // The distinction the whole column exists for.
    expect(body.length_in).not.toBe(0)
    // Untouched fields keep their recorded values.
    expect(body.width_in).toBe(3.5)
    expect(body.height_in).toBe(1.5)
  })

  it('preserves a deliberately entered 0 as a real measurement', async () => {
    const { el, stub } = await mountTab()

    await typeInto(el, 'Height', '0')
    await save(el)

    const body = stub.last()
    expect(body.height_in).toBe(0)
    expect(body.height_in).not.toBeNull()
  })

  it('clears every geometry column to null when the operator clears them all', async () => {
    const { el, stub } = await mountTab()

    const clear = Array.from(el.querySelectorAll('button')).find((b) =>
      (b.textContent ?? '').includes('Clear geometry'),
    )
    if (!clear) throw new Error('no Clear geometry button')
    clear.click()
    await update(el, {})
    await save(el)

    const body = stub.last()
    expect(body).toEqual({
      length_in: null,
      width_in: null,
      height_in: null,
      stackable: null,
      // No dimensions means no provenance to claim. Leaving 'parametric'
      // behind would assert geometry that does not exist.
      geometry_source: null,
    })
  })

  it('round-trips a product that has no geometry at all without inventing zeros', async () => {
    const { el, stub } = await mountTab(
      productRow({
        length_in: null,
        width_in: null,
        height_in: null,
        stackable: null,
        geometry_source: null,
      }),
    )

    // Blank inputs, not "0".
    for (const label of ['Length', 'Width', 'Height']) {
      expect(q<HTMLInputElement>(el, `input[aria-label="${label}"]`).value).toBe('')
    }

    await save(el)
    const body = stub.last()
    expect(body.length_in).toBeNull()
    expect(body.width_in).toBeNull()
    expect(body.height_in).toBeNull()
  })
})

describe('ProductGeometryTab: stackable is tri-state', () => {
  it('keeps "unknown" as null rather than defaulting it to true', async () => {
    const { el, stub } = await mountTab(productRow({ stackable: null }))

    const select = q<HTMLSelectElement>(el, 'select[aria-label="Stackable in a load"]')
    expect(select.value).toBe('')

    await save(el)
    expect(stub.last().stackable).toBeNull()
  })

  it('distinguishes an explicit "no" from "unknown"', async () => {
    const { el, stub } = await mountTab(productRow({ stackable: null }))

    const select = q<HTMLSelectElement>(el, 'select[aria-label="Stackable in a load"]')
    select.value = 'false'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await update(el, {})
    await save(el)

    expect(stub.last().stackable).toBe(false)
    expect(stub.last().stackable).not.toBeNull()
  })
})

describe('ProductGeometryTab: provenance', () => {
  it('stamps parametric provenance on operator-entered geometry', async () => {
    const { el, stub } = await mountTab(
      productRow({ length_in: null, width_in: null, height_in: null, geometry_source: null }),
    )

    await typeInto(el, 'Length', '96')
    await save(el)

    expect(stub.last().geometry_source).toBe('parametric')
  })

  it('does not overwrite a non-parametric provenance', async () => {
    const { el, stub } = await mountTab(productRow({ geometry_source: 'mesh' }))

    await typeInto(el, 'Length', '120')
    await save(el)

    expect(stub.last().geometry_source).toBe('mesh')
  })
})

describe('ProductGeometryTab: preview', () => {
  it('refuses to draw a shape for a SKU with no recorded geometry', async () => {
    const { el } = await mountTab(
      productRow({ length_in: null, width_in: null, height_in: null }),
    )
    expect(el.querySelector('[data-testid="geometry-preview-empty"]')).not.toBeNull()
    // No scale drawing at all — not a degenerate zero-size one.
    expect(el.querySelector('svg[aria-label="Scale elevation of the product"]')).toBeNull()
  })

  it('draws a scale elevation once length and height are recorded', async () => {
    const { el } = await mountTab()
    const preview = el.querySelector('[data-testid="geometry-preview"]')
    expect(preview).not.toBeNull()
    const rect = q<SVGRectElement>(preview!, 'rect')
    // 96" long x 1.5" high: the drawing is proportional, so the box is far
    // wider than it is tall. A square would mean the axes were dropped.
    expect(Number(rect.getAttribute('width'))).toBeGreaterThan(
      Number(rect.getAttribute('height')) * 10,
    )
  })
})
