// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { SaleLine, ScaleReading, ManualWeightReason } from '../../types/pos';

export interface ManualWeightPayload {
  weight_lb: string;
  tare_lb?: string;
  reason: string;
  note?: string;
  revision: number;
}

/** A line can be weighed when it is a top level product that has not been weighed yet. */
export function canWeighLine(line: SaleLine): boolean {
  return line.line_type === 'product' && line.parent_line_id === null && line.weight_source === null;
}

/** A weighed line shows its net and ticket number; manual entries are flagged. */
export function weightBadgeText(line: SaleLine): string | null {
  if (!line.weight_source || line.weight_net_lb === null) return null;
  const prefix = line.weight_source === 'manual' ? 'MAN WT ' : '';
  const net = `${formatLb(line.weight_net_lb)} lb`;
  const ticket = line.scale_ticket_number ? ` ${line.scale_ticket_number}` : '';
  return `${prefix}NET ${net}${ticket}`;
}

/** Format a decimal pound value with thousands separators, trimming trailing zeros. */
export function formatLb(lb: string | number | null | undefined): string {
  if (lb === null || lb === undefined || lb === '') return '0';
  const n = typeof lb === 'string' ? Number(lb) : lb;
  if (Number.isNaN(n)) return String(lb);
  return n.toLocaleString('en-US', { maximumFractionDigits: 4 });
}

/** Plain words for the current scale reading. */
export function scaleReadingText(reading: ScaleReading | null, configured: boolean, detail?: string): string {
  if (!configured) return 'No scale is configured for this register';
  if (detail) return detail;
  if (!reading) return 'Scale reading not available';
  if (reading.weight_lb === null) return 'Over capacity';
  if (reading.motion) return 'In motion';
  return `${formatLb(reading.weight_lb)} ${reading.unit}`;
}

/**
 * Build the manual weight payload, refusing client side when a configured scale
 * is not at zero (Handbook 44) or the fields are invalid.
 */
export function buildManualWeightPayload(
  weightLb: string,
  tareLb: string,
  reason: ManualWeightReason | null,
  note: string,
  revision: number,
  reading: ScaleReading | null,
  configured: boolean,
): { ok: true; payload: ManualWeightPayload } | { ok: false; error: string } {
  if (configured && reading && !reading.at_zero) {
    return { ok: false, error: 'The scale is not at zero. Clear it before a manual weight.' };
  }
  if (!reason) {
    return { ok: false, error: 'Pick a reason for the manual weight.' };
  }
  const weight = Number(weightLb);
  if (Number.isNaN(weight) || weight <= 0) {
    return { ok: false, error: 'Enter a weight above zero.' };
  }
  const tareNum = tareLb.trim() === '' ? 0 : Number(tareLb);
  if (Number.isNaN(tareNum) || tareNum < 0) {
    return { ok: false, error: 'Enter a tare of zero or more.' };
  }
  if (reason.code === 'other' && note.trim() === '') {
    return { ok: false, error: 'Add a note when the reason is other.' };
  }
  const payload: ManualWeightPayload = {
    weight_lb: weightLb.trim(),
    revision,
    reason: reason.code,
  };
  if (tareNum > 0) payload.tare_lb = tareLb.trim();
  if (note.trim()) payload.note = note.trim();
  return { ok: true, payload };
}
