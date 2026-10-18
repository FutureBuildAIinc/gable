// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { GableCustomerQuotePage } from './public-page.ts'
import { sanitizePublicQuote, type PublicQuote } from './public-view.ts'
import { publicIds, publicQuoteFixture, publicToken, signedPublicQuoteFixture } from '../test/public-fixtures.ts'

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function readyFixture(): PublicQuote {
  const ready = sanitizePublicQuote(publicQuoteFixture)
  ready.lines = ready.lines.map((line) => ({ ...line, selection: line.id === publicIds.lineB ? 'declined' : 'approved' }))
  return ready
}

async function mountHosted(fixture: unknown): Promise<GableCustomerQuotePage> {
  const element = new GableCustomerQuotePage()
  element.hosted = true
  element.quote = fixture as PublicQuote
  document.body.append(element)
  await element.updateComplete
  return element
}

async function mountPublic(fixture: unknown): Promise<{ element: GableCustomerQuotePage, fetchMock: ReturnType<typeof vi.fn> }> {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof Request ? input.url : input.toString(), 'http://localhost')
    if (url.pathname.endsWith('/code')) return new Response(null, { status: 202 })
    if (url.pathname.endsWith('/accept')) return json(signedPublicQuoteFixture)
    if (url.pathname.endsWith('/selections')) {
      const body = JSON.parse(String(init?.body)) as { choices: { line_id: string, state: string }[] }
      const next = sanitizePublicQuote(fixture)
      next.lines = next.lines.map((line) => body.choices.some((choice) => choice.line_id === line.id) ? { ...line, selection: body.choices[0]?.state as 'approved' | 'declined' } : line)
      return json(next)
    }
    return json(fixture)
  })
  vi.stubGlobal('fetch', fetchMock)
  const element = new GableCustomerQuotePage()
  document.body.append(element)
  await vi.waitFor(() => expect(element.querySelector('[data-testid="public-selections"]')).not.toBeNull())
  return { element, fetchMock }
}

function input(element: Element, selector: string, value: string): void {
  const field = element.querySelector<HTMLInputElement>(selector)
  if (field === null) throw new Error(`Missing input ${selector}`)
  field.value = value
  field.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('customer public quote page', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    localStorage.clear()
    sessionStorage.clear()
    window.history.replaceState({}, '', `/q/${publicToken}`)
    Object.defineProperty(navigator, 'onLine', { value: true, configurable: true })
    vi.restoreAllMocks()
  })

  it('renders no dealer cost, markup, margin, or contractor only fixture data', async () => {
    const element = await mountHosted(publicQuoteFixture)
    const dom = element.textContent ?? ''
    expect(dom).toContain('Cedar decking')
    expect(dom).not.toContain('Secret internal labour')
    for (const forbidden of ['cost_cents', 'dealer_cost', 'markup', 'margin_bps', '$1,111.11', '$2,222.22', '$4,444.44', '$5,555.55', '$6,666.66', '$7,777.77']) {
      expect(dom).not.toContain(forbidden)
    }
    const lump = sanitizePublicQuote({ ...publicQuoteFixture, hide_line_prices: true })
    const lumpElement = await mountHosted(lump)
    expect(lumpElement.textContent).toContain('Lump sum quote')
    expect(lumpElement.textContent).not.toContain('$120.00')
    expect(lumpElement.textContent).not.toContain('$180.00')
  })

  it('moves through the code step and records a typed signature', async () => {
    const { element, fetchMock } = await mountPublic(readyFixture())
    input(element, 'input[name="full_name"]', 'Customer Signoff')
    input(element, 'input[type="email"]', 'customer@example.com')
    await element.updateComplete
    const form = element.querySelector('form')
    form?.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }))
    await vi.waitFor(() => expect(element.querySelector('[data-testid="verification-code"]')).not.toBeNull())
    input(element, '[data-testid="verification-code"]', '123456')
    await element.updateComplete
    form?.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }))
    await vi.waitFor(() => expect(element.querySelector('[data-testid="public-signed"]')).not.toBeNull())
    const acceptCall = fetchMock.mock.calls.find(([input]) => String(input).includes('/accept'))
    const body = JSON.parse(String(acceptCall?.[1]?.body))
    expect(body.code).toBe('123456')
    expect(body.signature).toEqual({ kind: 'typed', text: 'Customer Signoff' })
    expect(body.full_name).toBe('Customer Signoff')
    expect(body.selections_version).toBe(1)
  })

  it('saves a selection and disables public writes offline', async () => {
    const { element, fetchMock } = await mountPublic(publicQuoteFixture)
    element.querySelector<HTMLButtonElement>(`[data-testid="approve-${publicIds.lineB}"]`)?.click()
    await vi.waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/selections'))).toBe(true))
    window.dispatchEvent(new Event('offline'))
    await element.updateComplete
    expect(element.querySelector<HTMLButtonElement>(`[data-testid="approve-${publicIds.lineC}"]`)?.disabled).toBe(true)
    expect(element.querySelector('[data-testid="public-offline"]')?.textContent).toContain('Nothing is queued')
  })

  it('shows the signed status without any approval controls', async () => {
    const element = await mountHosted(signedPublicQuoteFixture)
    expect(element.querySelector('[data-testid="public-signed"]')?.textContent).toContain('Quote approved')
    expect(element.querySelector('[data-testid="public-signed"]')?.textContent).toContain('Customer Signoff')
    expect(element.querySelector('[data-testid="public-signoff"]')).toBeNull()
  })
})
