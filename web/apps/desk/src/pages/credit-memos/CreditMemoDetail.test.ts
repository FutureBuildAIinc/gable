// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The credit memo page (C2-3): negative cents and quantities shown as credits
 * and never flipped, a draft with no number, and post and void on the loaded
 * revision.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './CreditMemoDetail'
import type { GableCreditMemoDetail } from './CreditMemoDetail'
import { mountAsync, text, flush } from '../../test/dom'
import { creditMemo, creditLine, invoice, routedFetch, INVOICE_ID } from '../../test/salesFixtures'
import type { CreditMemo } from '../../types/creditMemo'

const MEMO_ID = creditMemo().id

function stub(memo: CreditMemo, extra: Record<string, (url: URL, init?: RequestInit) => unknown> = {}) {
  const state = routedFetch({
    [`/api/v1/credit-memos/${MEMO_ID}`]: () => memo,
    [`/api/v1/invoices/${INVOICE_ID}`]: () => invoice(),
    ...extra,
  })
  vi.stubGlobal('fetch', vi.fn(state.fn))
  return state
}

function buttonByText(el: Element, label: string): HTMLButtonElement {
  return Array.from(el.querySelectorAll('button')).find(b => b.textContent?.includes(label)) as HTMLButtonElement
}

describe('gable-credit-memo-detail', () => {
  let el: GableCreditMemoDetail
  afterEach(() => vi.unstubAllGlobals())

  it('shows a draft as Draft with its explanation and Post, Edit and Void', async () => {
    stub(creditMemo())
    el = await mountAsync<GableCreditMemoDetail>('gable-credit-memo-detail', { routeId: MEMO_ID })
    expect(text(el.querySelector('h1'))).toBe('Draft credit memo')
    expect(text(el.querySelector('[data-testid="draft-banner"]'))).toContain('no number')
    expect(buttonByText(el, 'Post')).toBeTruthy()
    expect(el.querySelector(`a[href="/credit-memos/${MEMO_ID}/edit"]`)).not.toBeNull()
    expect(buttonByText(el, 'Void')).toBeTruthy()
    expect(text(el)).toContain('IN-000042')
  })

  it('keeps stored signs: negative quantity and extension are shown as stored, in red', async () => {
    stub(creditMemo())
    el = await mountAsync<GableCreditMemoDetail>('gable-credit-memo-detail', { routeId: MEMO_ID })
    const row = el.querySelector('tr[data-line-type="product"]')!
    expect(text(row)).toContain('-2')
    expect(text(row)).toContain('-$64.80')
    expect(row.querySelectorAll('.text-red-400').length).toBeGreaterThanOrEqual(2)
    const totals = text(el.querySelector('[data-testid="credit-totals"]'))
    expect(totals).toContain('-$64.80')
    expect(totals).toContain('-$5.75')
    expect(text(el.querySelector('[data-testid="credit-total"]'))).toBe('-$70.55')
    expect(totals).toContain('8.875%')
  })

  it('shows the restock flag per line', async () => {
    stub(creditMemo({ lines: [creditLine({ restock: true }), creditLine({ id: 'l2', position: 1, restock: false, sku: 'X' })] }))
    el = await mountAsync<GableCreditMemoDetail>('gable-credit-memo-detail', { routeId: MEMO_ID })
    const cells = Array.from(el.querySelectorAll('tr[data-line-type="product"]')).map(r => text(r.children[6]))
    expect(cells).toEqual(['Yes', '-'])
  })

  it('posts after a confirm that says what posting does, on the loaded revision', async () => {
    const state = stub(creditMemo({ revision: 3 }), {
      [`POST /api/v1/credit-memos/${MEMO_ID}/transitions`]: () => creditMemo({ status: 'open', number: 'CM-000009', revision: 4 }),
    })
    el = await mountAsync<GableCreditMemoDetail>('gable-credit-memo-detail', { routeId: MEMO_ID })
    buttonByText(el, 'Post').click()
    await el.updateComplete
    const dialog = el.querySelector('gable-sales-dialog')!
    const body = text(dialog)
    expect(body).toContain('assigns')
    expect(body).toContain('number')
    expect(body).toContain('restock')
    expect(body).toContain('ledger')
    ;(Array.from(dialog.querySelectorAll('button')).find(b => b.textContent?.includes('Post credit memo')) as HTMLButtonElement).click()
    await flush()
    await flush()
    await el.updateComplete
    const call = state.calls.find(c => c.url.pathname.endsWith('/transitions'))!
    expect(JSON.parse(String(call.init!.body))).toEqual({ to: 'open', revision: 3 })
    expect(text(el.querySelector('h1'))).toBe('CM-000009')
    expect(text(el)).toContain('Open')
    expect(el.querySelector('[data-testid="draft-banner"]')).toBeNull()
    expect(el.querySelector('a[href$="/edit"]')).toBeNull()
  })

  it('voids with a required reason and shows the VOID banner', async () => {
    const state = stub(creditMemo({ status: 'open', number: 'CM-000009', revision: 2 }), {
      [`POST /api/v1/credit-memos/${MEMO_ID}/transitions`]: () => creditMemo({ status: 'void', number: 'CM-000009', revision: 3, void_reason: 'entered twice' }),
    })
    el = await mountAsync<GableCreditMemoDetail>('gable-credit-memo-detail', { routeId: MEMO_ID })
    buttonByText(el, 'Void').click()
    await el.updateComplete
    const dialog = el.querySelector('gable-sales-dialog')!
    const confirmBtn = Array.from(dialog.querySelectorAll('button')).find(b => b.textContent?.includes('Void credit memo')) as HTMLButtonElement
    expect(confirmBtn.disabled).toBe(true)
    const textarea = dialog.querySelector('textarea') as HTMLTextAreaElement
    textarea.value = 'entered twice'
    textarea.dispatchEvent(new Event('input'))
    await flush()
    await (dialog as unknown as { updateComplete: Promise<boolean> }).updateComplete
    confirmBtn.click()
    await flush()
    await flush()
    await el.updateComplete
    const call = state.calls.find(c => c.url.pathname.endsWith('/transitions'))!
    expect(JSON.parse(String(call.init!.body))).toEqual({ to: 'void', revision: 2, reason: 'entered twice' })
    expect(text(el.querySelector('[data-testid="void-banner"]'))).toContain('entered twice')
  })

  it('shows the server refusal inside the dialog and keeps it open', async () => {
    stub(creditMemo({ status: 'open', number: 'CM-000009' }), {
      [`POST /api/v1/credit-memos/${MEMO_ID}/transitions`]: () =>
        new Response(JSON.stringify({ error: { code: 'conflict', message: 'the credit memo has been used: reverse its applications and refunds before voiding it', details: [{ code: 'has_applications', message: 'x' }] }, meta: {} }), { status: 409, headers: { 'Content-Type': 'application/json' } }),
    })
    el = await mountAsync<GableCreditMemoDetail>('gable-credit-memo-detail', { routeId: MEMO_ID })
    buttonByText(el, 'Void').click()
    await el.updateComplete
    const dialog = el.querySelector('gable-sales-dialog')!
    const textarea = dialog.querySelector('textarea') as HTMLTextAreaElement
    textarea.value = 'why'
    textarea.dispatchEvent(new Event('input'))
    await flush()
    await (dialog as unknown as { updateComplete: Promise<boolean> }).updateComplete
    ;(Array.from(dialog.querySelectorAll('button')).find(b => b.textContent?.includes('Void credit memo')) as HTMLButtonElement).click()
    await flush()
    await flush()
    await el.updateComplete
    await (dialog as unknown as { updateComplete: Promise<boolean> }).updateComplete
    expect(text(el.querySelector('[data-testid="dialog-error"]'))).toContain('has been used')
    expect(el.querySelector('[role="dialog"]')).not.toBeNull()
  })
})
