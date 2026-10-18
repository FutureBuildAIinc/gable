// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * A hand made journal entry belongs to one branch and goes over the wire in
 * integer cents, whatever dollars the operator typed.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './JournalEntries'
import type { LitElement } from 'lit'
import { mountAsync, text, jsonResponse, flush, update, chooseOption } from '../../test/dom'
import { branchContext } from '../../services/BranchContext'

const BRANCH = '11111111-1111-4111-8111-111111111111'

beforeEach(() => {
  localStorage.clear()
  ;(branchContext as unknown as { _branches: unknown[]; _currentId: string | null })._branches = [
    { id: BRANCH, code: 'KEL', name: 'Kelowna Main', is_home: true },
  ]
  ;(branchContext as unknown as { _currentId: string | null })._currentId = BRANCH
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('new journal entry', () => {
  it('sends its branch and integer cents', async () => {
    const posted: unknown[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init?: RequestInit) => {
        if (url.includes('/gl/accounts')) {
          return Promise.resolve(jsonResponse({
            items: [
              { id: 'acct-cash', code: '1010', name: 'Cash' },
              { id: 'acct-sales', code: '4010', name: 'Sales' },
            ],
            next_cursor: null,
            limit: 200,
          }))
        }
        if (init?.method === 'POST') {
          posted.push(JSON.parse(String(init.body)))
          return Promise.resolve(jsonResponse({ id: 'je-new' }, 201))
        }
        return Promise.resolve(jsonResponse({ items: [], next_cursor: null, limit: 200 }))
      }),
    )
    const el = await mountAsync<LitElement>('gable-journal-entries')
    Array.from(el.querySelectorAll('button')).find(b => text(b).includes('New Entry'))!.click()
    await update(el, {})

    const selects = Array.from(el.querySelectorAll<HTMLElement & { value: string }>('gable-select'))
    // The branch select comes first, then one account select per line.
    await chooseOption(selects[1], 'acct-cash')
    await chooseOption(selects[2], 'acct-sales')
    await update(el, {})
    const numbers = Array.from(el.querySelectorAll<HTMLInputElement>('input[type="number"]'))
    for (const [input, value] of [[numbers[0], '73.88'], [numbers[3], '73.88']] as [HTMLInputElement, string][]) {
      input.value = value
      input.dispatchEvent(new Event('input', { bubbles: true }))
      await update(el, {})
    }

    Array.from(el.querySelectorAll('button')).find(b => text(b).includes('Create Entry'))!.click()
    await flush()

    expect(posted).toHaveLength(1)
    const body = posted[0] as { branch_id: string; lines: { account_id: string; debit_cents: number; credit_cents: number }[] }
    expect(body.branch_id).toBe(BRANCH)
    expect(body.lines).toEqual([
      expect.objectContaining({ account_id: 'acct-cash', debit_cents: 7388, credit_cents: 0 }),
      expect.objectContaining({ account_id: 'acct-sales', debit_cents: 0, credit_cents: 7388 }),
    ])
  })
})
