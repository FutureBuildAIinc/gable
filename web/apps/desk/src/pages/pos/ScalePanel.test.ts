// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Sale, ScaleStatus, ScaleTicket } from '../../types/pos';
import { mount, flush, chooseOption } from '../../test/dom';

const svc = vi.hoisted(() => ({
  getScaleStatus: vi.fn(),
  listScaleTickets: vi.fn(),
  scaleManualReasons: vi.fn(),
  weighIn: vi.fn(),
  weighOut: vi.fn(),
  voidScaleTicket: vi.fn(),
  setManualWeight: vi.fn(),
}));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './ScalePanel';
import type { ScalePanel } from './ScalePanel';

const sale = (lines: Sale['lines'] = []): Sale => ({
  id: 's1',
  number: 'S-1',
  revision: 4,
  branch_id: 'b1',
  register_id: 'REG-01',
  cashier_id: 'c1',
  customer_id: null,
  currency: 'USD',
  subtotal_cents: 0,
  tax_cents: 0,
  total_cents: 0,
  change_cents: 0,
  till_session_id: 't1',
  status: 'open',
  invoice_id: null,
  completed_at: null,
  created_at: '',
  customer_po: null,
  job_id: null,
  buyer_contact_id: null,
  print_count: 0,
  exchange_return_id: null,
  lines,
  tenders: [],
});

const productLine = (id: string, desc: string, qty = '1'): Sale['lines'][0] => ({
  id,
  position: 1,
  line_type: 'product',
  parent_line_id: null,
  product_id: `p-${id}`,
  charge_code_id: null,
  charge_code: null,
  sku: `SKU-${id}`,
  description: desc,
  quantity: qty,
  uom: 'TON',
  price_uom: 'TON',
  uom_qty: qty,
  price_uom_qty: qty,
  unit_price_ten_thousandths: 300000,
  priced_unit_price_ten_thousandths: 300000,
  price_source: 'price_list',
  override_reason: null,
  discount_percent: null,
  discount_cents: null,
  discount_reason: null,
  price_adjusted_by: null,
  line_total_cents: null,
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
});

const ticket = (over: Partial<ScaleTicket> = {}): ScaleTicket => ({
  id: 'wt1',
  ticket_number: 'WT-000001',
  revision: 1,
  branch_id: 'b1',
  scale_device_id: 'sd1',
  vehicle_ref: 'TRUCK-1',
  direction: 'weigh_in',
  status: 'open',
  gross_lb: '32000',
  tare_lb: null,
  net_lb: null,
  reading_unit: 'lb',
  source: 'scale',
  manual_reason: null,
  manual_note: null,
  net_basis: null,
  weigh_in_ticket_id: null,
  sale_line_id: null,
  captured_by: 'c1',
  captured_at: '',
  void_reason: null,
  created_at: '',
  ...over,
});

const configuredStatus = () => ({
  configured: true,
  device: { id: 'sd1', name: 'Yard scale', certificate_no: 'CERT-1', location_note: 'North end' },
  reading: { weight_lb: '0', unit: 'lb' as const, mode: 'gross' as const, motion: false, at_zero: true },
});

async function mountPanel(
  s: Sale = sale(),
  opts: { status?: ScaleStatus; tickets?: ScaleTicket[] } = {},
) {
  svc.getScaleStatus.mockResolvedValue(opts.status ?? configuredStatus());
  svc.listScaleTickets.mockResolvedValue(opts.tickets ?? []);
  svc.scaleManualReasons.mockResolvedValue([
    { code: 'scale_offline' as const, label: 'Scale offline', needs_note: false },
    { code: 'scale_error' as const, label: 'Scale error', needs_note: false },
    { code: 'preweighed' as const, label: 'Preweighed', needs_note: false },
    { code: 'other' as const, label: 'Other', needs_note: true },
  ]);
  const el = await mount<ScalePanel>('gable-pos-scale-panel', { registerId: 'REG-01', sale: s });
  await flush();
  await flush();
  return el;
}

const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;
const typeInto = (input: HTMLInputElement, value: string) => {
  input.value = value;
  input.dispatchEvent(new Event('input'));
};

