// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The four accounting pages default "today" from the module's todayDay, in
 * the person's own zone (PR 156 review P2-7). Late in the day west of UTC
 * the UTC slice is already tomorrow: a journal entry posted without looking
 * would be dated, and its period, a day late; the balance sheet and trial
 * balance would open as of tomorrow.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './BalanceSheet'
import './ProfitAndLoss'
import './TrialBalance'
import './JournalEntries'

afterEach(() => {
  vi.useRealTimers()
})

describe('the accounting pages default today in the person\'s zone', () => {
  it('late in the day west of UTC, the default is the local day, not tomorrow', () => {
    const tz = process.env.TZ
    process.env.TZ = 'America/Los_Angeles'
    // 2026-10-15T02:00:00Z is Oct 14 at 7 PM in Kelowna; the UTC slice is
    // already the 15th.
    vi.useFakeTimers({ now: new Date('2026-10-15T02:00:00Z') })
    try {
      expect(new Date().toISOString().slice(0, 10)).toBe('2026-10-15')
      const cases: Array<[string, string]> = [
        ['gable-balance-sheet', 'asOfDate'],
        ['gable-profit-and-loss', 'endDate'],
        ['gable-trial-balance', 'asOfDate'],
        ['gable-journal-entries', 'entryDate'],
      ]
      for (const [tag, prop] of cases) {
        const el = document.createElement(tag) as unknown as Record<string, unknown>
        expect(el[prop], `${tag} ${prop}`).toBe('2026-10-14')
      }
    } finally {
      process.env.TZ = tz
    }
  })
})
