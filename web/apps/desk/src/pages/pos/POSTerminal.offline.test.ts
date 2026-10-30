// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter when the connection drops (ADR 0011 slice C2, section 5.3),
// against a stand in for the work shell's core: the open cart becomes the
// local cart, a completion whose answer was lost is result unknown and holds
// the till close, the queue holds it too, and on reconnect the queue drains
// before new prices and the unknown cart is read and followed. A browser tab
// has no core and keeps the online only refusal.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Sale } from '../../types/pos';
import { setOfflineInvoke } from '../../services/OfflineCounterService';
import { fakeOfflineCore, type FakeCore } from '../../test/fakeOfflineCore';

const svc = vi.hoisted(() => ({
  listRegisters: vi.fn(),
  currentTill: vi.fn(),
  startSale: vi.fn(),
  completeSale: vi.fn(),
  searchProducts: vi.fn(),
  findCustomers: vi.fn(),
  attachCustomer: vi.fn(),
  addItem: vi.fn(),
  removeItem: vi.fn(),
  changeQuantity: vi.fn(),
  getSale: vi.fn(),
  openTill: vi.fn(),
  tillReport: vi.fn(),
  closeTill: vi.fn(),
  printReceipt: vi.fn(),
  emailReceipt: vi.fn(),
  getTerminalIntent: vi.fn(),
  cancelTerminalIntent: vi.fn(),
  answerReplacedCart: vi.fn(),
}));

const auth = vi.hoisted(() => ({ authMethodsConfig: vi.fn() }));
vi.mock('@gable/auth', async (orig) => ({ ...(await orig<typeof import('@gable/auth')>()), authMethodsConfig: auth.authMethodsConfig }));

vi.mock('../../services/POSService', async (orig) => {
  const real = await orig<typeof import('../../services/POSService')>();
  return { ...real, posService: svc };
});

import './POSTerminal';
import type { POSTerminal } from './POSTerminal';
import { NO_SESSION_ASK, OFFLINE_BANNER, QUEUE_HOLDS_CLOSE, RESULT_UNKNOWN, SAME_GOODS_QUESTION, UNKNOWN_HOLDS_CLOSE } from './offline';