describe('ScalePanel', () => {
  beforeEach(() => { vi.clearAllMocks(); });

  it('shows the scale name and current reading', async () => {
    const el = await mountPanel();
    expect(q(el, 'scale-device').textContent).toContain('Yard scale');
    expect(q(el, 'scale-reading').textContent).toContain('0 lb');
    el.remove();
  });

  it('says when no scale is configured and still offers manual entry', async () => {
    const el = await mountPanel(sale(), { status: { configured: false, device: null, reading: null } });
    expect(q(el, 'scale-device').textContent).toContain('No scale configured');
    expect(q(el, 'scale-reading').textContent).toContain('No scale is configured');
    expect(q(el, 'scale-weigh-in').hasAttribute('disabled')).toBe(true);
    el.remove();
  });

  it('creates a weigh-in and refreshes the open tickets list', async () => {
    svc.weighIn.mockResolvedValue(ticket());
    svc.listScaleTickets
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([ticket()]);
    const el = await mountPanel();
    typeInto(q(el, 'scale-vehicle-in') as HTMLInputElement, 'TRUCK-1');
    await flush();
    q(el, 'scale-weigh-in').click();
    await flush();
    expect(svc.weighIn).toHaveBeenCalledWith('REG-01', 'TRUCK-1');
    expect(q(el, 'scale-ticket')).toBeTruthy();
    expect(q(el, 'scale-ticket').textContent).toContain('WT-000001');
    el.remove();
  });

  it('weighs out the chosen line and ticket and dispatches the updated sale', async () => {
    const line = productLine('l1', 'Crushed stone');
    const updated = sale([{ ...line, weight_source: 'scale', weight_net_lb: '12400', scale_ticket_number: 'WT-000002' } as Sale['lines'][0]]);
    svc.weighOut.mockResolvedValue({ sale: updated, ticket: ticket({ ticket_number: 'WT-000002', direction: 'weigh_out' }) });
    const el = await mountPanel(sale([line]), { tickets: [ticket()] });
    await chooseOption(q(el, 'scale-line-out') as HTMLElement & { value: string }, 'l1');
    await chooseOption(q(el, 'scale-ticket-out') as HTMLElement & { value: string }, 'wt1');
    let captured: Sale | null = null;
    el.addEventListener('scale-sale', (e) => { captured = (e as CustomEvent<Sale>).detail; });
    q(el, 'scale-weigh-out').click();
    await flush();
    expect(svc.weighOut).toHaveBeenCalledWith('s1', 'l1', { weigh_in_ticket_id: 'wt1', revision: 4 });
    expect(captured).toBe(updated);
    el.remove();
  });

  it('voids an open ticket after asking for a reason', async () => {
    svc.voidScaleTicket.mockResolvedValue(ticket({ status: 'void' }));
    const el = await mountPanel(sale(), { tickets: [ticket()] });
    expect(q(el, 'scale-ticket')).toBeTruthy();
    q(el, 'scale-void-ticket').click();
    await flush();
    const dialog = document.body.querySelector('gable-app-dialog');
    expect(dialog).toBeTruthy();
    const input = dialog!.querySelector('input') as HTMLInputElement;
    typeInto(input, 'Wrong truck');
    await flush();
    const confirm = dialog!.querySelector('[data-dialog-confirm]') as HTMLButtonElement;
    confirm.click();
    await flush();
    expect(svc.voidScaleTicket).toHaveBeenCalledWith('wt1', 'Wrong truck', 1);
    el.remove();
  });

  it('sets a manual weight and dispatches the updated sale', async () => {
    const line = productLine('l1', 'Crushed stone');
    const updated = sale([{ ...line, weight_source: 'manual', weight_net_lb: '5000', scale_ticket_number: 'WT-000003' } as Sale['lines'][0]]);
    svc.setManualWeight.mockResolvedValue(updated);
    const el = await mountPanel(sale([line]));
    await chooseOption(q(el, 'scale-line-manual') as HTMLElement & { value: string }, 'l1');
    typeInto(q(el, 'scale-weight-manual') as HTMLInputElement, '5000');
    await chooseOption(q(el, 'scale-reason') as HTMLElement & { value: string }, 'scale_offline');
    let captured: Sale | null = null;
    el.addEventListener('scale-sale', (e) => { captured = (e as CustomEvent<Sale>).detail; });
    q(el, 'scale-manual-submit').click();
    await flush();
    expect(svc.setManualWeight).toHaveBeenCalledWith('s1', 'l1', { weight_lb: '5000', reason: 'scale_offline', revision: 4 });
    expect(captured).toBe(updated);
    el.remove();
  });

  it('refuses a manual weight client side when the scale is not at zero', async () => {
    const line = productLine('l1', 'Crushed stone');
    const el = await mountPanel(sale([line]), {
      status: {
        configured: true,
        device: { id: 'sd1', name: 'Yard scale', certificate_no: 'CERT-1', location_note: 'North end' },
        reading: { weight_lb: '1200', unit: 'lb', mode: 'gross', motion: false, at_zero: false },
      },
    });
    await chooseOption(q(el, 'scale-line-manual') as HTMLElement & { value: string }, 'l1');
    typeInto(q(el, 'scale-weight-manual') as HTMLInputElement, '5000');
    await chooseOption(q(el, 'scale-reason') as HTMLElement & { value: string }, 'scale_offline');
    let message: string | null = null;
    el.addEventListener('scale-error', (e) => { message = (e as CustomEvent<string>).detail; });
    q(el, 'scale-manual-submit').click();
    await flush();
    expect(message).toContain('not at zero');
    expect(svc.setManualWeight).not.toHaveBeenCalled();
    el.remove();
  });

  it('shows the note field when the reason is other', async () => {
    const line = productLine('l1', 'Crushed stone');
    const el = await mountPanel(sale([line]));
    await chooseOption(q(el, 'scale-reason') as HTMLElement & { value: string }, 'other');
    await flush();
    expect(q(el, 'scale-note')).toBeTruthy();
    el.remove();
  });
});
