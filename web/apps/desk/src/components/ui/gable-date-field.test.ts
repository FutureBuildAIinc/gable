// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The app's date field's legs (G1 walk note N15): typing both forms (the
 * module's style and YYYY-MM-DD), the calendar's keys (arrows a day or a
 * week, Page Up and Page Down a month, Home and End the week, Enter picks,
 * Escape closes), min and max honoured, today marked, the week starting as
 * the locale starts it, and an empty field that says what to do in words,
 * never "mm/dd/yyyy".
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import type { GableDateField } from './gable-date-field'
import './gable-date-field'
import { setDealerLocale } from '../../lib/dates'
import { mount, update, q } from '../../test/dom'
import { placePopover } from './gable-date-field'

const TAG = 'gable-date-field'

beforeEach(() => setDealerLocale('en-US'))
afterEach(() => {
  document.body.innerHTML = ''
  vi.useRealTimers()
})

function input(el: GableDateField): HTMLInputElement {
  return q<HTMLInputElement>(el, 'input[type="text"]')
}

async function type(el: GableDateField, text: string) {
  const box = input(el)
  box.value = text
  box.dispatchEvent(new Event('input'))
  box.dispatchEvent(new FocusEvent('blur'))
  await update(el, {})
}

async function openCalendar(el: GableDateField) {
  q<HTMLButtonElement>(el, 'button[aria-label="Open the calendar"]').click()
  await update(el, {})
}

async function key(el: GableDateField, k: string, shift = false) {
  q<HTMLElement>(el, '[role="grid"]').dispatchEvent(
    new KeyboardEvent('keydown', { key: k, shiftKey: shift, bubbles: true }),
  )
  await update(el, {})
}

function gridDays(el: GableDateField): HTMLButtonElement[] {
  return Array.from(el.querySelectorAll<HTMLButtonElement>('[role="gridcell"] button'))
}

describe('typing', () => {
  it('takes the module style and fires change with YYYY-MM-DD', async () => {
    const el = await mount<GableDateField>(TAG)
    const seen: string[] = []
    el.addEventListener('change', () => seen.push(el.value))
    await type(el, 'Oct 14, 2026')
    expect(el.value).toBe('2026-10-14')
    expect(seen).toEqual(['2026-10-14'])
    expect(input(el).value).toBe('Oct 14, 2026')
  })

  it('takes YYYY-MM-DD', async () => {
    const el = await mount<GableDateField>(TAG)
    await type(el, '2026-10-14')
    expect(el.value).toBe('2026-10-14')
    expect(input(el).value).toBe('Oct 14, 2026')
  })

  it('reads back in the dealer locale', async () => {
    setDealerLocale('en-GB')
    const el = await mount<GableDateField>(TAG, { value: '2026-10-14' })
    expect(input(el).value).toBe('14 Oct 2026')
  })

  it('refuses what is not a day, in words, keeping the value', async () => {
    const el = await mount<GableDateField>(TAG, { value: '2026-10-14' })
    const seen: string[] = []
    el.addEventListener('change', () => seen.push(el.value))
    await type(el, 'whenever')
    expect(el.value).toBe('2026-10-14')
    expect(seen).toEqual([])
    expect(input(el).getAttribute('aria-invalid')).toBe('true')
    expect(el.textContent).toContain('Use a day like')
  })

  it('an empty box clears the value', async () => {
    const el = await mount<GableDateField>(TAG, { value: '2026-10-14' })
    await type(el, '')
    expect(el.value).toBe('')
    expect(input(el).value).toBe('')
  })

  it('an empty field says what to do, never mm/dd/yyyy', async () => {
    const el = await mount<GableDateField>(TAG)
    expect(input(el).placeholder).toBe('Choose a date')
    expect(el.innerHTML).not.toContain('mm/dd/yyyy')
  })

  it('is named for a screen reader', async () => {
    const el = await mount<GableDateField>(TAG, { label: 'As of' })
    expect(input(el).getAttribute('aria-label')).toBe('As of')
  })
})

