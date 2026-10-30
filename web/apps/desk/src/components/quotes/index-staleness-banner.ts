// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ExposureService } from '../../services/ExposureService';
import type { MarketIndexStaleness } from '../../types/exposure';
import { Clock } from 'lucide';

/**
 * The market index staleness banner (QE-07-b1).
 *
 * A deployment says how old a market index value may get before the desk is
 * told. When an active index's newest value is older than that, the numbers
 * on the screen are older than the dealer agreed they may be, and the banner
 * says so. It self-hides when nothing is stale, and when the answer cannot
 * be read: a notice that cannot be fetched is not a notice about staleness.
 *
 * The host bumps `refreshToken` when it changes an index value, so the
 * banner reads the answer again rather than showing what it knew before the
 * change.
 */
@customElement('gable-index-staleness-banner')
export class GableIndexStalenessBanner extends LitElement {
  createRenderRoot() { return this; }

  /** Bumped by the host when the index values change under the banner. */
  @property({ attribute: false }) refreshToken = 0;

  @state() private staleness: MarketIndexStaleness | null = null;

  /** The token the answer now on screen was read at. */
  private _readAt = -1;

  connectedCallback() {
    super.connectedCallback();
    void this._load();
  }

  updated(changed: Map<string, unknown>) {
    if (changed.has('refreshToken') && this.refreshToken !== this._readAt) {
      void this._load();
    }
  }

  private async _load() {
    this._readAt = this.refreshToken;
    try {
      this.staleness = await ExposureService.getIndexStaleness();
    } catch {
      // The answer could not be read: show nothing rather than claim the
      // values are fresh when nobody knows.
      this.staleness = null;
    }
  }

  render() {
    const s = this.staleness;
    if (!s || !s.stale || s.stale_indices.length === 0) return nothing;

    const names = s.stale_indices.map((i) => i.name || i.index_code);
    const oldest = s.stale_indices.reduce((a, i) => (i.days_old > a ? i.days_old : a), 0);
    return html`
      <div class="rounded-xl border border-amber-500/30 bg-amber-500/10 p-4" data-testid="index-staleness-banner">
        <div class="flex items-start gap-3">
          ${icon(Clock, 20, 'w-5 h-5 text-amber-400 shrink-0 mt-0.5')}
          <div>
            <h3 class="text-sm font-semibold text-white uppercase tracking-wider">
              Market index values are stale
            </h3>
            <p class="text-xs text-amber-400 mt-1" data-testid="staleness-message">
              ${names.join(', ')} ${names.length === 1 ? 'is' : 'are'} older than the ${s.threshold_days} day
              threshold${oldest > 0 ? ` (the oldest is ${oldest} days old)` : ''}. The prices below use those older
              values.
            </p>
          </div>
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-index-staleness-banner': GableIndexStalenessBanner;
  }
}
