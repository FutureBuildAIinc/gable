// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect, vi } from 'vitest'
import { mount, update, q } from '../../test/dom.ts'
import type { GableSelect, GableSelectOption } from './gable-select.ts'
import './gable-select.ts'

const FRUITS: GableSelectOption[] = [
  { value: 'apple', label: 'Apple' },
  { value: 'pear', label: 'Pear' },
  { value: 'plum', label: 'Plum' },
]

const GROUPED: GableSelectOption[] = [
  { value: 'kel-a', label: 'KEL-A', group: 'Kelowna' },
  { value: 'kel-b', label: 'KEL-B', group: 'Kelowna' },
  { value: 'van-a', label: 'VAN-A', group: 'Vancouver', disabled: true },
]

async function mountSelect(props: Partial<GableSelect> = {}): Promise<GableSelect> {
  return mount<GableSelect>('gable-select', { options: FRUITS, ...props })
}

function buttonOf(el: GableSelect): HTMLButtonElement {
  return q<HTMLButtonElement>(el, 'button[aria-haspopup="listbox"]')
}

async function open(el: GableSelect) {
  buttonOf(el).click()
  await update(el, {})
}

function keydown(el: GableSelect, key: string) {
  buttonOf(el).dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
}

function options(el: GableSelect): HTMLElement[] {
  return Array.from(el.querySelectorAll<HTMLElement>('[role="option"]'))
}

describe('gable-select', () => {
  it('shows the chosen option label, or the placeholder when nothing is chosen', async () => {
    const el = await mountSelect({ placeholder: 'Pick a bin' })
    expect(buttonOf(el).textContent).toContain('Pick a bin')
    await update(el, { value: 'pear' })
    expect(buttonOf(el).textContent).toContain('Pear')
    expect(buttonOf(el).textContent).not.toContain('Pick a bin')
  })

  it('exposes listbox semantics: aria-haspopup, aria-expanded, role listbox and option', async () => {
    const el = await mountSelect({ label: 'Fruit' })
    expect(buttonOf(el).getAttribute('aria-expanded')).toBe('false')
    await open(el)
    expect(buttonOf(el).getAttribute('aria-expanded')).toBe('true')
    const list = q<HTMLElement>(el, '[role="listbox"]')
    expect(list.getAttribute('aria-label')).toBe('Fruit')
    expect(options(el).length).toBe(3)
    expect(options(el)[0].getAttribute('aria-selected')).toBe('false')
  })

  it('chooses on click and fires change with the value on the element', async () => {
    const el = await mountSelect()
    const onChange = vi.fn((e: Event) => (e.target as GableSelect).value)
    el.addEventListener('change', onChange)
    await open(el)
    options(el)[1].click()
    await update(el, {})
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange.mock.results[0].value).toBe('pear')
    expect(el.value).toBe('pear')
    expect(el.querySelector('[role="listbox"]')).toBeNull()
  })

  it('drives the list with Up, Down, Home and End; Enter chooses; Space chooses', async () => {
    const el = await mountSelect()
    await open(el)
    keydown(el, 'ArrowDown')
    await update(el, {})
    expect(el.querySelector('[role="listbox"]')!.getAttribute('aria-activedescendant')).toContain('-opt-1')
    keydown(el, 'End')
    await update(el, {})
    expect(el.querySelector('[role="listbox"]')!.getAttribute('aria-activedescendant')).toContain('-opt-2')
    keydown(el, 'Home')
    await update(el, {})
    keydown(el, 'Enter')
    await update(el, {})
    expect(el.value).toBe('apple')

    await open(el)
    keydown(el, 'ArrowDown')
    await update(el, {})
    keydown(el, ' ')
    await update(el, {})
    expect(el.value).toBe('pear')
  })

  it('Escape closes without choosing and returns focus to the button', async () => {
    const el = await mountSelect()
    const onChange = vi.fn()
    el.addEventListener('change', onChange)
    await open(el)
    keydown(el, 'ArrowDown')
    keydown(el, 'Escape')
    await update(el, {})
    expect(el.querySelector('[role="listbox"]')).toBeNull()
    expect(el.value).toBe('')
    expect(onChange).not.toHaveBeenCalled()
    expect(document.activeElement).toBe(buttonOf(el))
  })

  it('Tab closes and moves on', async () => {
    const el = await mountSelect()
    await open(el)
    keydown(el, 'Tab')
    await update(el, {})
    expect(el.querySelector('[role="listbox"]')).toBeNull()
    expect(el.value).toBe('')
  })

  it('typeahead on the first letters moves the active option', async () => {
    const el = await mountSelect()
    await open(el)
    keydown(el, 'p')
    await update(el, {})
    expect(el.querySelector('[role="listbox"]')!.getAttribute('aria-activedescendant')).toContain('-opt-1')
    keydown(el, 'l')
    await update(el, {})
    expect(el.querySelector('[role="listbox"]')!.getAttribute('aria-activedescendant')).toContain('-opt-2')
    keydown(el, 'Enter')
    await update(el, {})
    expect(el.value).toBe('plum')
  })

  it('never chooses a disabled option and skips it in keyboard movement', async () => {
    const el = await mountSelect({ options: GROUPED })
    const onChange = vi.fn()
    el.addEventListener('change', onChange)
    await open(el)
    const all = options(el)
    expect(all[2].getAttribute('aria-disabled')).toBe('true')
    all[2].click()
    await update(el, {})
    expect(onChange).not.toHaveBeenCalled()
    expect(el.querySelector('[role="listbox"]')).not.toBeNull()
    keydown(el, 'ArrowDown')
    keydown(el, 'ArrowDown')
    await update(el, {})
    // From KEL-A Down lands on KEL-B, then skips the disabled VAN-A and wraps to KEL-A.
    expect(el.querySelector('[role="listbox"]')!.getAttribute('aria-activedescendant')).toContain('-opt-0')
  })

  it('draws a heading row for each group', async () => {
    const el = await mountSelect({ options: GROUPED })
    await open(el)
    const headings = Array.from(el.querySelectorAll('[role="presentation"]')).map((h) => h.textContent?.trim())
    expect(headings).toEqual(['Kelowna', 'Vancouver'])
  })

  it('closes when the click lands outside', async () => {
    const el = await mountSelect()
    await open(el)
    document.body.click()
    await update(el, {})
    expect(el.querySelector('[role="listbox"]')).toBeNull()
  })

  it('a disabled select never opens', async () => {
    const el = await mountSelect({ disabled: true })
    buttonOf(el).click()
    await update(el, {})
    keydown(el, 'ArrowDown')
    await update(el, {})
    expect(el.querySelector('[role="listbox"]')).toBeNull()
  })

  it('a page reads the chosen value from its own state through the change event', async () => {
    const el = await mountSelect()
    let pageState = ''
    el.addEventListener('change', (e) => { pageState = (e.target as GableSelect).value })
    await open(el)
    keydown(el, 'ArrowDown')
    keydown(el, 'ArrowDown')
    keydown(el, 'Enter')
    await update(el, {})
    expect(pageState).toBe('plum')
  })
})
