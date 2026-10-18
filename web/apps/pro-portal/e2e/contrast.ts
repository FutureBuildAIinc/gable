// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

export interface ContrastSample {
  surface: string
  theme: string
  selector: string
  text: string
  ratio: number
  kind: 'text' | 'control'
}

export async function collectContrast(page: import('@playwright/test').Page, surface: string, theme: string): Promise<ContrastSample[]> {
  return page.evaluate(({ surfaceName, themeName }) => {
    function channel(value: number): number {
      const c = value / 255
      return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
    }
    function luminance(rgb: [number, number, number]): number {
      return 0.2126 * channel(rgb[0]) + 0.7152 * channel(rgb[1]) + 0.0722 * channel(rgb[2])
    }
    function parse(value: string): [number, number, number, number] {
      const values = value.match(/[\d.]+/g)?.map(Number) ?? [0, 0, 0, 1]
      return [values[0] ?? 0, values[1] ?? 0, values[2] ?? 0, values[3] ?? 1]
    }
    function blend(fg: [number, number, number, number], bg: [number, number, number]): [number, number, number] {
      return [
        fg[0] * fg[3] + bg[0] * (1 - fg[3]),
        fg[1] * fg[3] + bg[1] * (1 - fg[3]),
        fg[2] * fg[3] + bg[2] * (1 - fg[3]),
      ]
    }
    function background(el: Element): [number, number, number] {
      const layers: [number, number, number, number][] = []
      let node: Element | null = el
      while (node !== null) {
        const color = parse(getComputedStyle(node).backgroundColor)
        if (color[3] > 0) layers.push(color)
        node = node.parentElement
      }
      const base: [number, number, number] = [255, 255, 255]
      return layers.reverse().reduce((bg, layer) => blend(layer, bg), base)
    }
    function ratio(fg: [number, number, number], bg: [number, number, number]): number {
      const a = luminance(fg)
      const b = luminance(bg)
      return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
    }
    const samples: ContrastSample[] = []
    const elements = Array.from(document.querySelectorAll('body *')).filter((el) => {
      const style = getComputedStyle(el)
      const rect = el.getBoundingClientRect()
      if (style.display === 'none' || style.visibility === 'hidden' || rect.width === 0 || rect.height === 0) return false
      const directText = Array.from(el.childNodes).some((node) => node.nodeType === Node.TEXT_NODE && (node.textContent?.trim() ?? '') !== '')
      return directText || el.matches('button, a, input, select, textarea, [role="button"], [role="checkbox"], [role="radio"]')
    })
    for (const el of elements) {
      const style = getComputedStyle(el)
      const fg = blend(parse(style.color), background(el))
      const interactive = el.matches('button, a, input, select, textarea, [role="button"], [role="checkbox"], [role="radio"]')
      const directText = Array.from(el.childNodes).some((node) => node.nodeType === Node.TEXT_NODE && (node.textContent?.trim() ?? '') !== '')
      const text = (el.textContent ?? '').trim().replace(/\s+/g, ' ').slice(0, 80)
      const selector = el.getAttribute('data-testid') ?? `${el.tagName.toLowerCase()}.${el.className.toString().split(' ').filter(Boolean).slice(0, 2).join('.')}`
      if (directText) samples.push({ surface: surfaceName, theme: themeName, selector, text, ratio: Math.round(ratio(fg, background(el)) * 100) / 100, kind: 'text' })
      if (interactive) {
        const border = parse(style.borderTopColor)
        if (border[3] > 0 && Number.parseFloat(style.borderTopWidth) > 0 && style.borderTopStyle !== 'none') {
          const borderFg = blend(border, background(el.parentElement ?? el))
          samples.push({
            surface: surfaceName,
            theme: themeName,
            selector: `border:${selector}`,
            text,
            ratio: Math.round(ratio(borderFg, background(el.parentElement ?? el)) * 100) / 100,
            kind: 'control',
          })
        }
      }
    }
    return samples
  }, { surfaceName: surface, themeName: theme })
}
