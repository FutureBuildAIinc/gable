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

/**
 * Let pending microtasks *and* one macrotask turn settle. `updateComplete`
 * only drains Lit's own queue, so a component that awaits a fetch (or a lazy
 * `import()`) in `connectedCallback` needs this before its data-driven render
 * is observable.
 */
export function flush(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0))
}

/** Mount, then settle again after in-flight async work started on connect. */
export async function mountAsync<T extends LitElement>(
  tag: string,
  props: Partial<T> = {},
): Promise<T> {
  const el = await mount<T>(tag, props)
  await flush()
  await update(el, {})
  return el
}

/** Click the first element matching `selector` whose collapsed text contains `label`. */
export async function clickByText<T extends LitElement>(
  host: T,
  selector: string,
  label: string,
): Promise<void> {
  const target = Array.from(host.querySelectorAll(selector)).find((el) =>
    text(el).includes(label),
  ) as HTMLElement | undefined
  if (!target) {
    throw new Error(
      `no ${selector} matching "${label}" — saw: ${JSON.stringify(
        Array.from(host.querySelectorAll(selector)).map((el) => text(el)),
      )}`,
    )
  }
  target.click()
  await flush()
  await update(host, {})
}

/** Build a JSON `Response`, mirroring what the Go handlers return. */
export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}
