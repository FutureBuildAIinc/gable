// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import {
  canWeighLine,
  weightBadgeText,
  formatLb,
  scaleReadingText,
  buildManualWeightPayload,
} from './scale';
import type { SaleLine, ScaleReading } from '../../types/pos';

function line(over: Partial<SaleLine> = {}): SaleLine {
  return {
    id: 'l1',
    position: 1,
    line_type: 'product',
    parent_line_id: null,
    product_id: 'p1',
    charge_code_id: null,
    charge_code: null,
    sku: 'SKU-1',
    description: 'Gravel',
    quantity: '1',
    uom: 'TON',
    price_uom: 'TON',
    uom_qty: '1',
    price_uom_qty: '1',
    tint: null,
    unit_price_ten_thousandths: 300000,
    priced_unit_price_ten_thousandths: 300000,
    price_source: 'price_list',
    override_reason: null,
    discount_percent: null,
    discount_cents: null,
    discount_reason: null,
    price_adjusted_by: null,
    line_total_cents: 3000,
    taxable: true,
    revenue_account_code: null,
    is_special_order: false,
    vendor_id: null,
    special_order_unit_cost_ten_thousandths: null,
    weight_source: null,
    weight_gross_lb: null,
    weight_tare_lb: null,
    weight_net_lb: null,
    weight_net_basis: null,
    scale_device_id: null,
    scale_ticket_id: null,
    scale_ticket_number: null,
    manual_weight_reason: null,
    weight_captured_by: null,
    weight_captured_at: null,
    created_at: '',
    ...over,
  };
}

const reading = (over: Partial<ScaleReading> = {}): ScaleReading => ({
  weight_lb: '10000',
  unit: 'lb',
  mode: 'gross',
  motion: false,
  at_zero: false,
  ...over,
});

describe('scale helpers', () => {
  it('says a product line with no parent and no weight can be weighed', () => {
    expect(canWeighLine(line())).toBe(true);
  });

  it('refuses a kit, a child line, and an already weighed line', () => {
    expect(canWeighLine(line({ line_type: 'kit' }))).toBe(false);
    expect(canWeighLine(line({ parent_line_id: 'parent-1' }))).toBe(false);
    expect(canWeighLine(line({ weight_source: 'scale' }))).toBe(false);
  });

  it('formats pounds with thousands separators', () => {
    expect(formatLb('12400')).toBe('12,400');
    expect(formatLb('12400.5000')).toBe('12,400.5');
    expect(formatLb(null)).toBe('0');
  });

  it('builds the weight badge for a scaled line', () => {
    const weighed = line({
      weight_source: 'scale',
      weight_net_lb: '12400',
      scale_ticket_number: 'WT-000012',
    });
    expect(weightBadgeText(weighed)).toBe('NET 12,400 lb WT-000012');
  });

  it('prefixes the manual flag on a manual line', () => {
    const weighed = line({
      weight_source: 'manual',
      weight_net_lb: '5000',
      scale_ticket_number: 'WT-000003',
    });
    expect(weightBadgeText(weighed)).toBe('MAN WT NET 5,000 lb WT-000003');
  });

  it('returns null when the line has not been weighed', () => {
    expect(weightBadgeText(line())).toBeNull();
  });

  it('describes a configured scale reading', () => {
    expect(scaleReadingText(reading({ weight_lb: '32000' }), true)).toBe('32,000 lb');
    expect(scaleReadingText(reading({ motion: true }), true)).toBe('In motion');
    expect(scaleReadingText(reading({ weight_lb: null }), true)).toBe('Over capacity');
  });

  it('describes an unconfigured scale', () => {
    expect(scaleReadingText(null, false)).toBe('No scale is configured for this register');
  });

  it('builds a manual payload with optional tare and note', () => {
    const result = buildManualWeightPayload(
      '15000',
      '2000',
      { code: 'scale_offline', label: 'Scale offline', needs_note: false },
      '',
      3,
      reading({ at_zero: true }),
      true,
    );
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.payload).toEqual({
      weight_lb: '15000',
      tare_lb: '2000',
      reason: 'scale_offline',
      revision: 3,
    });
  });

  it('refuses a manual weight when the scale is not at zero', () => {
    const result = buildManualWeightPayload(
      '15000',
      '',
      { code: 'scale_offline', label: 'Scale offline', needs_note: false },
      '',
      3,
      reading(),
      true,
    );
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.error).toContain('not at zero');
  });

  it('does not refuse a manual weight when no scale is configured', () => {
    const result = buildManualWeightPayload(
      '15000',
      '',
      { code: 'scale_offline', label: 'Scale offline', needs_note: false },
      '',
      3,
      null,
      false,
    );
    expect(result.ok).toBe(true);
  });

  it('requires a note for the other reason', () => {
    const result = buildManualWeightPayload(
      '15000',
      '',
      { code: 'other', label: 'Other', needs_note: true },
      '',
      3,
      reading({ at_zero: true }),
      true,
    );
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.error).toContain('note');
  });

  it('rejects a non positive weight', () => {
    const result = buildManualWeightPayload(
      '0',
      '',
      { code: 'scale_offline', label: 'Scale offline', needs_note: false },
      '',
      3,
      reading({ at_zero: true }),
      true,
    );
    expect(result.ok).toBe(false);
  });
});
