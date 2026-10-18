// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, type TemplateResult } from 'lit'
import { customElement, property } from 'lit/decorators.js'
import type { PortalMe } from '../types.ts'

@customElement('gable-pro-today')
export class GableProToday extends LitElement {
  createRenderRoot() { return this }

  @property({ attribute: false }) me!: PortalMe
  @property({ type: Boolean }) offline = false

  render(): TemplateResult {
    return html`<section data-testid="today-screen">
      <p class="pro-page-kicker">Today · ${this.me.account.name}</p>
      <h1 class="pro-page-title">Good to see you, ${this.me.user.name}</h1>
      <p class="pro-page-subtitle">Your jobs, deliveries, quotes, and invoices in one place.</p>
      <div class="pro-empty mt-5">
        <p class="pro-card-title">Your Today board is opening.</p>
        <p class="pro-text-small">The latest account activity will appear here.</p>
      </div>
    </section>`
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pro-today': GableProToday
  }
}
