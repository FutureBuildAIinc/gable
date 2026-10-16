// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The media tab's generation button says which one it makes: "Generate
 * illustration" with Anthropic chosen (Claude draws an SVG), "Generate image"
 * with OpenRouter chosen (a photo model). It reads the chosen provider from
 * the AI settings document, never assumes one.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './ProductMediaTab'
import type { GableProductMediaTab } from './ProductMediaTab'
import { mountAsync, text } from '../../../test/dom'
import type { PIMMedia } from '../../../types/pim'

function stubSettings(provider: 'openrouter' | 'anthropic' | null) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (provider && String(input).includes('/api/v1/admin/settings/ai')) {
      return new Response(JSON.stringify({ provider, model: 'claude-sonnet-5-5', configured: true, revision: 1 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    return new Response('{}', { status: 500, headers: { 'Content-Type': 'application/json' } })
  }))
}

describe('the product media tab generation words', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
  })

  it('offers an illustration with Anthropic chosen', async () => {
    stubSettings('anthropic')
    const el = await mountAsync<GableProductMediaTab>('gable-product-media-tab', { productId: 'p1' })
    expect(text(el)).toContain('Generate illustration')
    expect(text(el)).toContain('AI Illustration')
    expect(text(el)).not.toContain('Generate image')
  })

  it('offers an image with OpenRouter chosen', async () => {
    stubSettings('openrouter')
    const el = await mountAsync<GableProductMediaTab>('gable-product-media-tab', { productId: 'p1' })
    expect(text(el)).toContain('Generate image')
    expect(text(el)).not.toContain('illustration')
  })

  it('falls back to the image words when the settings cannot be read', async () => {
    stubSettings(null)
    const el = await mountAsync<GableProductMediaTab>('gable-product-media-tab', { productId: 'p1' })
    expect(text(el)).toContain('Generate image')
  })

  it('says in words that the provider refused the key when an image row settled refused', async () => {
    stubSettings('anthropic')
    const refused = { id: 'm1', product_id: 'p1', media_type: 'hero', url: '', alt_text: '', status: 'refused', is_primary: false } as unknown as PIMMedia
    const el = await mountAsync<GableProductMediaTab>('gable-product-media-tab', { productId: 'p1', media: [refused] })
    const notice = el.querySelector('[data-testid="pim-ai-notice"]')
    expect(text(notice)).toContain('Anthropic refused the saved key')
    expect(text(notice)).toContain('Tech Admin, AI Settings')
    expect(text(el)).not.toContain('Please try again')
  })

  it('shows the refusal notice only while the newest attempt is refused', async () => {
    stubSettings('anthropic')
    const row = (id: string, status: string, created: string) =>
      ({ id, product_id: 'p1', media_type: 'hero', url: status === 'ready' ? 'data:image/svg+xml;base64,AA==' : '', alt_text: '', status, is_primary: false, created_at: created, updated_at: created }) as unknown as PIMMedia
    // An old refused row, then a newer one that was drawn once the key was fixed.
    const fixed = await mountAsync<GableProductMediaTab>('gable-product-media-tab', {
      productId: 'p1', media: [row('new', 'ready', '2026-02-01T00:00:00Z'), row('old', 'refused', '2026-01-01T00:00:00Z')],
    })
    expect(fixed.querySelector('[data-testid="pim-ai-notice"]')).toBeNull()
    document.body.innerHTML = ''
    // The newest attempt refused: the notice stands.
    const refused = await mountAsync<GableProductMediaTab>('gable-product-media-tab', {
      productId: 'p1', media: [row('old', 'ready', '2026-01-01T00:00:00Z'), row('new', 'refused', '2026-02-01T00:00:00Z')],
    })
    expect(text(refused.querySelector('[data-testid="pim-ai-notice"]'))).toContain('refused the saved key')
  })

  it('orders attempts by the moment, not the text, when one falls on a whole second', async () => {
    stubSettings('anthropic')
    const row = (id: string, status: string, created: string) =>
      ({ id, product_id: 'p1', media_type: 'hero', url: '', alt_text: '', status, is_primary: false, created_at: created, updated_at: created }) as unknown as PIMMedia
    // The refused attempt is half a second AFTER the ready one, though its text sorts before it.
    const el = await mountAsync<GableProductMediaTab>('gable-product-media-tab', {
      productId: 'p1', media: [row('a', 'ready', '2026-02-01T00:00:00Z'), row('b', 'refused', '2026-02-01T00:00:00.500Z')],
    })
    expect(text(el.querySelector('[data-testid="pim-ai-notice"]'))).toContain('refused the saved key')
  })
})
