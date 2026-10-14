// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * When the AI provider refuses the saved key, PIM's Generate shows the
 * refusal in words on the page, where it stays until the next try; it is not
 * a toast that leaves nothing behind.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './ProductContentTab'
import './ProductSEOTab'
import './ProductCollateralTab'
import { mount, flush, q, text, update } from '../../../test/dom'

const WORDS = 'Anthropic refused the saved key. An administrator can enter it again in Tech Admin, AI Settings.'

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  vi.stubGlobal('fetch', vi.fn(async () => new Response(
    JSON.stringify({ error: { code: 'ai_refused', message: WORDS }, meta: { request_id: 'r' } }),
    { status: 502, headers: { 'Content-Type': 'application/json' } },
  )))
})
afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

function press(el: HTMLElement, label: string) {
  const button = Array.from(el.querySelectorAll('button')).find((b) => text(b).includes(label))
  if (!button) throw new Error(`no button ${label}`)
  button.click()
}

describe('PIM generate shows a provider refusal on the page', () => {
  it('on the description tab', async () => {
    const el = await mount<HTMLElement & { productId: string }>('gable-product-content-tab', { productId: 'p1' })
    press(el, 'Generate')
    await flush()
    await update(el as never, {})
    expect(text(q(el, '[data-testid="pim-ai-notice"]'))).toContain(WORDS)
  })

  it('on the SEO tab', async () => {
    const el = await mount<HTMLElement & { productId: string }>('gable-product-seo-tab', { productId: 'p1' })
    press(el, 'Generate')
    await flush()
    await update(el as never, {})
    expect(text(q(el, '[data-testid="pim-ai-notice"]'))).toContain(WORDS)
  })

  it('on the collateral tab', async () => {
    const el = await mount<HTMLElement & { productId: string }>('gable-product-collateral-tab', { productId: 'p1' })
    press(el, 'Generate')
    await flush()
    await update(el as never, {})
    expect(text(q(el, '[data-testid="pim-ai-notice"]'))).toContain(WORDS)
  })
})
