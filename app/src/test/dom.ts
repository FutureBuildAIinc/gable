// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Minimal helpers for mounting `gable-*` custom elements in jsdom.
 * Every gable component overrides `createRenderRoot()` to render into the light
 * DOM, so assertions can query the element directly.
 */
import type { LitElement } from 'lit'

/**
 * Drain Lit's update queue. `updateComplete` resolves `false` when the render
 * it awaited scheduled another one (e.g. a component whose `updated()` hook
 * assigns state), so a single await is not enough to reach a settled DOM.
 */
async function settle(el: LitElement): Promise<void> {
  let guard = 0
  while (!(await el.updateComplete)) {
    if (++guard > 10) throw new Error('component never settled after 10 update cycles')
  }
}

/** Create `tag`, assign `props`, attach to the document, render to completion. */
export async function mount<T extends LitElement>(
  tag: string,
  props: Partial<T> = {},
): Promise<T> {
  const el = document.createElement(tag) as T
  Object.assign(el, props)
  document.body.appendChild(el)
  await settle(el)
  return el
}

/** Assign more props to a mounted element and wait for the re-render. */
export async function update<T extends LitElement>(el: T, props: Partial<T>): Promise<T> {
  Object.assign(el, props)
  await settle(el)
  return el
}

/** Rendered text with runs of whitespace collapsed, so assertions ignore markup indentation. */
export function text(el: Element | null): string {
  return (el?.textContent ?? '').replace(/\s+/g, ' ').trim()
}

/** Query one element, failing loudly instead of returning null. */
export function q<E extends Element>(root: ParentNode, selector: string): E {
  const found = root.querySelector<E>(selector)
  if (!found) throw new Error(`no element matched ${selector}`)
  return found
}
