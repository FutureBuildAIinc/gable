// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Create Route asks for a vehicle and a driver with the app's own selects.
 * Submitting with nothing chosen must say what is missing in words, not fail
 * silently (the browser's own refusal went away with the native selects).
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './CreateRouteModal'
import type { GableCreateRouteModal } from './CreateRouteModal'
import { mountAsync, update, q, text } from '../../test/dom.ts'
import { jsonResponse, chooseOption } from '../../test/dom.ts'

let posts: string[]

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  posts = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (init?.method === 'POST') {
      posts.push(url)
      return jsonResponse({ id: 'r-1' }, 201)
    }
    if (url.includes('/api/v1/delivery/vehicles') || url.includes('/api/v1/delivery/drivers')) {
      return jsonResponse({ items: [], next_cursor: null })
    }
    throw new Error(`unexpected fetch: ${init?.method ?? 'GET'} ${url}`)
  }))
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function open(): Promise<GableCreateRouteModal> {
  return mountAsync<GableCreateRouteModal>('gable-create-route-modal', { isOpen: true })
}

describe('gable-create-route-modal required picks', () => {
  it('the vehicle and driver selects read as required', async () => {
    const el = await open()
    const selects = Array.from(el.querySelectorAll('gable-select'))
    expect(selects.length).toBe(2)
    for (const s of selects) {
      expect(s.querySelector('button')!.getAttribute('aria-required')).toBe('true')
    }
  })

  it('a submit with nothing chosen says what is missing and sends nothing', async () => {
    const el = await open()
    q<HTMLFormElement>(el, 'form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await update(el, {})
    const message = q<HTMLElement>(el, '[data-testid="route-form-error"]')
    expect(message.getAttribute('role')).toBe('alert')
    expect(text(message)).toContain('vehicle')
    expect(text(message)).toContain('driver')
    expect(posts).toEqual([])
  })

  it('the Scheduled Date field reads as required', async () => {
    const el = await open()
    const field = q<HTMLElement>(el, 'gable-date-field')
    expect(field.querySelector('input')!.getAttribute('aria-required')).toBe('true')
  })

  it('a cleared date says the date is missing in words and sends nothing', async () => {
    const el = await open()
    const selects = Array.from(el.querySelectorAll('gable-select'))
    await chooseOption(selects[0] as HTMLElement & { value: string }, 'v-1')
    await chooseOption(selects[1] as HTMLElement & { value: string }, 'd-1')
    await update(el, {})
    const field = q<HTMLElement>(el, 'gable-date-field')
    const box = field.querySelector('input')!
    box.value = ''
    box.dispatchEvent(new Event('input'))
    box.dispatchEvent(new FocusEvent('blur'))
    await update(el, {})
    q<HTMLFormElement>(el, 'form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await update(el, {})
    const message = q<HTMLElement>(el, '[data-testid="route-form-error"]')
    expect(message.getAttribute('role')).toBe('alert')
    expect(text(message).toLowerCase()).toContain('scheduled date')
    expect(text(message).toLowerCase()).not.toContain('vehicle')
    expect(text(message).toLowerCase()).not.toContain('driver')
    expect(box.getAttribute('aria-invalid')).toBe('true')
    expect(posts).toEqual([])
  })
})
