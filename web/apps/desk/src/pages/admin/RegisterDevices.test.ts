// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import './RegisterDevices';
import type { GableRegisterDevices } from './RegisterDevices';
import { branchContext } from '../../services/BranchContext';
import { flush, jsonResponse, mountAsync, q, text, update } from '../../test/dom';
import type { RegisterDevice } from '../../services/OfflineAdminService';

vi.mock('../../services/OfflineAdminService', async (orig) => ({
  ...(await orig<typeof import('../../services/OfflineAdminService')>()),
  shellDevicePublicKey: vi.fn(async () => null),
}));
import { shellDevicePublicKey } from '../../services/OfflineAdminService';

const device = (patch: Partial<RegisterDevice> = {}): RegisterDevice => ({
  register_id: 'REG-01', epoch: 2, status: 'active', enrolled_by: 'mgr-1', enrolled_at: '2030-01-01T10:00:00Z',
  revoked_at: null, synced_high_sequence: 9, ...patch,
});

let requests: { method: string; url: string; body?: Record<string, unknown> }[] = [];
// SEAM O2: the device routes are faked here until refactor/cp-offline-o2 lands
let handler: (method: string, url: string) => Response;

beforeEach(() => {
  requests = [];
  localStorage.setItem('token', 'test-token');
  branchContext.setCurrent('b-1');
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET';
    requests.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    return handler(method, url);
  }));
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
  branchContext.setCurrent(null);
});

const type = async (el: GableRegisterDevices, testid: string, value: string) => {
  const input = q<HTMLInputElement>(el, `[data-testid="${testid}"]`);
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  await update(el, {});
};

describe('the registers\' offline devices', () => {
  it('lists each register with its status', async () => {
    handler = () => jsonResponse({ items: [device(), device({ register_id: 'REG-02', status: 'none', epoch: 0, enrolled_by: null, enrolled_at: null })] });
    const el = await mountAsync<GableRegisterDevices>('gable-register-devices');
    expect(requests[0].url).toContain('/api/v1/pos/offline/devices?branch_id=b-1');
    expect(text(q(el, '[data-testid="device-REG-01"] [data-testid="device-status"]'))).toBe('Enrolled (epoch 2)');
    expect(text(q(el, '[data-testid="device-REG-02"] [data-testid="device-status"]'))).toBe('Not enrolled');
    expect(el.querySelector('[data-testid="device-REG-02"] [data-testid="device-revoke"]')).toBeNull();
  });

  it('revokes with the manager\'s staff number and counter PIN, and clears the PIN after', async () => {
    handler = (method) => (method === 'POST' ? jsonResponse(device({ status: 'revoked', revoked_at: '2030-02-01T10:00:00Z' })) : jsonResponse({ items: [device()] }));
    const el = await mountAsync<GableRegisterDevices>('gable-register-devices');
    q<HTMLButtonElement>(el, '[data-testid="device-revoke"]').click();
    await update(el, {});
    q<HTMLButtonElement>(el, '[data-testid="device-confirm"]').click();
    await update(el, {});
    expect(text(q(el, '[data-testid="register-devices-error"]'))).toContain('staff number and your counter PIN');
    await type(el, 'device-manager', 'M-100');
    await type(el, 'device-pin', '12ab34');
    expect(q<HTMLInputElement>(el, '[data-testid="device-pin"]').value).toBe('1234'); // digits only
    q<HTMLButtonElement>(el, '[data-testid="device-confirm"]').click();
    await flush();
    await update(el, {});
    const post = requests.find((r) => r.method === 'POST');
    expect(post?.url).toContain('/api/v1/pos/offline/devices/REG-01/revoke');
    expect(post?.body).toEqual({ manager: 'M-100', pin: '1234' });
    expect(text(q(el, '[data-testid="device-REG-01"] [data-testid="device-status"]'))).toContain('Revoked');
  });

  it('says a wrong PIN in the server\'s words, keeps the register as it was, and empties the PIN', async () => {
    handler = (method) => (method === 'POST'
      ? jsonResponse({ error: { code: 'step_up_refused', message: 'That PIN is not right' } }, 403)
      : jsonResponse({ items: [device()] }));
    const el = await mountAsync<GableRegisterDevices>('gable-register-devices');
    q<HTMLButtonElement>(el, '[data-testid="device-revoke"]').click();
    await update(el, {});
    await type(el, 'device-manager', 'M-100');
    await type(el, 'device-pin', '0000');
    q<HTMLButtonElement>(el, '[data-testid="device-confirm"]').click();
    await flush();
    await update(el, {});
    expect(text(q(el, '[data-testid="register-devices-error"]'))).toContain('That PIN is not right');
    expect(q<HTMLInputElement>(el, '[data-testid="device-pin"]').value).toBe('');
    expect(text(q(el, '[data-testid="device-REG-01"] [data-testid="device-status"]'))).toBe('Enrolled (epoch 2)');
  });

  it('sends no enrolment from a browser that holds no register key', async () => {
    handler = () => jsonResponse({ items: [device({ status: 'none', epoch: 0, enrolled_by: null })] });
    const el = await mountAsync<GableRegisterDevices>('gable-register-devices');
    q<HTMLButtonElement>(el, '[data-testid="device-enrol"]').click();
    await update(el, {});
    await type(el, 'device-manager', 'M-100');
    await type(el, 'device-pin', '1234');
    q<HTMLButtonElement>(el, '[data-testid="device-confirm"]').click();
    await flush();
    await update(el, {});
    expect(text(q(el, '[data-testid="register-devices-error"]'))).toContain("register's own work shell");
    expect(requests.some((r) => r.method === 'POST')).toBe(false);
  });

  it('enrols with the public key the shell hands over', async () => {
    vi.mocked(shellDevicePublicKey).mockResolvedValueOnce('PUBKEY-PLACEHOLDER');
    handler = (method) => (method === 'POST' ? jsonResponse(device({ epoch: 3 })) : jsonResponse({ items: [device({ status: 'none', epoch: 0, enrolled_by: null })] }));
    const el = await mountAsync<GableRegisterDevices>('gable-register-devices');
    q<HTMLButtonElement>(el, '[data-testid="device-enrol"]').click();
    await update(el, {});
    await type(el, 'device-manager', 'M-100');
    await type(el, 'device-pin', '1234');
    q<HTMLButtonElement>(el, '[data-testid="device-confirm"]').click();
    await flush();
    await update(el, {});
    const post = requests.find((r) => r.method === 'POST');
    expect(post?.url).toContain('/api/v1/pos/offline/devices');
    expect(post?.body).toEqual({ register_id: 'REG-01', public_key: 'PUBKEY-PLACEHOLDER', manager: 'M-100', pin: '1234' });
    expect(text(q(el, '[data-testid="device-REG-01"] [data-testid="device-status"]'))).toBe('Enrolled (epoch 3)');
  });

  it('shows nothing while the device routes are not built', async () => {
    handler = () => jsonResponse({ error: { code: 'not_found', message: 'no' } }, 404);
    const el = await mountAsync<GableRegisterDevices>('gable-register-devices');
    expect(el.querySelector('[data-testid="register-devices"]')).toBeNull();
  });
});