describe('the calendar', () => {
  it('opens on its button, marks today, Escape closes', async () => {
    const el = await mount<GableDateField>(TAG)
    await openCalendar(el)
    expect(q(el, '[role="dialog"]')).toBeTruthy()
    const today = gridDays(el).find((b) => b.getAttribute('aria-current') === 'date')
    expect(today).toBeTruthy()
    await key(el, 'Escape')
    expect(el.querySelector('[role="dialog"]')).toBeNull()
  })

  it('opens on the value\'s month and picks with Enter', async () => {
    const el = await mount<GableDateField>(TAG, { value: '2026-10-14' })
    await openCalendar(el)
    expect(el.textContent).toContain('October 2026')
    const focused = gridDays(el).find((b) => b.tabIndex === 0)
    expect(focused?.textContent).toBe('14')
    await key(el, 'Enter')
    expect(el.value).toBe('2026-10-14')
    expect(el.querySelector('[role="dialog"]')).toBeNull()
  })

  it('arrows move a day or a week, Home and End the week', async () => {
    // Wednesday Oct 14, 2026; the en-US week starts Sunday.
    const el = await mount<GableDateField>(TAG, { value: '2026-10-14' })
    await openCalendar(el)
    await key(el, 'ArrowRight')
    await key(el, 'Enter')
    expect(el.value).toBe('2026-10-15')
    await openCalendar(el)
    await key(el, 'ArrowUp')
    await key(el, 'Enter')
    expect(el.value).toBe('2026-10-08')
    await openCalendar(el)
    await key(el, 'ArrowDown')
    await key(el, 'Enter')
    expect(el.value).toBe('2026-10-15')
    await openCalendar(el)
    await key(el, 'Home') // the Sunday of the 15th's week
    await key(el, 'Enter')
    expect(el.value).toBe('2026-10-11')
    await openCalendar(el)
    await key(el, 'End')
    await key(el, 'Enter')
    expect(el.value).toBe('2026-10-17')
  })

  it('Page Up and Page Down move a month', async () => {
    const el = await mount<GableDateField>(TAG, { value: '2026-10-14' })
    await openCalendar(el)
    await key(el, 'PageUp')
    expect(el.textContent).toContain('September 2026')
    await key(el, 'Enter')
    expect(el.value).toBe('2026-09-14')
    await openCalendar(el)
    await key(el, 'PageDown')
    await key(el, 'PageDown')
    expect(el.textContent).toContain('November 2026')
  })

  it('keeps focus on the day it lands on when the month turns', async () => {
    // Jan 31, ArrowRight into February: the old cell's node goes away; the
    // focus must land on the Feb 1 button, not the body.
    const el = await mount<GableDateField>(TAG, { value: '2026-01-31' })
    await openCalendar(el)
    const start = gridDays(el).find((b) => b.tabIndex === 0)
    if (!start) throw new Error('no focused cell')
    start.focus()
    expect(document.activeElement).toBe(start)
    await key(el, 'ArrowRight')
    const landed = gridDays(el).find((b) => b.tabIndex === 0)
    expect(el.textContent).toContain('February 2026')
    expect(landed?.textContent).toBe('1')
    expect(document.activeElement).toBe(landed)
    // From there the keys still reach the grid: Escape closes.
    await key(el, 'Escape')
    expect(el.querySelector('[role="dialog"]')).toBeNull()
  })

  it('the focusout Chromium sends on removal does not lose the focus', async () => {
    // Chromium fires blur and focusout (relatedTarget null) on the focused
    // cell when the render removes it; jsdom fires nothing. The sequence is
    // replayed by hand: the key lands, the focusout follows before the
    // render settles, and the Feb 1 button must still end up focused.
    const el = await mount<GableDateField>(TAG, { value: '2026-01-31' })
    await openCalendar(el)
    const start = gridDays(el).find((b) => b.tabIndex === 0)
    if (!start) throw new Error('no focused cell')
    start.focus()
    q<HTMLElement>(el, '[role="grid"]').dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }),
    )
    start.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: null }))
    await update(el, {})
    const landed = gridDays(el).find((b) => b.tabIndex === 0)
    expect(el.textContent).toContain('February 2026')
    expect(landed?.textContent).toBe('1')
    expect(document.activeElement).toBe(landed)
  })

  it('the week starts as the locale starts it', async () => {
    // Oct 2026 starts on a Thursday: four blanks under en-US, three under en-GB.
    const el = await mount<GableDateField>(TAG, { value: '2026-10-01' })
    await openCalendar(el)
    let blanks = 0
    for (const c of Array.from(el.querySelectorAll('[role="grid"] > span'))) {
      if (c.getAttribute('role') === 'gridcell') break
      if (c.textContent?.trim() === '') blanks++
    }
    expect(blanks).toBe(4)
    await key(el, 'Escape')
    setDealerLocale('en-GB')
    const el2 = await mount<GableDateField>(TAG, { value: '2026-10-01' })
    await openCalendar(el2)
    let blanks2 = 0
    for (const c of Array.from(el2.querySelectorAll('[role="grid"] > span'))) {
      if (c.getAttribute('role') === 'gridcell') break
      if (c.textContent?.trim() === '') blanks2++
    }
    expect(blanks2).toBe(3)
  })
})

