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

/**
 * Choose a value on a mounted `gable-select` and fire its change event, the
 * way a pick from its list reaches a page's handler. Prefer driving the real
 * list (button click, then an option click) when the list itself is under
 * test; this helper is for pages whose own state is what matters.
 */
export async function chooseOption(el: HTMLElement & { value: string }, value: string): Promise<void> {
  el.value = value
  el.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
  await flush()
}

/** The app's dialog currently open over the page, or null. */
export function openAppDialog(): (LitElement & { updateComplete: Promise<boolean> }) | null {
  return document.body.querySelector('gable-app-dialog')
}

/**
 * Answer the open app dialog: `true` clicks its confirm, `false` its cancel.
 * Fails loudly when no dialog is open, so a page that stopped asking breaks
 * the leg that expected the question.
 */
export async function answerAppDialog(accept: boolean): Promise<void> {
  const dialog = openAppDialog()
  if (!dialog) throw new Error('no gable-app-dialog is open')
  await dialog.updateComplete
  q<HTMLButtonElement>(dialog, accept ? '[data-dialog-confirm]' : '[data-dialog-cancel]').click()
  await flush()
}

/** Type into the open app dialog's prompt field. */
export async function typeIntoAppDialog(value: string): Promise<void> {
  const dialog = openAppDialog()
  if (!dialog) throw new Error('no gable-app-dialog is open')
  await dialog.updateComplete
  const input = q<HTMLInputElement>(dialog, 'input')
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await dialog.updateComplete
}
