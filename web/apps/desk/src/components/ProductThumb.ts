// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';

export type ProductThumbSize = 24 | 40 | 72;

/** True for a path the desk may draw: one leading slash, never a scheme or a protocol relative URL. */
export function isDrawablePath(src: string | null | undefined): src is string {
  return !!src && src.startsWith('/') && !src.startsWith('//');
}

/**
 * A product's picture at 24, 40 or 72 pixels, drawn as an <img> from the path the reads answer
 * (`image_url`: null while the catalog.product_images setting is off or the product has none).
 * With no path it draws nothing, or, when `reserve` is set, the neutral mark on a plate of its size (a screen whose
 * other rows carry a picture keeps its names in line). A picture that fails to load draws the same
 * neutral stack of boards the counter's catalogue card draws.
 */
@customElement('gable-product-thumb')
export class GableProductThumb extends LitElement {
  createRenderRoot() { return this; }

  @property({ attribute: 'src' }) src: string | null | undefined = null;
  @property({ type: Number }) size: ProductThumbSize = 40;
  @property({ type: Boolean }) reserve = false;

  @state() private _broken = false;

  willUpdate(changed: Map<string, unknown>) {
    if (changed.has('src')) this._broken = false;
  }

  render() {
    const px = `${this.size}px`;
    // Only a same origin path is drawn (one leading slash, never a scheme or a protocol relative URL).
    const drawable = isDrawablePath(this.src);
    // Without a picture it draws nothing, or (reserve) the neutral mark on the same plate, so the
    // same product looks the same here as on the counter and the column keeps its alignment.
    if (!drawable && !this.reserve) return nothing;
    const mark = Math.round(this.size * 0.6);
    return html`<span data-testid="product-thumb"
        class="inline-flex shrink-0 items-center justify-center overflow-hidden rounded-md border border-white/10 bg-deep-space align-middle"
        style="width:${px};height:${px}">
      ${this._broken || !drawable
        ? html`<svg data-testid="product-thumb-none" viewBox="0 0 24 24" fill="none" aria-label="No picture"
              class="text-slate-400 opacity-70" style="width:${mark}px;height:${mark}px">
            <rect x="2" y="14" width="20" height="5" rx="1" stroke="currentColor" stroke-width="1.5" />
            <rect x="4" y="9" width="16" height="5" rx="1" stroke="currentColor" stroke-width="1.5" />
            <rect x="7" y="4" width="10" height="5" rx="1" stroke="currentColor" stroke-width="1.5" />
          </svg>`
        : html`<img src=${this.src as string} alt="" loading="lazy" class="h-full w-full object-cover"
              @error=${() => { this._broken = true; }} />`}
    </span>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-product-thumb': GableProductThumb;
  }
}
