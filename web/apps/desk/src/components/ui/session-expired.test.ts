// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The session expired panel: it says plainly that the person was signed out
 * and sends them to the front door, the one place staff sign in.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './session-expired.ts'
import type { GableSessionExpired } from './session-expired.ts'

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('gable-session-expired', () => {
  it('offers Sign in again and takes the person to the front door', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign })
    const el = document.createElement('gable-session-expired') as GableSessionExpired
    document.body.appendChild(el)
    await el.updateComplete

    expect(el.querySelector('#session-expired-title')?.textContent).toContain('session has expired')
    expect(el.textContent).not.toContain('identity provider')
    const btn = el.querySelector('button')!
    expect(btn.textContent?.trim()).toBe('Sign in again')
    btn.click()
    expect(assign).toHaveBeenCalledWith('/')
  })
})
