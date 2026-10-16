// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** Tech Admin opens on the tab the address names (the Market check page links to its own card). */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './TechAdminPage'
import type { TechAdminPage } from './TechAdminPage'
import { mountAsync, text, jsonResponse } from '../../../test/dom'

afterEach(() => {
  document.body.innerHTML = ''
  history.replaceState(null, '', '/')
  vi.unstubAllGlobals()
})

function serve() {
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes('/market-check/settings')) {
      return Promise.resolve(jsonResponse({
        connect_url: '', webhook_id_saved: false, flow_name: 'FB Connect pricing flow', store_label: '', store_id: '',
        store_zip: '', band_pct: '5', margin_floor_pct: '15', configured: false, notice: '',
      }))
    }
    return Promise.resolve(jsonResponse([]))
  }))
}

describe('Tech Admin tab from the address', () => {
  it('opens the Market check card for ?tab=market-check', async () => {
    serve()
    history.replaceState(null, '', '/admin?tab=market-check')
    const el = await mountAsync<TechAdminPage>('gable-tech-admin')
    expect(el.querySelector('gable-market-check-settings')).not.toBeNull()
    expect(text(el)).toContain('Flow webhook id')
  })

  it('opens API Keys with no tab named, and ignores an unknown one', async () => {
    serve()
    history.replaceState(null, '', '/admin?tab=nonsense')
    const el = await mountAsync<TechAdminPage>('gable-tech-admin')
    expect(el.querySelector('gable-market-check-settings')).toBeNull()
  })
})
