// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dashboard's charts read their colours from the --gb-* variables when
 * they draw, and draw again when the mode changes, so Chart.js follows
 * Industrial Dark and Gable light.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

interface Built {
  options: {
    plugins: { legend: { labels: { color: string } } }
    scales: { y: { ticks: { color: string } } }
  }
  data: { datasets: Array<{ borderColor: string; backgroundColor: string[] }> }
}
const built: Built[] = []

vi.mock('chart.js/auto', () => ({
  default: class {
    constructor(_canvas: unknown, config: Built) {
      built.push(config)
    }
    destroy() {}
  },
}))

import './OrderStatusChart'
import './RevenueTrendChart'
import type { GableOrderStatusChart } from './OrderStatusChart'
import type { GableRevenueTrendChart } from './RevenueTrendChart'
import { setTheme } from '@gable/design-system'
import { mount } from '../../test/dom'

const root = document.documentElement
const frame = () => new Promise((resolve) => requestAnimationFrame(() => resolve(null)))

function setVars(vars: Record<string, string>) {
  for (const [k, v] of Object.entries(vars)) root.style.setProperty(`--gb-${k}`, v)
}

describe('dashboard charts follow the mode', () => {
  beforeEach(() => {
    built.length = 0
    localStorage.clear()
    setTheme('dark')
    setVars({ 'zinc-400': '161 161 170', 'gable-green': '0 255 163', 'zinc-600': '82 82 91', 'zinc-500': '113 113 122' })
  })
  afterEach(() => {
    root.removeAttribute('style')
    setTheme('dark')
  })

  it('the order ring reads its legend colour from the variable and redraws on a switch', async () => {
    const el = await mount<GableOrderStatusChart>('gable-order-status-chart', { statusBreakdown: { confirmed: 3, draft: 1 } })
    await frame()
    expect(built.at(-1)?.options.plugins.legend.labels.color).toBe('rgb(161 161 170 / 1)')
    expect(built.at(-1)?.data.datasets[0].backgroundColor[0]).toBe('rgb(0 255 163 / 1)')

    setVars({ 'zinc-400': '90 86 81', 'gable-green': '4 108 67' })
    setTheme('light')
    await frame()
    expect(built.at(-1)?.options.plugins.legend.labels.color).toBe('rgb(90 86 81 / 1)')
    expect(built.at(-1)?.data.datasets[0].backgroundColor[0]).toBe('rgb(4 108 67 / 1)')
    el.remove()
  })

  it('the revenue line reads its line, grid and tick colours from the variables', async () => {
    const stops: string[] = []
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({
      createLinearGradient: () => ({ addColorStop: (_o: number, c: string) => stops.push(c) }),
    } as unknown as CanvasRenderingContext2D)
    const el = await mount<GableRevenueTrendChart>('gable-revenue-trend-chart', {
      data: [{ date: 'd1', revenue: 1000 }],
    })
    await frame()
    const cfg = built.at(-1)!
    expect(cfg.data.datasets[0].borderColor).toBe('rgb(0 255 163 / 1)')
    expect(stops).toEqual(['rgb(0 255 163 / 0.3)', 'rgb(0 255 163 / 0)'])
    expect(cfg.options.scales.y.ticks.color).toBe('rgb(82 82 91 / 1)')

    setVars({ 'zinc-500': '109 104 99', 'gable-green': '4 108 67' })
    setTheme('light')
    await frame()
    const light = built.at(-1)!
    expect(light.data.datasets[0].borderColor).toBe('rgb(4 108 67 / 1)')
    expect(light.options.scales.y.ticks.color).toBe('rgb(109 104 99 / 1)')
    el.remove()
  })
})
