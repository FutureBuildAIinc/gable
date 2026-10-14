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
import { mount, q } from '../../test/dom'

describe('kpi card', () => {
  it('draws its icon once: the boxed icon only, no faded copy behind it', async () => {
    const el = await mount<GableKPICard>('gable-kpi-card', {
      cardTitle: "Today's Revenue",
      value: '$100.00',
      iconHtml: icon(DollarSign, 20, 'w-5 h-5'),
    })

    expect(el.querySelectorAll('svg').length).toBe(1)
  })

  it('a card with a drill-in target is a real link named for a screen reader', async () => {
    const el = await mount<GableKPICard>('gable-kpi-card', {
      cardTitle: "Today's Revenue",
      value: '$100.00',
      iconHtml: icon(DollarSign, 20, 'w-5 h-5'),
      href: '/reports/daily-till',
      linkLabel: "Open today's sales",
    })

    const link = q<HTMLAnchorElement>(el, 'a[href="/reports/daily-till"]')
    expect(link.getAttribute('aria-label')).toBe("Open today's sales")
  })
})
