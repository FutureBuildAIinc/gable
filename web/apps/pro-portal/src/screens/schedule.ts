// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, type TemplateResult } from 'lit'
import { customElement, property } from 'lit/decorators.js'
import type { PortalMe } from '../types.ts'
import './schedule-board.ts'
import './order-detail.ts'

@customElement('gable-pro-schedule')
export class GableProSchedule extends LitElement {
  createRenderRoot() { return this }

  @property({ attribute: false }) me!: PortalMe
  @property({ type: Boolean }) offline = false
  @property({ type: String }) orderId: string | null = null

  render(): TemplateResult {
    if (this.orderId !== null && this.orderId !== '') {
      return html`<gable-pro-order-detail .me=${this.me} .offline=${this.offline} .orderId=${this.orderId}></gable-pro-order-detail>`
    }
    return html`<gable-pro-schedule-board .me=${this.me} .offline=${this.offline}></gable-pro-schedule-board>`
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pro-schedule': GableProSchedule
  }
}
