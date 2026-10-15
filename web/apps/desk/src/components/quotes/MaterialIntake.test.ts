// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './MaterialIntake'
import type { GableMaterialIntake } from './MaterialIntake'
import type { ParseResponse, ParsedItem } from '../../types/parsing'
import { mount, update, flush, q, jsonResponse, text } from '../../test/dom'

const MATCH = {
  product_id: '33333333-3333-4333-8333-333333333333',
  sku: 'SPF-2x4x8',
  description: '2x4x8 SPF Stud',
  uom: 'PCS',
  base_price: 4.25,
}
const ALT = { ...MATCH, product_id: '44444444-4444-4444-8444-444444444444', sku: 'SPF-2x4x8-PRM', description: '2x4x8 SPF Premium' }

const ITEMS: ParsedItem[] = [
  { raw_text: '50 pcs - 2x4x8 stud', matched_product: MATCH, quantity: 50, uom: 'PCS', confidence: 0.95, is_special_order: false, alternatives: [] },
  { raw_text: '20 pcs - 2x6x12', matched_product: MATCH, quantity: 20, uom: 'PCS', confidence: 0.62, is_special_order: false, alternatives: [ALT] },
  { raw_text: 'Specialty glass panel', matched_product: null, quantity: 1, uom: 'PCS', confidence: 0, is_special_order: true, alternatives: [] },
]

