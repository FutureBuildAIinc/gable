// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Icon helper — renders lucide icons as SVG strings for use in Lit templates.
 * MOVED from web/apps/desk/src/lib/icons.ts when web/ became one workspace;
 * the desk re-exports it there, and the front door's tiles use this copy.
 * Usage:
 *   import { icon } from '@gable/design-system';
 *   import { Package } from 'lucide';
 *   html`${icon(Package)}`
 */
import { html } from 'lit';
import { unsafeHTML } from 'lit/directives/unsafe-html.js';
import { createElement, LayoutGrid, Receipt, Store, Warehouse, Truck } from 'lucide';

/**
 * Render a lucide icon into a Lit template.
 * @param iconData  The icon import from 'lucide' (e.g. `Package`, `Truck`)
 * @param size      Pixel size (default 20)
 * @param cls       Extra CSS classes
 *
 * Returns a TemplateResult so the value is safe to pass through property
 * bindings (e.g. `.iconHtml=${icon(...)}`) — `unsafeHTML()` alone may only
 * be used in child bindings.
 */
export function icon(
  iconData: Parameters<typeof createElement>[0],
  size: number = 20,
  cls: string = ''
) {
  const el = createElement(iconData);
  el.setAttribute('width', String(size));
  el.setAttribute('height', String(size));
  if (cls) el.setAttribute('class', cls);
  return html`${unsafeHTML(el.outerHTML)}`;
}

/**
 * The named icons the shell chrome draws for the catalog's apps (the catalog
 * carries each app's icon id). An unknown id answers the grid, the desk's
 * own, so a new app id never renders blank.
 */
const APP_ICONS = {
  grid: LayoutGrid,
  receipt: Receipt,
  storefront: Store,
  warehouse: Warehouse,
  truck: Truck,
} as const;

export type AppIconId = keyof typeof APP_ICONS;

export function appIcon(id: string): Parameters<typeof createElement>[0] {
  return APP_ICONS[id as AppIconId] ?? LayoutGrid;
}

/**
 * The two provider marks a sign in button carries, drawn here by hand in the
 * helper's own idiom (a 24 box, currentColor, the stroke width and caps
 * lucide uses) rather than brought in as files: monochrome on purpose, so
 * each follows the theme exactly like every other icon on a card.
 */

/** Google's mark: the ring and crossbar of a G, in the icon stroke style. */
export function googleGlyph(size: number = 20, cls: string = '') {
  return glyph(size, cls, '<path d="M19.79 7.5A9 9 0 1 0 21 12h-9"/>');
}

/**
 * Microsoft's mark: four separate filled panes of the window with a narrow gap,
 * monochrome by currentColor so it reads as the Microsoft mark at button size
 * rather than the stroked apps grid outline.
 */
export function microsoftGlyph(size: number = 20, cls: string = '') {
  const classAttr = cls ? ` class="${cls}"` : '';
  const body =
    '<rect x="2" y="2" width="9" height="9" rx="0.5"/>' +
    '<rect x="13" y="2" width="9" height="9" rx="0.5"/>' +
    '<rect x="2" y="13" width="9" height="9" rx="0.5"/>' +
    '<rect x="13" y="13" width="9" height="9" rx="0.5"/>';
  return html`${unsafeHTML(
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="${size}" height="${size}"${classAttr} ` +
    `fill="currentColor" stroke="none" aria-hidden="true">${body}</svg>`
  )}`;
}

function glyph(size: number, cls: string, body: string) {
  const classAttr = cls ? ` class="${cls}"` : '';
  return html`${unsafeHTML(
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="${size}" height="${size}"${classAttr} ` +
    `fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`
  )}`;
}
