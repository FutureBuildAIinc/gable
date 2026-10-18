// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { html, type TemplateResult } from 'lit'
import { statusLabel } from './format.ts'

export type StatusTone = 'neutral' | 'green' | 'blue' | 'red' | 'amber'

export function toneForStatus(status: string): StatusTone {
  const value = status.toUpperCase()
  if (['PAID', 'FULFILLED', 'APPROVED', 'ACCEPTED', 'DELIVERED', 'PICKED_UP', 'CONFIRMED'].includes(value)) return 'green'
  if (['CANCELLED', 'DECLINED', 'REJECTED', 'EXPIRED', 'WRITTEN_OFF'].includes(value)) return 'red'
  if (['OUT_FOR_DELIVERY', 'READY_FOR_PICKUP', 'LOADED', 'PREPARING', 'SENT', 'OPEN'].includes(value)) return 'blue'
  if (['REQUESTED', 'PRICED', 'UNPAID', 'PARTIAL', 'OVERDUE'].includes(value)) return 'amber'
  return 'neutral'
}

export function statusChip(status: string, tone = toneForStatus(status)): TemplateResult {
  const toneClass = tone === 'neutral' ? '' : `pro-chip-${tone}`
  return html`<span class="pro-chip ${toneClass}" data-status=${status}>${statusLabel(status)}</span>`
}
