// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dashboard's KPI card: one icon per card (the boxed icon, never the
 * faded enlarged copy behind it), and a card that drills in is a real link
 * with a name a screen reader announces.
 */
import { describe, it, expect } from 'vitest'
import { icon } from '../../lib/icons.ts'
import { DollarSign } from 'lucide'
import './KPICard.ts'
import type { GableKPICard } from './KPICard.ts'
import { mount, q, text } from '../../test/dom'

describe('kpi card', () => {
  it('draws its icon once: the boxed icon only, no faded copy behind it', async () => {
    const el = await mount<GableKPICard>('gable-kpi-card', {
      cardTitle: "Today's Revenue",
      value: '$100.00',
      iconHtml: icon(DollarSign, 20, 'w-5 h-5'),
    })

    expect(el.querySelectorAll('svg').length).toBe(1)
  })

  it('a card with a drill-in target is a real link whose name keeps the figure and adds the action', async () => {
    const el = await mount<GableKPICard>('gable-kpi-card', {
      cardTitle: "Today's Revenue",
      value: '$100.00',
      iconHtml: icon(DollarSign, 20, 'w-5 h-5'),
      href: '/reports/daily-till',
      linkLabel: "Open today's sales",
    })

    const link = q<HTMLAnchorElement>(el, 'a[href="/reports/daily-till"]')
    // No aria-label: it would replace the card's content as the accessible
    // name and hide the figure. The action is visually hidden text instead.
    expect(link.getAttribute('aria-label')).toBeNull()
    expect(text(link)).toContain("Open today's sales")
    expect(text(link)).toContain('$100.00')
    expect(text(link)).toContain("Today's Revenue")
  })
})
