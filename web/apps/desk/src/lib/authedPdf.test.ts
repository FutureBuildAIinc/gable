// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * A document print is fetched with the signed in person's token and shown from
 * a blob in a tab opened during the click: a bare new tab on the API's address
 * carries no Authorization header and lands on a 401.
 */
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { openAuthedPdf, fetchAuthedBlobUrl } from './authedPdf'

describe('openAuthedPdf', () => {
  const tab = { location: { href: '' }, close: vi.fn() }
  beforeEach(() => {
    localStorage.setItem('token', 'test-token')
    tab.location.href = ''
    tab.close.mockReset()
    vi.stubGlobal('open', vi.fn(() => tab))
    URL.createObjectURL = vi.fn(() => 'blob:authed-pdf')
    URL.revokeObjectURL = vi.fn()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.removeItem('token')
  })

  it('opens the tab before the fetch, sends the token and points the tab at the blob', async () => {
    const order: string[] = []
    vi.stubGlobal('open', vi.fn(() => { order.push('open'); return tab }))
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      order.push('fetch')
      expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer test-token')
      return new Response('%PDF-1.4', { status: 200, headers: { 'Content-Type': 'application/pdf' } })
    }))
    const shown = await openAuthedPdf('/api/v1/documents/print/invoice/abc', 'Failed to read the invoice')
    expect(order).toEqual(['open', 'fetch'])
    expect(window.open).toHaveBeenCalledWith('', '_blank')
    expect(String((fetch as unknown as { mock: { calls: string[][] } }).mock.calls[0][0])).toContain('/api/v1/documents/print/invoice/abc')
    expect(tab.location.href).toBe('blob:authed-pdf')
    expect(shown).toEqual({ url: 'blob:authed-pdf', blocked: false })
  })

  it('answers the url for a download when the tab is blocked', async () => {
    vi.stubGlobal('open', vi.fn(() => null))
    vi.stubGlobal('fetch', vi.fn(async () => new Response('x', { status: 200 })))
    const shown = await openAuthedPdf('/api/v1/documents/print/invoice/abc', 'Failed')
    expect(shown).toEqual({ url: 'blob:authed-pdf', blocked: true })
  })

  it('closes the tab and throws the server\'s words on a refusal', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ error: { code: 'not_found', message: 'customer not found' } }),
      { status: 404, headers: { 'Content-Type': 'application/json' } },
    )))
    await expect(openAuthedPdf('/api/v1/documents/print/statement/abc', 'Failed to read the statement')).rejects.toThrow(/customer not found|Failed to read the statement/)
    expect(tab.close).toHaveBeenCalled()
  })

  it('saves the document as a download when the tab is blocked and a file name is given', async () => {
    vi.stubGlobal('open', vi.fn(() => null))
    vi.stubGlobal('fetch', vi.fn(async () => new Response('%PDF', { status: 200 })))
    const clicked: { href: string; download: string }[] = []
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push({ href: this.getAttribute('href') ?? '', download: this.download })
    })
    try {
      const shown = await openAuthedPdf('/api/v1/documents/print/invoice/abc', 'Failed', 'invoice.pdf')
      expect(shown.blocked).toBe(true)
      expect(clicked).toEqual([{ href: 'blob:authed-pdf', download: 'invoice.pdf' }])
    } finally {
      click.mockRestore()
    }
  })
})

describe('fetchAuthedBlobUrl', () => {
  beforeEach(() => {
    localStorage.setItem('token', 'test-token')
    URL.createObjectURL = vi.fn(() => 'blob:picture')
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.removeItem('token')
  })

  it('fetches with the token and answers a blob url', async () => {
    const fetchMock = vi.fn(async () => new Response('bytes', { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    expect(await fetchAuthedBlobUrl('/api/v1/quotes/q1/file', 'Failed')).toBe('blob:picture')
    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1]
    expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer test-token')
  })

  it('throws the server\'s words on a refusal', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ error: { code: 'not_found', message: 'no original' } }),
      { status: 404, headers: { 'Content-Type': 'application/json' } },
    )))
    await expect(fetchAuthedBlobUrl('/api/v1/quotes/q1/file', 'Failed')).rejects.toThrow(/no original|Failed/)
  })
})
