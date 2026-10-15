// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The portal login in light: a tenant brand colour used as text (the support
 * link) needs 4.5:1, and the Sign In label on a brand fill picks ink or paper
 * by contrast.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'

vi.mock('../../services/PortalService.ts', () => ({
  PortalService: {
    getConfig: async () => ({
      company_name: 'Ridgeview',
      logo_url: '',
      primary_color: '#8a8a8a',
      support_email: 'help@ridgeview.test',
    }),
    login: async () => ({}),
  },
}))

import './PortalLogin'
import type { LitElement } from 'lit'
import { mount, flush } from '../../test/dom'

describe('portal login brand colours in light', () => {
  afterEach(() => {
    document.documentElement.removeAttribute('data-theme')
    document.documentElement.removeAttribute('style')
  })

  it('draws the support link at 4.5:1 and the Sign In label by contrast', async () => {
    document.documentElement.dataset.theme = 'light'
    const root = document.documentElement.style
    root.setProperty('--gb-slate-steel', '248 243 236')
    root.setProperty('--gb-gable-green', '4 108 67')
    root.setProperty('--gb-black', '252 250 246')
    root.setProperty('--gb-white', '34 31 29')
    const el = await mount<LitElement>('gable-portal-login')
    await flush()
    await el.updateComplete
    const link = el.querySelector('a[href^="mailto:"]') as HTMLElement
    expect(link.getAttribute('style')).toMatch(/color: #046c43/i)
    const submit = el.querySelector('button[type="submit"]') as HTMLElement
    expect(submit.getAttribute('style') ?? '').toContain('color: rgb(var(--gb-white))')
  })
})