const REG_01 = { id: 'REG-01', name: 'Front till', branch_id: 'b-main', branch_code: 'MAIN', branch_name: 'main yard' };
const q = (el: Element, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement | null;

const line = (id: string, cents: number) => ({
  id, position: 1, line_type: 'product', parent_line_id: null, product_id: `p-${id}`,
  charge_code_id: null, charge_code: null, sku: `SKU-${id}`, description: `Item ${id}`,
  quantity: '3', uom: 'EA', unit_price_ten_thousandths: cents * 100, line_total_cents: cents,
});

function sale(status = 'open'): Sale {
  return {
    id: 's1', number: 'POS-000101', revision: 3, branch_id: 'b', register_id: 'REG-01', cashier_id: 'c',
    customer_id: null, currency: 'USD', subtotal_cents: 8000, tax_cents: 650, total_cents: 8650,
    change_cents: 0, till_session_id: 't', status, invoice_id: null, completed_at: null,
    created_at: '', customer_po: null, job_id: null, tenders: [],
    lines: [line('a', 8650)],
  } as unknown as Sale;
}

let core: FakeCore;
let online = true;

const settle = async (el: POSTerminal) => {
  for (let i = 0; i < 6; i++) {
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
  }
};

async function mount(): Promise<POSTerminal> {
  svc.listRegisters.mockResolvedValue([REG_01]);
  svc.currentTill.mockResolvedValue({ id: 't', register_id: 'REG-01', cashier_id: 'c', status: 'open', opening_float_cents: 20000 });
  svc.startSale.mockResolvedValue(sale());
  const el = document.createElement('gable-pos-terminal') as POSTerminal;
  document.body.appendChild(el);
  await settle(el);
  return el;
}

function goOffline() {
  online = false;
  window.dispatchEvent(new Event('offline'));
}

function goOnline() {
  online = true;
  window.dispatchEvent(new Event('online'));
}

describe('the counter when the connection drops', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    online = true;
    vi.spyOn(navigator, 'onLine', 'get').mockImplementation(() => online);
    core = fakeOfflineCore();
    setOfflineInvoke(core.invoke);
  });
  afterEach(() => {
    setOfflineInvoke(undefined);
    vi.restoreAllMocks();
    document.body.innerHTML = '';
  });

  it('carries the open cart into the offline mode, with the banner and the waiting figures', async () => {
    const el = await mount();
    goOffline();
    await settle(el);
    expect(q(el, 'pos-offline-banner')?.textContent).toContain(OFFLINE_BANNER);
    expect(q(el, 'pos-offline-banner-figures')?.textContent).toContain('Room left');
    expect(q(el, 'pos-offline-banner-figures')?.textContent).toMatch(/\$\d{1,3}(,\d{3})+\.\d{2}/);
    for (const id of ['pos-drawer-cash', 'pos-x-report', 'pos-z-reports', 'pos-close-till', 'return-search-open']) {
      const b = q(el, id) as HTMLButtonElement;
      expect(b.disabled, id).toBe(true);
      expect(b.title, id).toBe('needs the connection');
    }
    const legend = el.querySelector('[data-testid="pos-hotkey-legend"]');
    expect(legend?.querySelectorAll('[data-action]').length).toBeGreaterThan(0);
    expect(legend?.querySelectorAll('[data-action]:not(.opacity-40)').length).toBe(0);
    const offline = q(el, 'pos-offline-mode') as HTMLElement & { seed: { lines: unknown[]; replacesSaleId: string } };
    expect(offline).toBeTruthy();
    expect(offline.seed.replacesSaleId).toBe('s1');
    expect(offline.seed.lines).toEqual([{ id: expect.any(String), product_id: 'p-a', quantity: '3', uom: 'EA' }]);
    expect(q(el, 'pos-pay-bar')).toBeNull();
    el.remove();
  });

  it('holds a cart whose completion answer was lost as result unknown, carries nothing and holds the close', async () => {
    const el = await mount();
    svc.completeSale.mockImplementation(async () => { online = false; throw new TypeError('Failed to fetch'); });
    q(el, 'tender-cash')?.click();
    await el.updateComplete;
    (q(el, 'pos-pay') as HTMLButtonElement).click();
    await settle(el);
    expect(el.textContent).toContain(RESULT_UNKNOWN);
    expect(localStorage.getItem('gable.pos.offline.unknown-cart.REG-01')).toBe('s1');
    const offline = q(el, 'pos-offline-mode') as HTMLElement & { seed: unknown; unknownCartId: string };
    expect(offline.seed).toBeNull();
    expect(offline.unknownCartId).toBe('s1');
    await (el as unknown as { _beginCloseTill(): Promise<void> })._beginCloseTill();
    await settle(el);
    expect(el.textContent).toContain(UNKNOWN_HOLDS_CLOSE);
    expect(svc.tillReport).not.toHaveBeenCalled();
    el.remove();
  });

  it('holds the till close while the queue has sales the server has not answered', async () => {
    const el = await mount();
    core.queue = { ...core.queue, waiting_count: 1, items: [{ device_seq: 1, state: 'waiting', reason_code: null, reason_message: null, invoice_id: null, invoice_number: null, sale: {} as never }] };
    await (el as unknown as { _beginCloseTill(): Promise<void> })._beginCloseTill();
    await settle(el);
    expect(el.textContent).toContain(QUEUE_HOLDS_CLOSE);
    expect(svc.tillReport).not.toHaveBeenCalled();
    el.remove();
  });

  it('drains the queue before it takes new prices on reconnect, and says what was sent', async () => {
    const el = await mount();
    goOffline();
    await settle(el);
    core.calls.length = 0;
    core.sync = () => ({ sent: 2, posted: 1, refused: 1, held: 0, resolved: 0, errors: 0, confirmed_seq: 2, open_exception_count: 1, queue: core.queue });
    goOnline();
    await settle(el);
    const order = core.calls.map((c) => c.command).filter((c) => c === 'sync_offline_queue' || c === 'refresh_offline_snapshot');
    expect(order).toEqual(['sync_offline_queue', 'refresh_offline_snapshot']);
    expect(q(el, 'pos-offline-strip')?.textContent).toContain('Sent 1, 1 needs a manager');
    expect(q(el, 'pos-offline-mode')).toBeNull();
    el.remove();
  });

  // Review of PR 246: a web sign in writes only the cookie, so the core's
  // no_session needs the shell's own sign in (begin_sign_in) to put a token in
  // the keychain, with the dealer's configured redirect method, then a re-run.
  describe('the core answers no_session', () => {
    const noSession = { code: 'no_session', message: 'Sign in on this register again.' };
    const methods = (list: Array<{ method: string; kind: string }>) => ({
      authMode: 'production', dealerName: null, dealerMarkUrl: null, degraded: false, notice: null,
      methods: list.map((m) => ({ ...m, label: m.method, start_url: '/start', issuer_url: null, client_id: null })),
    });
    beforeEach(() => {
      auth.authMethodsConfig.mockResolvedValue(methods([{ method: 'google', kind: 'redirect' }, { method: 'email_password', kind: 'form' }]));
    });

    it('offers Sign in this register on the strip when the sync answers no_session', async () => {
      core.refuse['sync_offline_queue'] = noSession;
      const el = await mount();
      await settle(el);
      const ask = q(el, 'pos-offline-sign-in') as HTMLButtonElement;
      expect(ask.textContent).toContain(NO_SESSION_ASK);
      expect(q(el, 'pos-offline-strip')?.textContent).toContain(noSession.message);
      el.remove();
    });

    it('offers it when only the snapshot fetch answers no_session', async () => {
      core.refuse['refresh_offline_snapshot'] = noSession;
      const el = await mount();
      await settle(el);
      expect(q(el, 'pos-offline-sign-in')).toBeTruthy();
      el.remove();
    });

    it('offers nothing for any other refusal', async () => {
      core.refuse['sync_offline_queue'] = { code: 'server_unreachable', message: 'The server cannot be reached' };
      const el = await mount();
      await settle(el);
      expect(q(el, 'pos-offline-sign-in')).toBeNull();
      el.remove();
    });

    it('starts the shell sign in with the dealer redirect method, then re-runs the reconnect', async () => {
      core.refuse['sync_offline_queue'] = noSession;
      core.refuse['refresh_offline_snapshot'] = noSession;
      const el = await mount();
      await settle(el);
      delete core.refuse['sync_offline_queue'];
      delete core.refuse['refresh_offline_snapshot'];
      core.calls.length = 0;
      (q(el, 'pos-offline-sign-in') as HTMLButtonElement).click();
      await settle(el);
      const order = core.calls.map((c) => c.command).filter((c) => ['begin_sign_in', 'sync_offline_queue', 'refresh_offline_snapshot'].includes(c));
      expect(order).toEqual(['begin_sign_in', 'sync_offline_queue', 'refresh_offline_snapshot']);
      expect(core.calls.find((c) => c.command === 'begin_sign_in')?.args).toEqual({ method: 'google' });
      expect(q(el, 'pos-offline-sign-in')).toBeNull();
      el.remove();
    });

    it('says so, and keeps the ask, when the shell sign in is refused or no redirect method is allowed', async () => {
      core.refuse['sync_offline_queue'] = noSession;
      core.refuse['begin_sign_in'] = { code: 'timed_out', message: 'The sign in did not finish in time.' };
      const el = await mount();
      await settle(el);
      (q(el, 'pos-offline-sign-in') as HTMLButtonElement).click();
      await settle(el);
      expect(q(el, 'pos-offline-strip')?.textContent).toContain('The sign in did not finish in time.');
      expect(q(el, 'pos-offline-sign-in')).toBeTruthy();
      delete core.refuse['begin_sign_in'];
      auth.authMethodsConfig.mockResolvedValue(methods([{ method: 'magic_link', kind: 'link' }]));
      core.calls.length = 0;
      (q(el, 'pos-offline-sign-in') as HTMLButtonElement).click();
      await settle(el);
      expect(core.calls.some((c) => c.command === 'begin_sign_in')).toBe(false);
      expect(q(el, 'pos-offline-strip')?.textContent).toContain('no sign in method');
      expect(q(el, 'pos-offline-sign-in')).toBeTruthy();
      el.remove();
    });

    it('says the server cannot be reached, and keeps the ask, when the methods answer degraded during a drop', async () => {
      core.refuse['sync_offline_queue'] = noSession;
      auth.authMethodsConfig.mockResolvedValue({ ...methods([{ method: 'email_password', kind: 'form' }]), degraded: true, notice: 'degraded' });
      const el = await mount();
      await settle(el);
      core.calls.length = 0;
      (q(el, 'pos-offline-sign-in') as HTMLButtonElement).click();
      await settle(el);
      expect(core.calls.some((c) => c.command === 'begin_sign_in')).toBe(false);
      expect(q(el, 'pos-offline-strip')?.textContent).toContain('The server cannot be reached');
      expect(q(el, 'pos-offline-strip')?.textContent).not.toContain('no sign in method');
      expect(q(el, 'pos-offline-sign-in')).toBeTruthy();
      el.remove();
    });
  });

  // CP-13 PR 3: with email and password first the strip holds its own form.
  describe('the strip form for email and password', () => {
    const noSession = { code: 'no_session', message: 'Sign in on this register again.' };
    const cfg = (list: Array<{ method: string; kind: string }>, degraded = false) => ({
      authMode: 'production', dealerName: null, dealerMarkUrl: null, degraded, notice: degraded ? 'degraded' : null,
      methods: list.map((m) => ({ ...m, label: m.method, start_url: '/start', issuer_url: null, client_id: null })),
    });
    const passwordFirst = () => cfg([{ method: 'email_password', kind: 'form' }, { method: 'google', kind: 'redirect' }]);
    const field = (el: Element, id: string) => q(el, id) as HTMLInputElement;
    async function openForm() {
      core.refuse['sync_offline_queue'] = noSession;
      core.refuse['refresh_offline_snapshot'] = noSession;
      auth.authMethodsConfig.mockResolvedValue(passwordFirst());
      const el = await mount();
      await settle(el);
      core.calls.length = 0;
      (q(el, 'pos-offline-sign-in') as HTMLButtonElement).click();
      await settle(el);
      return el;
    }
    async function submit(el: POSTerminal, email = 'clerk@yard.example', password = 'hunter2-secret') {
      field(el, 'pos-offline-pw-email').value = email;
      field(el, 'pos-offline-pw-email').dispatchEvent(new Event('input', { bubbles: true }));
      field(el, 'pos-offline-pw-password').value = password;
      field(el, 'pos-offline-pw-password').dispatchEvent(new Event('input', { bubbles: true }));
      (q(el, 'pos-offline-pw-submit') as HTMLButtonElement).click();
      await settle(el);
    }

    it('shows the form, with the right autocomplete, and never starts begin_sign_in', async () => {
      const el = await openForm();
      expect(field(el, 'pos-offline-pw-email').getAttribute('autocomplete')).toBe('username');
      expect(field(el, 'pos-offline-pw-password').getAttribute('autocomplete')).toBe('current-password');
      expect(field(el, 'pos-offline-pw-password').type).toBe('password');
      expect(q(el, 'pos-offline-pw-submit')?.textContent).toContain(NO_SESSION_ASK);
      expect(q(el, 'pos-offline-pw-cancel')).toBeTruthy();
      expect(core.calls.some((c) => c.command === 'begin_sign_in')).toBe(false);
      el.remove();
    });

    it('calls sign_in_with_password once with the typed values, then sync and snapshot in that order', async () => {
      const el = await openForm();
      delete core.refuse['sync_offline_queue'];
      delete core.refuse['refresh_offline_snapshot'];
      core.calls.length = 0;
      await submit(el);
      const names = ['sign_in_with_password', 'sync_offline_queue', 'refresh_offline_snapshot'];
      expect(core.calls.map((c) => c.command).filter((c) => names.includes(c))).toEqual(names);
      expect(core.calls.find((c) => c.command === 'sign_in_with_password')?.args).toEqual({ email: 'clerk@yard.example', password: 'hunter2-secret' });
      expect(q(el, 'pos-offline-pw-form')).toBeNull();
      expect(q(el, 'pos-offline-sign-in')).toBeNull();
      el.remove();
    });

    it('does not keep the password after submit: the field is empty and no state or DOM holds it', async () => {
      const el = await openForm();
      delete core.refuse['sync_offline_queue'];
      delete core.refuse['refresh_offline_snapshot'];
      const secret = 'hunter2-secret';
      await submit(el, 'clerk@yard.example', secret);
      const held = Object.entries(el as unknown as Record<string, unknown>).filter(([, v]) => typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean' || v === null);
      expect(held.length).toBeGreaterThan(10);
      expect(JSON.stringify(held)).not.toContain(secret);
      const plain = Object.values(el as unknown as Record<string, unknown>).filter((v): v is object => typeof v === 'object' && v !== null && !(v instanceof Node) && (Array.isArray(v) || Object.getPrototypeOf(v) === Object.prototype));
      const safe = (v: object) => { try { return JSON.stringify(v); } catch { return ''; } };
      expect(plain.map(safe).join('')).not.toContain(secret);
      expect(el.innerHTML).not.toContain(secret);
      el.remove();
    });

    it.each([
      ['sign_in_refused', 'That email and password did not sign this register in. Type the password again.'],
      ['sign_in_limited', 'Too many tries. Wait a little, then try again.'],
      ['sign_in_not_allowed', 'Ask a manager'],
      ['sign_in_unavailable', 'The server cannot be reached'],
      ['no_server', 'This register has no server set, so it cannot sign in. Ask a manager.'],
    ])('shows the words for %s, empties the password field, and keeps the form', async (code, words) => {
      const el = await openForm();
      core.refuse['sign_in_with_password'] = code;
      await submit(el, 'clerk@yard.example', 'hunter2-secret');
      expect(q(el, 'pos-offline-pw-error')?.textContent).toContain(words);
      expect(field(el, 'pos-offline-pw-password').value).toBe('');
      expect(field(el, 'pos-offline-pw-email').value).toBe('clerk@yard.example');
      expect(el.innerHTML).not.toContain('hunter2-secret');
      el.remove();
    });

    it('empties the password field even when the call has not yet answered', async () => {
      const el = await openForm();
      let release: () => void = () => undefined;
      const held = new Promise<void>((r) => { release = r; });
      const inner = core.invoke;
      setOfflineInvoke(async (c, a) => { if (c === 'sign_in_with_password') { core.calls.push({ command: c, args: a }); await held; return undefined; } return inner(c, a); });
      field(el, 'pos-offline-pw-password').value = 'hunter2-secret';
      field(el, 'pos-offline-pw-password').dispatchEvent(new Event('input', { bubbles: true }));
      (q(el, 'pos-offline-pw-submit') as HTMLButtonElement).click();
      await settle(el);
      expect(field(el, 'pos-offline-pw-password').value).toBe('');
      release();
      await settle(el);
      el.remove();
    });

    it('cancel closes the form and clears both fields', async () => {
      const el = await openForm();
      field(el, 'pos-offline-pw-password').value = 'hunter2-secret';
      field(el, 'pos-offline-pw-password').dispatchEvent(new Event('input', { bubbles: true }));
      (q(el, 'pos-offline-pw-cancel') as HTMLButtonElement).click();
      await settle(el);
      expect(q(el, 'pos-offline-pw-form')).toBeNull();
      expect(q(el, 'pos-offline-sign-in')).toBeTruthy();
      expect(core.calls.some((c) => c.command === 'sign_in_with_password')).toBe(false);
      el.remove();
    });

    it('puts focus on the email field when the form opens', async () => {
      const el = await openForm();
      expect(document.activeElement).toBe(field(el, 'pos-offline-pw-email'));
      el.remove();
    });

    it('puts focus back on the ask button when Cancel closes the form', async () => {
      const el = await openForm();
      (q(el, 'pos-offline-pw-cancel') as HTMLButtonElement).click();
      await settle(el);
      expect(document.activeElement).toBe(q(el, 'pos-offline-sign-in'));
      el.remove();
    });

    it('puts focus on the password field after a refused answer', async () => {
      const el = await openForm();
      core.refuse['sign_in_with_password'] = 'sign_in_refused';
      await submit(el);
      expect(document.activeElement).toBe(field(el, 'pos-offline-pw-password'));
      el.remove();
    });

    it('reads Signing in on the submit button while the call runs', async () => {
      const el = await openForm();
      let release: () => void = () => undefined;
      const held = new Promise<void>((r) => { release = r; });
      const inner = core.invoke;
      setOfflineInvoke(async (c, a) => { if (c === 'sign_in_with_password') { await held; return undefined; } return inner(c, a); });
      (q(el, 'pos-offline-pw-submit') as HTMLButtonElement).click();
      await settle(el);
      expect(q(el, 'pos-offline-pw-submit')?.textContent?.trim()).toBe('Signing in');
      release();
      await settle(el);
      el.remove();
    });

    it('holds the two buttons together in one row beneath the fields', async () => {
      const el = await openForm();
      const buttons = q(el, 'pos-offline-pw-buttons') as HTMLElement;
      expect(buttons.contains(q(el, 'pos-offline-pw-submit'))).toBe(true);
      expect(buttons.contains(q(el, 'pos-offline-pw-cancel'))).toBe(true);
      el.remove();
    });

    it('a degraded config shows the unreachable words, no form, and the next tap reads the config again', async () => {
      core.refuse['sync_offline_queue'] = noSession;
      auth.authMethodsConfig.mockResolvedValue(cfg([{ method: 'email_password', kind: 'form' }], true));
      const el = await mount();
      await settle(el);
      (q(el, 'pos-offline-sign-in') as HTMLButtonElement).click();
      await settle(el);
      expect(q(el, 'pos-offline-pw-form')).toBeNull();
      expect(q(el, 'pos-offline-strip')?.textContent).toContain('The server cannot be reached');
      auth.authMethodsConfig.mockResolvedValue(passwordFirst());
      (q(el, 'pos-offline-sign-in') as HTMLButtonElement).click();
      await settle(el);
      expect(q(el, 'pos-offline-pw-form')).toBeTruthy();
      el.remove();
    });

    it('a dealer with google first still starts begin_sign_in and shows no form', async () => {
      core.refuse['sync_offline_queue'] = noSession;
      auth.authMethodsConfig.mockResolvedValue(cfg([{ method: 'google', kind: 'redirect' }, { method: 'email_password', kind: 'form' }]));
      const el = await mount();
      await settle(el);
      (q(el, 'pos-offline-sign-in') as HTMLButtonElement).click();
      await settle(el);
      expect(core.calls.find((c) => c.command === 'begin_sign_in')?.args).toEqual({ method: 'google' });
      expect(q(el, 'pos-offline-pw-form')).toBeNull();
      el.remove();
    });
  });

  it('reads the unknown cart on reconnect: abandoned by a linked sale, the clerk is asked and the answer recorded', async () => {
    localStorage.setItem('gable.pos.offline.unknown-cart.REG-01', 's1');
    svc.getSale.mockResolvedValue(sale('abandoned'));
    core.queue = { ...core.queue, items: [{ device_seq: 1, state: 'synced', reason_code: null, reason_message: null, invoice_id: null, invoice_number: 'IN-1',
      sale: { client_id: 'off-1', offline_ref: 'REG-01-2-000001', replaces_sale_id: 's1' } as never }] };
    const el = await mount();
    await settle(el);
    expect(svc.getSale).toHaveBeenCalledWith('s1');
    expect(q(el, 'pos-offline-same-goods')?.textContent).toContain(SAME_GOODS_QUESTION);
    expect(q(el, 'pos-offline-same-goods')?.textContent).toContain('REG-01-2-000001');
    svc.answerReplacedCart.mockResolvedValue(undefined);
    q(el, 'pos-offline-same-goods-no')?.click();
    await settle(el);
    expect(svc.answerReplacedCart).toHaveBeenCalledWith('off-1', false);
    expect(localStorage.getItem('gable.pos.offline.unknown-cart.REG-01')).toBeNull();
    el.remove();
  });

  it('shows the unknown cart complete when it committed online', async () => {
    localStorage.setItem('gable.pos.offline.unknown-cart.REG-01', 's1');
    svc.getSale.mockResolvedValue(sale('completed'));
    const el = await mount();
    await settle(el);
    expect(el.textContent).toContain('completed online: POS-000101');
    expect(localStorage.getItem('gable.pos.offline.unknown-cart.REG-01')).toBeNull();
    el.remove();
  });

  // ADR 0011 5.3: on reconnect the page drains the queue FIRST, then reads the
  // unknown cart. A default linked offline sale still waiting means the cart
  // has not been matched yet: it must stay result unknown, not return to the
  // screen to be completed online a second time.
  it('keeps the cart result unknown while the drain failed and a linked sale still waits', async () => {
    localStorage.setItem('gable.pos.offline.unknown-cart.REG-01', 's1');
    core.refuse['sync_offline_queue'] = { code: 'server_unreachable', message: 'The server cannot be reached' };
    core.queue = { ...core.queue, waiting_count: 1, items: [{ device_seq: 1, state: 'waiting', reason_code: null, reason_message: null, invoice_id: null, invoice_number: null,
      sale: { client_id: 'off-1', offline_ref: 'REG-01-2-000001', replaces_sale_id: 's1' } as never }] };
    svc.getSale.mockResolvedValue(sale('open'));
    const el = await mount();
    await settle(el);
    expect(localStorage.getItem('gable.pos.offline.unknown-cart.REG-01')).toBe('s1');
    expect(svc.getSale).not.toHaveBeenCalled();
    el.remove();
  });

  it('keeps the cart result unknown while the sync answered but a linked sale still waits', async () => {
    localStorage.setItem('gable.pos.offline.unknown-cart.REG-01', 's1');
    core.queue = { ...core.queue, waiting_count: 1, items: [{ device_seq: 1, state: 'pending', reason_code: null, reason_message: null, invoice_id: null, invoice_number: null,
      sale: { client_id: 'off-1', offline_ref: 'REG-01-2-000001', replaces_sale_id: 's1' } as never }] };
    core.sync = () => ({ sent: 1, posted: 0, refused: 0, held: 0, resolved: 0, errors: 1, confirmed_seq: 0, open_exception_count: 0, queue: core.queue });
    svc.getSale.mockResolvedValue(sale('open'));
    const el = await mount();
    await settle(el);
    expect(localStorage.getItem('gable.pos.offline.unknown-cart.REG-01')).toBe('s1');
    expect(svc.getSale).not.toHaveBeenCalled();
    el.remove();
  });

  // ADR 0011 5.3: a terminal call sent and not answered is result unknown
  // too: the cart is never converted and no offline tender is offered for it.
  it('does not carry a cart whose card is waiting at the terminal into the offline mode', async () => {
    const el = await mount();
    (el as unknown as { _cardIntent: unknown })._cardIntent = { id: 'ti-1', state: 'pending_at_device', amount_cents: 8650 };
    goOffline();
    await settle(el);
    const offline = q(el, 'pos-offline-mode') as HTMLElement & { seed: unknown; unknownCartId: string | null };
    expect(offline).toBeTruthy();
    expect(offline.seed).toBeNull();
    expect(offline.unknownCartId).toBe('s1');
    expect(localStorage.getItem('gable.pos.offline.unknown-card.REG-01')).toBe('ti-1');
    el.remove();
  });

  it('does not carry a cart whose card read was in flight when the connection dropped', async () => {
    const el = await mount();
    svc.getTerminalIntent.mockImplementation(() => new Promise(() => {}));
    void (el as unknown as { _watchCard(id: string): Promise<void> })._watchCard('ti-2');
    goOffline();
    await settle(el);
    const offline = q(el, 'pos-offline-mode') as HTMLElement & { seed: unknown; unknownCartId: string | null };
    expect(offline.seed).toBeNull();
    expect(offline.unknownCartId).toBe('s1');
    el.remove();
  });

  it('reads the unknown cart\'s card leg on reconnect and keeps the cart unknown while the terminal holds it', async () => {
    localStorage.setItem('gable.pos.offline.unknown-cart.REG-01', 's1');
    localStorage.setItem('gable.pos.offline.unknown-card.REG-01', 'ti-1');
    svc.getTerminalIntent.mockResolvedValue({ id: 'ti-1', state: 'pending_at_device', amount_cents: 8650 });
    svc.getSale.mockResolvedValue(sale('open'));
    const el = await mount();
    await settle(el);
    expect(svc.getTerminalIntent).toHaveBeenCalledWith('ti-1');
    expect(svc.getSale).not.toHaveBeenCalled();
    expect(localStorage.getItem('gable.pos.offline.unknown-cart.REG-01')).toBe('s1');
    el.remove();
  });

  it('settles the unknown cart once its card leg is answered', async () => {
    localStorage.setItem('gable.pos.offline.unknown-cart.REG-01', 's1');
    localStorage.setItem('gable.pos.offline.unknown-card.REG-01', 'ti-1');
    svc.getTerminalIntent.mockResolvedValue({ id: 'ti-1', state: 'declined', amount_cents: 8650 });
    svc.getSale.mockResolvedValue(sale('open'));
    const el = await mount();
    await settle(el);
    expect(svc.getSale).toHaveBeenCalledWith('s1');
    expect(localStorage.getItem('gable.pos.offline.unknown-cart.REG-01')).toBeNull();
    expect(localStorage.getItem('gable.pos.offline.unknown-card.REG-01')).toBeNull();
    el.remove();
  });

  it('keeps the online only refusal in a browser tab, which has no core', async () => {
    setOfflineInvoke(null);
    const el = await mount();
    goOffline();
    await settle(el);
    expect(q(el, 'pos-offline-mode')).toBeNull();
    q(el, 'tender-cash')?.click();
    await el.updateComplete;
    (q(el, 'pos-pay') as HTMLButtonElement).click();
    await settle(el);
    expect(el.textContent).toContain('money never queues');
    expect(svc.completeSale).not.toHaveBeenCalled();
    el.remove();
  });
});