describe('min and max', () => {
  it('a typed day outside the bounds is refused', async () => {
    const el = await mount<GableDateField>(TAG, { min: '2026-10-10', max: '2026-10-20' })
    await type(el, 'Oct 1, 2026')
    expect(el.value).toBe('')
    expect(input(el).getAttribute('aria-invalid')).toBe('true')
    await type(el, '2026-10-25')
    expect(el.value).toBe('')
    await type(el, '2026-10-15')
    expect(el.value).toBe('2026-10-15')
  })

  it('grid days outside the bounds are disabled and the keys clamp to them', async () => {
    const el = await mount<GableDateField>(TAG, { value: '2026-10-14', min: '2026-10-10', max: '2026-10-20' })
    await openCalendar(el)
    const days = gridDays(el)
    expect(days.find((b) => b.textContent === '9')?.disabled).toBe(true)
    expect(days.find((b) => b.textContent === '21')?.disabled).toBe(true)
    expect(days.find((b) => b.textContent === '14')?.disabled).toBe(false)
    // The keys never land on a disabled day: two weeks up from the 14th
    // clamps to the 10th, and Enter picks it.
    await key(el, 'ArrowUp')
    await key(el, 'ArrowUp')
    const focused = gridDays(el).find((b) => b.tabIndex === 0)
    expect(focused?.textContent).toBe('10')
    await key(el, 'Enter')
    expect(el.value).toBe('2026-10-10')
  })
})

