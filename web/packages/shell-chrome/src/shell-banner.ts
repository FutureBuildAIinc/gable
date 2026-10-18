// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The shell's banner: the strip under the header band that says the truth
// about the connection while the person keeps working. It renders nothing at
// all while the device is online; a chrome that says nothing is the point of
// the states. Gable's apps are online only, so the words never claim that
// writes wait or send later.
//
// Purely presentational: the host maps the platform's network state onto
// `mode`.

import { LitElement, html } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import { icon } from '@gable/design-system';
import { WifiOff } from 'lucide';
import { SHELL_BANNER_MODES, bannerWords, type ShellBannerMode } from './shell-state.ts';

@customElement('gable-shell-banner')
export class GableShellBanner extends LitElement {
  createRenderRoot() {
    return this;
  }

  /** The connection's truth: 'offline', or '' for nothing to say. */
  @property({ attribute: 'mode' }) mode: ShellBannerMode | string = '';

  render() {
    if (this.mode === '') return html``;
    if (!(SHELL_BANNER_MODES as readonly string[]).includes(this.mode)) {
      // An unknown mode is a defect at the call site: refuse loudly, never
      // paint a quietly wrong state.
      throw new Error(
        `gable-shell-banner was given the unknown mode ${JSON.stringify(this.mode)}; ` +
          `expected '' or one of: ${SHELL_BANNER_MODES.filter((m) => m !== '').join(', ')}`,
      );
    }
    const words = bannerWords(this.mode as ShellBannerMode);
    if (words === '') return html``;
    return html`
      <div
        role="status"
        data-mode=${this.mode}
        class="flex items-center justify-center gap-2 bg-amber-500/10 border-b border-amber-500/30 text-amber-400 text-sm px-4 py-2"
      >
        ${icon(WifiOff, 14)}
        <span>${words}</span>
      </div>
    `;
  }
}
