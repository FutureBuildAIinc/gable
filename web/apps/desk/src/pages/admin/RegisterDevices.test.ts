// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import './RegisterDevices';
import type { GableRegisterDevices } from './RegisterDevices';
import { branchContext } from '../../services/BranchContext';
import { flush, jsonResponse, mountAsync, q, text, update } from '../../test/dom';

vi.mock('../../services/OfflineAdminService', async (orig) => ({
  ...(await orig<typeof import('../../services/OfflineAdminService')>()),
  shellDevicePublicKey: vi.fn(async () => null),
}));
import { shellDevicePublicKey } from '../../services/OfflineAdminService';

const wire = (register: string, state: 'active' | 'revoked' | 'none' = 'active') => {
  const dev = (revoked: string | null) => ({
    register_id: register, device_epoch: 2, public_key: 'k', enrolled_by: 'mgr-1', enrolled_at: '2030-01-01T10:00:00Z',
    revoked_by: null, revoked_at: revoked, revoke_reason: null, high_seq: 9, chain_head: 'h', confirmed_seq: 9, reported_seq: 9,
  });
  return {
    register_id: register, branch_id: 'b-1', next_epoch: state === 'none' ? 1 : 3,
    device: state === 'active' ? dev(null) : null, previous: state === 'revoked' ? dev('2030-02-01T10:00:00Z') : null,
    unknown_tail_exception_id: null, sessions: null,
  };
};
const registers = (...ids: string[]) => jsonResponse({ items: ids.map((id) => ({ id, name: id, branch_id: 'b-1', branch_code: 'M', branch_name: 'Main' })) });
const DEV = (id: string) => `/api/v1/pos/offline/registers/${id}/device`;

let requests: { method: string; url: string; body?: Record<string, unknown> }[] = [];
// the registers list and the device read are answered per URL, as the server does
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
    handler = (_m, url) => (url.endsWith('/pos/registers') ? registers('REG-01', 'REG-02') : jsonResponse(url.includes('REG-01') ? wire('REG-01') : wire('REG-02', 'none')));
    const el = await mountAsync<GableRegisterDevices>('gable-register-devices');
    expect(requests.map((r) => r.url)).toContain(DEV('REG-02'));
    expect(text(q(el, '[data-testid="device-REG-01"] [data-testid="device-status"]'))).toBe('Enrolled (epoch 2)');
    expect(text(q(el, '[data-testid="device-REG-02"] [data-testid="device-status"]'))).toBe('Not enrolled');
    expect(el.querySelector('[data-testid="device-REG-02"] [data-testid="device-revoke"]')).toBeNull();
  });

  it('revokes with the manager\'s staff number and counter PIN, and clears the PIN after', async () => {
    handler = (method, url) => (method === 'POST' ? jsonResponse(wire('REG-01', 'revoked')) : url.endsWith('/pos/registers') ? registers('REG-01') : jsonResponse(wire('REG-01')));
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
    await update(el, {});
    expect(text(q(el, '[data-testid="register-devices-error"]'))).toContain('Say why');
    expect(requests.some((r) => r.method === 'POST')).toBe(false);
    await type(el, 'device-reason', 'Lost at the yard');
    q<HTMLButtonElement>(el, '[data-testid="device-confirm"]').click();
    await flush();
    await update(el, {});
    const post = requests.find((r) => r.method === 'POST');
    expect(post?.url).toContain(`${DEV('REG-01')}/revoke`);
    expect(post?.body).toEqual({ device_epoch: 2, reason: 'Lost at the yard', manager: 'M-100', pin: '1234' });
    expect(text(q(el, '[data-testid="device-REG-01"] [data-testid="device-status"]'))).toContain('Revoked');
  });

  it('says a wrong PIN in the server\'s words, keeps the register as it was, and empties the PIN', async () => {
    handler = (method, url) => (method === 'POST'
      ? jsonResponse({ error: { code: 'step_up_refused', message: 'That PIN is not right' } }, 403)
      : url.endsWith('/pos/registers') ? registers('REG-01') : jsonResponse(wire('REG-01')));
    const el = await mountAsync<GableRegisterDevices>('gable-register-devices');
    q<HTMLButtonElement>(el, '[data-testid="device-revoke"]').click();
    await update(el, {});
    await type(el, 'device-manager', 'M-100');
    await type(el, 'device-pin', '0000');
    await type(el, 'device-reason', 'Lost at the yard');
    q<HTMLButtonElement>(el, '[data-testid="device-confirm"]').click();
    await flush();
    await update(el, {});
    expect(text(q(el, '[data-testid="register-devices-error"]'))).toContain('That PIN is not right');
    expect(q<HTMLInputElement>(el, '[data-testid="device-pin"]').value).toBe('');
    expect(text(q(el, '[data-testid="device-REG-01"] [data-testid="device-status"]'))).toBe('Enrolled (epoch 2)');
  });

  it('sends no enrolment from a browser that holds no register key', async () => {
    handler = (_m, url) => (url.endsWith('/pos/registers') ? registers('REG-01') : jsonResponse(wire('REG-01', 'none')));
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
    handler = (method, url) => (method === 'POST' ? jsonResponse({ ...wire('REG-01'), device: { ...wire('REG-01').device, device_epoch: 3 } })
      : url.endsWith('/pos/registers') ? registers('REG-01') : jsonResponse(wire('REG-01', 'none')));
    const el = await mountAsync<GableRegisterDevices>('gable-register-devices');
    q<HTMLButtonElement>(el, '[data-testid="device-enrol"]').click();
    await update(el, {});
    await type(el, 'device-manager', 'M-100');
    await type(el, 'device-pin', '1234');
    q<HTMLButtonElement>(el, '[data-testid="device-confirm"]').click();
    await flush();
    await update(el, {});
    const post = requests.find((r) => r.method === 'POST');
    expect(post?.url).toContain(DEV('REG-01'));
    expect(post?.body).toEqual({ device_epoch: 1, public_key: 'PUBKEY-PLACEHOLDER', manager: 'M-100', pin: '1234' });
    expect(text(q(el, '[data-testid="device-REG-01"] [data-testid="device-status"]'))).toBe('Enrolled (epoch 3)');
  });

  it('shows nothing to a user the server refuses', async () => {
    handler = () => jsonResponse({ error: { code: 'forbidden', message: 'no' } }, 403);
    const el = await mountAsync<GableRegisterDevices>('gable-register-devices');
    expect(el.querySelector('[data-testid="register-devices"]')).toBeNull();
  });
});