describe('the popover stays inside the screen', () => {
  // The estimate the field places with before the popover is measured.
  const ESTIMATE = { width: 256, height: 320 }

  it('placePopover clamps sideways and flips upward when the space below is short', () => {
    // Room below: under the field, at the field's left.
    expect(placePopover({ left: 100, bottom: 200, top: 176 } as DOMRect, ESTIMATE, 1024, 768))
      .toEqual({ top: 204, left: 100 })
    // At the right edge: clamped to the viewport.
    expect(placePopover({ left: 1000, bottom: 200, top: 176 } as DOMRect, ESTIMATE, 1024, 768).left)
      .toBe(1024 - 264)
    // At the left edge: clamped to the margin.
    expect(placePopover({ left: -30, bottom: 200, top: 176 } as DOMRect, ESTIMATE, 1024, 768).left).toBe(8)
    // Near the bottom with more room above: flipped over the field.
    expect(placePopover({ left: 100, bottom: 740, top: 716 } as DOMRect, ESTIMATE, 1024, 768))
      .toEqual({ top: 716 - 320 - 4, left: 100 })
    // A viewport too short for either side: flipped, clamped to the top margin.
    const p = placePopover({ left: 100, bottom: 204, top: 180 } as DOMRect, ESTIMATE, 1024, 340)
    expect(p.top).toBe(8)
  })

  it('flipped, the bottom edge sits a few pixels above the field, by the measured height', () => {
    // The calendar measured shorter than the estimate: its bottom edge is
    // still the field's top, less the gap; it does not float above the field.
    const size = { width: 256, height: 260 }
    const at = placePopover({ left: 100, bottom: 740, top: 716 } as DOMRect, size, 1024, 768)
    expect(at).toEqual({ top: 716 - 260 - 4, left: 100 })
    expect(at.top + size.height).toBe(716 - 4)
  })

  it('opens below when the room is below, whatever the measured height', () => {
    expect(placePopover({ left: 100, bottom: 200, top: 176 } as DOMRect, { width: 256, height: 260 }, 1024, 768))
      .toEqual({ top: 204, left: 100 })
  })

  it('with no room either way it clamps inside the viewport', () => {
    const size = { width: 256, height: 260 }
    const at = placePopover({ left: 100, bottom: 204, top: 180 } as DOMRect, size, 1024, 340)
    expect(at.top).toBe(8)
    expect(at.top + size.height).toBeLessThanOrEqual(340 - 8)
    // A field at the very top opens downward, never above the screen.
    const low = placePopover({ left: 100, bottom: 32, top: 8 } as DOMRect, size, 1024, 340)
    expect(low.top).toBe(36)
  })

  it('the open popover is fixed at the field, not clipped by a modal', async () => {
    const el = await mount<GableDateField>(TAG, { value: '2026-10-14' })
    await openCalendar(el)
    const pop = q<HTMLElement>(el, '[role="dialog"]')
    expect(pop.style.position).toBe('fixed')
    expect(pop.style.top).toBe('4px')
    expect(pop.style.left).toBe('8px')
  })

  // A field near the bottom of a dialog, its label drawn by the page just
  // above it (the way every form rows them).
  async function fieldNearTheFloor(withLabel = true) {
    const wrap = document.createElement('div')
    const label = document.createElement('label')
    label.textContent = 'Insurance Expiry'
    if (withLabel) wrap.appendChild(label)
    document.body.appendChild(wrap)
    const el = document.createElement(TAG) as GableDateField
    el.value = '2026-10-14'
    wrap.appendChild(el)
    await update(el, {})
    const rect = (r: { left: number; right: number; top: number; bottom: number }) =>
      ({ ...r, width: r.right - r.left, height: r.bottom - r.top, x: r.left, y: r.top, toJSON: () => ({}) }) as DOMRect
    input(el).getBoundingClientRect = () => rect({ left: 100, right: 356, top: 676, bottom: 700 })
    label.getBoundingClientRect = () => rect({ left: 100, right: 220, top: 656, bottom: 672 })
    return el
  }

  it('flipped, the popover sits against the field and its label, never over either', async () => {
    const el = await fieldNearTheFloor()
    await openCalendar(el)
    const pop = q<HTMLElement>(el, '[role="dialog"]')
    // The label's top is 656: the bottom edge sits the gap above it, so the
    // calendar covers neither the label nor the field.
    expect(pop.style.top).toBe(`${656 - 4 - 320}px`)
    el.remove()
  })

  it('re-places itself once it has measured its own height', async () => {
    const el = await fieldNearTheFloor()
    await openCalendar(el)
    const pop = q<HTMLElement>(el, '[role="dialog"]')
    Object.defineProperty(pop, 'offsetWidth', { value: 256, configurable: true })
    Object.defineProperty(pop, 'offsetHeight', { value: 260, configurable: true })
    el.requestUpdate()
    await update(el, {})
    // Measured at 260, the bottom edge moves down against the label's top.
    expect(pop.style.top).toBe(`${656 - 4 - 260}px`)
    el.remove()
  })
})

describe('the field follows its host surface', () => {
  it('token classes, so the theme follows the host', async () => {
    const el = await mount<GableDateField>(TAG)
    expect(input(el).className).toContain('text-white')
  })
})

