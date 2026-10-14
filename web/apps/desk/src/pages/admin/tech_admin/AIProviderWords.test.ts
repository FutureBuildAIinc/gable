// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The AI tab of Tech Admin reads the provider's state: with Anthropic chosen
 * it says "AI on: Anthropic, Sonnet 5.5" with a key saved and asks for the
 * Anthropic key without one, and never tells the admin to enter an
 * OpenRouter key.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './TechAdminPage'
import type { TechAdminPage } from './TechAdminPage'
import { mountAsync, update, flush, text, jsonResponse } from '../../../test/dom'

function serve(settings: Record<string, unknown>) {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      if (String(input).includes('/api/v1/admin/settings/ai')) return Promise.resolve(jsonResponse(settings))
      return Promise.resolve(jsonResponse([]))
    }),
  )
}

async function openAITab(): Promise<TechAdminPage> {
  const el = await mountAsync<TechAdminPage>('gable-tech-admin')
  const tab = Array.from(el.querySelectorAll('button')).find((b) => text(b).includes('AI'))
  if (!tab) throw new Error('no AI tab button')
  tab.click()
  await flush()
  await update(el, {})
  return el
}

const BASE = {
  revision: 3, source: 'none', key_hint: null, base_url: null, vault_present: true,
  anthropic_configured: false, anthropic_key_hint: null, configured: false,
}

beforeEach(() => {
  localStorage.clear()
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Tech Admin AI words follow the chosen provider', () => {
  it('names Anthropic and the model when its key is saved', async () => {
    serve({ ...BASE, provider: 'anthropic', model: 'claude-sonnet-5-5', configured: true, anthropic_configured: true, anthropic_key_hint: 'sk-a...wxyz', source: 'admin', test_status: 'passed' })
    const body = text(await openAITab())
    expect(body).toContain('AI on: Anthropic, Sonnet 5.5')
    expect(body).toContain('AI Features Active')
    expect(body).not.toContain('Enter your OpenRouter API key')
  })

  it('does not say AI is on for a key the provider refused', async () => {
    serve({ ...BASE, provider: 'anthropic', model: 'claude-sonnet-5-5', configured: true, anthropic_configured: true, anthropic_key_hint: '****dmin', source: 'admin', test_status: 'refused', test_message: 'Anthropic refused the saved key. An administrator can enter it again in Tech Admin, AI Settings.' })
    const body = text(await openAITab())
    expect(body).toContain('AI off: Anthropic refused the saved key')
    expect(body).not.toContain('AI on:')
    expect(body).not.toContain('AI Features Active')
  })

  it('dims the features grid for a key the provider refused and lights it for a passed one', async () => {
    const card = () => {
      const span = Array.from(document.querySelectorAll('span')).find((s) => text(s) === 'Product Illustrations')
      return span?.closest('div.border') as HTMLElement
    }
    serve({ ...BASE, provider: 'anthropic', model: 'claude-sonnet-5-5', configured: true, anthropic_configured: true, anthropic_key_hint: '****dmin', source: 'admin', test_status: 'refused' })
    await openAITab()
    expect(card().className).toContain('opacity-60')
    document.body.innerHTML = ''
    serve({ ...BASE, provider: 'anthropic', model: 'claude-sonnet-5-5', configured: true, anthropic_configured: true, anthropic_key_hint: '****dmin', source: 'admin', test_status: 'passed' })
    await openAITab()
    expect(card().className).not.toContain('opacity-60')
  })

  it('says a saved key was not tested yet', async () => {
    serve({ ...BASE, provider: 'anthropic', model: 'claude-sonnet-5-5', configured: true, anthropic_configured: true, anthropic_key_hint: '****real', source: 'admin', test_status: 'untested' })
    const body = text(await openAITab())
    expect(body).toContain('Key saved, not tested yet')
    expect(body).not.toContain('AI on:')
  })

  it('asks for the Anthropic key, not the OpenRouter one, when none is saved', async () => {
    serve({ ...BASE, provider: 'anthropic', model: 'claude-sonnet-5-5' })
    const body = text(await openAITab())
    expect(body).toContain('Enter your Anthropic API key')
    expect(body).not.toContain('Enter your OpenRouter API key')
    expect(body).toContain('Product Illustrations')
  })

  it('asks for the OpenRouter key with OpenRouter chosen', async () => {
    serve({ ...BASE, provider: 'openrouter', model: 'claude-sonnet-5-5' })
    const body = text(await openAITab())
    expect(body).toContain('Enter your OpenRouter API key')
    expect(body).toContain('Product Image Generation')
  })
})
