// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, type TemplateResult } from 'lit'
import { customElement, property } from 'lit/decorators.js'
import type { PortalMe } from '../types.ts'
import './quote-builder.ts'

@customElement('gable-pro-quotes')
export class GableProQuotes extends LitElement {
  createRenderRoot() { return this }

  @property({ attribute: false }) me!: PortalMe
  @property({ type: Boolean }) offline = false
  @property({ type: String }) quoteId: string | null = null

  render(): TemplateResult {
    return html`<gable-pro-quote-builder .me=${this.me} .offline=${this.offline} .quoteId=${this.quoteId}></gable-pro-quote-builder>`
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pro-quotes': GableProQuotes
  }
}