describe('Escape with the focus elsewhere', () => {
  it('Escape closes only the open calendar; the next Escape reaches the dialog', async () => {
    // A dialog carries a date field; the calendar was opened by mouse, so the
    // focus sits elsewhere in the dialog.
    const dialog = document.createElement('div')
    let dialogEscapes = 0
    dialog.addEventListener('keydown', (e) => {
      if ((e as KeyboardEvent).key === 'Escape') dialogEscapes++
    })
    document.body.appendChild(dialog)
    const el = document.createElement(TAG) as GableDateField
    el.value = '2026-10-14'
    dialog.appendChild(el)
    await update(el, {})
    const elsewhere = document.createElement('button')
    elsewhere.textContent = 'Some other control'
    dialog.appendChild(elsewhere)
    await openCalendar(el)
    expect(q(el, '[role="dialog"]')).toBeTruthy()
    elsewhere.focus()
    elsewhere.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await update(el, {})
    expect(el.querySelector('[role="dialog"]'), 'the first Escape closes the calendar').toBeNull()
    expect(dialogEscapes, 'the dialog never sees the first Escape').toBe(0)
    expect(document.activeElement, 'the focus stays where it was').toBe(elsewhere)
    elsewhere.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    expect(dialogEscapes, 'the next Escape is the dialog\'s own').toBe(1)
    dialog.remove()
  })

  it('Escape with the focus in the page outside the dialog still closes only the calendar', async () => {
    const el = await mount<GableDateField>(TAG, { value: '2026-10-14' })
    await openCalendar(el)
    const outside = document.createElement('button')
    document.body.appendChild(outside)
    outside.focus()
    outside.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await update(el, {})
    expect(el.querySelector('[role="dialog"]')).toBeNull()
    expect(document.activeElement).toBe(outside)
    outside.remove()
  })
})

describe('the calendar opens in the browser top layer', () => {
  it('inside a transform and overflow hidden ancestor, nothing can offset or clip it', async () => {
    // jsdom has no popover API; stub it to watch the calls a real engine gets.
    const proto = HTMLElement.prototype as unknown as {
      showPopover?: (this: HTMLElement) => void
      hidePopover?: (this: HTMLElement) => void
    }
    const shown: HTMLElement[] = []
    proto.showPopover = function (this: HTMLElement) { shown.push(this) }
    proto.hidePopover = function (this: HTMLElement) { /* no-op */ }
    try {
      const host = document.createElement('div')
      host.style.transform = 'translateX(40px)'
      host.style.overflow = 'hidden'
      document.body.appendChild(host)
      const el = document.createElement('gable-date-field') as GableDateField
      el.value = '2026-10-14'
      host.appendChild(el)
      await update(el, {})
      await openCalendar(el)
      const pop = q<HTMLElement>(el, '[role="dialog"]')
      expect(pop.getAttribute('popover')).toBe('manual')
      expect(shown).toContain(pop)
      // Coordinates still come from the field's own box on the screen.
      expect(pop.style.position).toBe('fixed')
      expect(pop.style.top).toBe('4px')
      await key(el, 'Escape')
      expect(el.querySelector('[role="dialog"]')).toBeNull()
      host.remove()
    } finally {
      delete proto.showPopover
      delete proto.hidePopover
    }
  })
})

describe('the popover defaults are cleared', () => {
  it('inset and margin are cleared before top and left', async () => {
    // The browser gives every popover inset: 0 and margin: auto, which would
    // centre the calendar in the space below and right of its field. jsdom has
    // no popover stylesheet, so the leg reads the inline styles: every edge is
    // written (the unused two auto), the margin cleared, ahead of nothing.
    const el = await mount<GableDateField>(TAG, { value: '2026-10-14' })
    await openCalendar(el)
    const pop = q<HTMLElement>(el, '[role="dialog"]')
    expect(pop.style.margin).toBe('0px')
    expect(pop.style.right).toBe('auto')
    expect(pop.style.bottom).toBe('auto')
    expect(pop.style.top).toBe('4px')
    expect(pop.style.left).toBe('8px')
  })
})