function result(over: Partial<ParseResponse> = {}): ParseResponse {
  return { items: ITEMS, source_image: '', parse_time_ms: 3, item_count: ITEMS.length, synthetic: false, ...over }
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  fetchMock = vi.fn(async () => jsonResponse(result()))
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

async function paste(el: GableMaterialIntake, value: string) {
  const area = q<HTMLTextAreaElement>(el, 'textarea')
  area.value = value
  area.dispatchEvent(new Event('input', { bubbles: true }))
  await update(el, {})
  q<HTMLButtonElement>(el, '[data-testid="intake-parse-pasted"]').click()
  await flush()
  await update(el, {})
}

describe('MaterialIntake', () => {
  it('sends a pasted list to the parsing route as a text file', async () => {
    const el = await mount<GableMaterialIntake>('gable-material-intake')
    await paste(el, '50 pcs - 2x4x8 stud')
    const [url, init] = fetchMock.mock.calls[0]
    expect(new URL(url as string, 'http://localhost').pathname).toBe('/api/v1/parsing/upload')
    const file = (init.body as FormData).get('file') as File
    expect(file.type).toContain('text/plain')
    expect(await file.text()).toBe('50 pcs - 2x4x8 stud')
  })

  it('shows each line with its confidence, matched product, alternatives and the special order flag', async () => {
    const el = await mount<GableMaterialIntake>('gable-material-intake')
    await paste(el, 'x')
    const rows = el.querySelectorAll('[data-testid="intake-line"]')
    expect(rows.length).toBe(3)
    expect(text(rows[0])).toContain('95%')
    expect(text(rows[0])).toContain('SPF-2x4x8')
    expect(text(rows[1])).toContain('62%')
    expect(text(rows[1])).toContain('1 alternative')
    expect(text(rows[2])).toContain('Special order')
  })

  it('accepts one line, which joins the quote and leaves the rest', async () => {
    const el = await mount<GableMaterialIntake>('gable-material-intake')
    await paste(el, 'x')
    let accepted: ParsedItem[] = []
    el.addEventListener('accept-lines', (e) => { accepted = (e as CustomEvent).detail })
    q<HTMLButtonElement>(el.querySelectorAll('[data-testid="intake-line"]')[0] as HTMLElement, '[data-testid="intake-accept"]').click()
    await update(el, {})
    expect(accepted.map((i) => i.matched_product?.sku)).toEqual(['SPF-2x4x8'])
    expect(el.querySelectorAll('[data-testid="intake-line"]').length).toBe(2)
  })

  it('holds every Accept while the quote is saving: no line leaves the list and nothing is sent', async () => {
    const el = await mount<GableMaterialIntake>('gable-material-intake')
    await paste(el, 'x')
    let sent = 0
    el.addEventListener('accept-lines', () => { sent += 1 })
    await update(el, { disabled: true })
    const lineAccept = q<HTMLButtonElement>(el.querySelectorAll('[data-testid="intake-line"]')[0] as HTMLElement, '[data-testid="intake-accept"]')
    const confident = q<HTMLButtonElement>(el, '[data-testid="intake-accept-confident"]')
    expect(lineAccept.disabled).toBe(true)
    expect(confident.disabled).toBe(true)
    lineAccept.click()
    confident.click()
    // An accept that reaches the handlers while saving (a click landing as the
    // save starts) is refused there too.
    const handlers = el as unknown as { _accept(index: number): void, _acceptConfident(): void }
    handlers._accept(0)
    handlers._acceptConfident()
    await update(el, {})
    expect(sent).toBe(0)
    expect(el.querySelectorAll('[data-testid="intake-line"]').length).toBe(3)
  })

  it('accepts all confident lines only', async () => {
    const el = await mount<GableMaterialIntake>('gable-material-intake')
    await paste(el, 'x')
    let accepted: ParsedItem[] = []
    el.addEventListener('accept-lines', (e) => { accepted = (e as CustomEvent).detail })
    q<HTMLButtonElement>(el, '[data-testid="intake-accept-confident"]').click()
    await update(el, {})
    expect(accepted.length).toBe(1)
    expect(accepted[0].raw_text).toBe('50 pcs - 2x4x8 stud')
  })

  it('swaps to an alternative before accepting and can reject a line', async () => {
    const el = await mount<GableMaterialIntake>('gable-material-intake')
    await paste(el, 'x')
    const second = el.querySelectorAll('[data-testid="intake-line"]')[1] as HTMLElement
    q<HTMLButtonElement>(second, '[data-testid="intake-alts"]').click()
    await update(el, {})
    q<HTMLButtonElement>(second, '[data-testid="intake-swap"]').click()
    await update(el, {})
    let accepted: ParsedItem[] = []
    el.addEventListener('accept-lines', (e) => { accepted = (e as CustomEvent).detail })
    q<HTMLButtonElement>(el.querySelectorAll('[data-testid="intake-line"]')[1] as HTMLElement, '[data-testid="intake-accept"]').click()
    await update(el, {})
    expect(accepted[0].matched_product?.sku).toBe('SPF-2x4x8-PRM')
    q<HTMLButtonElement>(el.querySelectorAll('[data-testid="intake-line"]')[1] as HTMLElement, '[data-testid="intake-reject"]').click()
    await update(el, {})
    expect(el.querySelectorAll('[data-testid="intake-line"]').length).toBe(1)
  })

  it('labels a synthetic result as a sample, not the caller\'s file, and will not accept it in one click', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(result({ synthetic: true })))
    const el = await mount<GableMaterialIntake>('gable-material-intake')
    await paste(el, 'x')
    const banner = q(el, '[data-testid="intake-sample-banner"]')
    expect(text(banner)).toContain('No AI reader is set up on this server: these lines are a sample list')
    expect(el.querySelector('[data-testid="intake-accept-confident"]')).toBeNull()
  })

  it('says the provider refused the saved key, with what to do, and shows no sample list', async () => {
    const words = 'Anthropic refused the saved key. An administrator can enter it again in Tech Admin, AI Settings.'
    fetchMock.mockResolvedValueOnce(new Response(
      JSON.stringify({ error: { code: 'ai_refused', message: words }, meta: { request_id: 'r' } }),
      { status: 502, headers: { 'Content-Type': 'application/json' } },
    ))
    const el = await mount<GableMaterialIntake>('gable-material-intake')
    // A photo goes through the file input: a pasted text list is read by the
    // server's rules under a refusal and never answers 502.
    const input = q<HTMLInputElement>(el, 'input[type="file"]')
    Object.defineProperty(input, 'files', { value: [new File(['x'], 'list.png', { type: 'image/png' })], configurable: true })
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await flush()
    await update(el, {})
    const refused = q(el, '[data-testid="intake-ai-refused"]')
    expect(text(refused)).toContain(words)
    expect(el.querySelector('[data-testid="intake-sample-banner"]')).toBeNull()
    expect(text(el)).not.toContain('No AI reader is set up')
    expect(el.querySelectorAll('[data-testid="intake-line"]').length).toBe(0)
  })

  it('shows a sample read only: no per line Accept, no swap, no accept all, and says how to turn AI on', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(result({ synthetic: true })))
    const el = await mount<GableMaterialIntake>('gable-material-intake')
    await paste(el, 'x')
    let fired = false
    el.addEventListener('accept-lines', () => { fired = true })
    expect(el.querySelectorAll('[data-testid="intake-line"]').length).toBe(3)
    expect(el.querySelector('[data-testid="intake-accept"]')).toBeNull()
    expect(el.querySelector('[data-testid="intake-swap"]')).toBeNull()
    expect(el.querySelector('[data-testid="intake-accept-confident"]')).toBeNull()
    expect(text(q(el, '[data-testid="intake-sample-banner"]'))).toContain('administrator')
    expect(fired).toBe(false)
  })

  it('shows Accept confident lines as a tinted secondary, never a filled primary', async () => {
    const el = await mount<GableMaterialIntake>('gable-material-intake')
    await paste(el, 'x')
    const btn = q<HTMLButtonElement>(el, '[data-testid="intake-accept-confident"]')
    expect(btn.classList.contains('bg-gable-green')).toBe(false)
    expect(btn.classList.contains('bg-gable-green/10')).toBe(true)
  })

  it('is a collapsed section until a result is under review, then open', async () => {
    const el = await mount<GableMaterialIntake>('gable-material-intake')
    const section = () => q<HTMLDetailsElement>(el, 'details')
    expect(section().open).toBe(false)
    await paste(el, 'x')
    expect(section().open).toBe(true)
  })
})
