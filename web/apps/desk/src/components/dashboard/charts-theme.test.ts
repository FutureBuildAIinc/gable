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
const resized: number[] = []
let chartCount = 0

vi.mock('chart.js/auto', () => ({
  default: class {
    readonly id: number
    constructor(_canvas: unknown, config: Built) {
      built.push(config)
      this.id = ++chartCount
    }
    destroy() {}
    resize() {
      resized.push(this.id)
    }
  },
}))

/** A ResizeObserver that records what it watches and lets a test fire it. */
interface StubObserver {
  callback: ResizeObserverCallback
  observed: Element[]
  disconnected: boolean
}
const observers: StubObserver[] = []
class StubResizeObserver {
  private readonly rec: StubObserver
  constructor(callback: ResizeObserverCallback) {
    this.rec = { callback, observed: [], disconnected: false }
    observers.push(this.rec)
  }
  observe(el: Element) {
    this.rec.observed.push(el)
  }
  unobserve() {}
  disconnect() {
    this.rec.disconnected = true
  }
}
vi.stubGlobal('ResizeObserver', StubResizeObserver)

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
    setVars({ 'zinc-400': '161 161 170', 'gable-green': '0 255 163', 'chart-series-1': '0 255 163', 'chart-tick': '82 82 91' })
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

    setVars({ 'zinc-400': '90 86 81', 'gable-green': '4 108 67', 'chart-series-1': '4 92 56' })
    setTheme('light')
    await frame()
    expect(built.at(-1)?.options.plugins.legend.labels.color).toBe('rgb(90 86 81 / 1)')
    expect(built.at(-1)?.data.datasets[0].backgroundColor[0]).toBe('rgb(4 92 56 / 1)')
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

    setVars({ 'chart-tick': '109 104 99', 'gable-green': '4 108 67' })
    setTheme('light')
    await frame()
    const light = built.at(-1)!
    expect(light.data.datasets[0].borderColor).toBe('rgb(4 108 67 / 1)')
    expect(light.options.scales.y.ticks.color).toBe('rgb(109 104 99 / 1)')
    el.remove()
  })

  // A chart must follow its card when the page narrows after it was drawn
  // wide. Chart.js sizes the canvas in pixels, so the canvas must take no
  // layout space of its own: it sits absolutely inside a relatively positioned
  // wrapper that takes its width from the card, and the host is a block.
  it.each([
    ['gable-order-status-chart', { statusBreakdown: { confirmed: 3, draft: 1 } }],
    ['gable-revenue-trend-chart', { data: [{ date: 'd1', revenue: 1000 }] }],
  ])('%s sizes from its card: block host, relative wrapper, absolute canvas', async (tag, props) => {
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({
      createLinearGradient: () => ({ addColorStop: () => {} }),
    } as unknown as CanvasRenderingContext2D)
    const el = await mount<GableOrderStatusChart>(tag, props as never)
    await frame()
    const wrap = el.querySelector('[data-testid="chart-canvas-wrap"]') as HTMLElement
    const canvas = wrap.querySelector('canvas') as HTMLCanvasElement
    expect(el.style.display).toBe('block')
    expect(wrap.className).toContain('relative')
    expect(wrap.className).toContain('h-full')
    expect(canvas.className).toContain('absolute')
    expect(canvas.className).not.toContain('w-full')
    // the card is not clipped: the donut, its legend and its centre are never cut
    expect(el.querySelector('.overflow-hidden')).toBeNull()
    el.remove()
  })

  // Chart.js's own resize did not reach the canvas on a real window resize
  // (the wrapper shrank, the canvas kept its pixel width), so each chart
  // watches its wrapper and resizes the chart itself.
  it.each([
    ['gable-order-status-chart', { statusBreakdown: { confirmed: 3, draft: 1 } }],
    ['gable-revenue-trend-chart', { data: [{ date: 'd1', revenue: 1000 }] }],
  ])('%s resizes its chart when its wrapper changes size', async (tag, props) => {
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({
      createLinearGradient: () => ({ addColorStop: () => {} }),
    } as unknown as CanvasRenderingContext2D)
    observers.length = 0
    resized.length = 0
    const el = await mount<GableOrderStatusChart>(tag, props as never)
    await frame()
    const wrap = el.querySelector('[data-testid="chart-canvas-wrap"]') as HTMLElement
    const live = () => observers.filter((o) => !o.disconnected && o.observed.includes(wrap))
    expect(live().length).toBe(1)

    // the wrapper changed size: the chart is resized
    live()[0].callback([], {} as ResizeObserver)
    expect(resized.length).toBe(1)

    // a mode change rebuilds the chart: the new chart is watched, the old one is not
    setTheme('light')
    await frame()
    expect(live().length).toBe(1)
    expect(observers.filter((o) => !o.disconnected).length).toBe(1)
    live()[0].callback([], {} as ResizeObserver)
    expect(resized.length).toBe(2)
    expect(resized[1]).not.toBe(resized[0])

    // removed from the page: nothing is left watching
    el.remove()
    expect(observers.every((o) => o.disconnected)).toBe(true)
  })
})
