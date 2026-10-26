// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The branch editor's own cash rounding rule (CP-18): a branch turns the rule
// on for itself, and the setting travels with the branch it belongs to.

import { describe, expect, it, vi } from 'vitest';
import type { Location } from '../../../types/location';

const svc = vi.hoisted(() => ({
  listAllBranches: vi.fn(),
  updateBranch: vi.fn(),
  createBranch: vi.fn(),
  archiveBranch: vi.fn(),
}));

vi.mock('../../../services/LocationService.ts', async (orig) => {
  const real = await orig<typeof import('../../../services/LocationService.ts')>();
  return {
    ...real,
    LocationService: {
      listAllBranches: svc.listAllBranches,
      updateBranch: svc.updateBranch,
      createBranch: svc.createBranch,
      archiveBranch: svc.archiveBranch,
    },
  };
});

vi.mock('../../../services/BranchContext.ts', async (orig) => {
  const real = await orig<typeof import('../../../services/BranchContext.ts')>();
  return {
    ...real,
    branchContext: {
      current: null,
      branches: [],
      refresh: vi.fn().mockResolvedValue(undefined),
    },
  };
});

import './Branches';

const branch = (mode: 'OFF' | 'NEAREST_5_CENTS'): Location => ({
  id: 'b-1',
  parent_id: null,
  path: 'Main',
  type: 'branch',
  code: 'MAIN',
  description: null,
  name: 'Main',
  address: null,
  city: null,
  state: null,
  zip: null,
  phone: null,
  tax_jurisdiction_code: null,
  default_tax_rate: 0.08875,
  cash_rounding_mode: mode,
  timezone: 'America/New_York',
  active: true,
  branch_id: null,
  revision: 3,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
});

async function openEditor(mode: 'OFF' | 'NEAREST_5_CENTS'): Promise<HTMLElement> {
  svc.listAllBranches.mockResolvedValue([branch(mode)]);
  const el = document.createElement('gable-admin-branches');
  document.body.appendChild(el);
  await (el as unknown as { updateComplete: Promise<boolean> }).updateComplete;
  // The list loads after the first render.
  await new Promise((resolve) => setTimeout(resolve, 0));
  await (el as unknown as { updateComplete: Promise<boolean> }).updateComplete;
  const edit = el.querySelector('button[title="Edit"]') as HTMLButtonElement | null;
  expect(edit).not.toBeNull();
  edit!.click();
  await (el as unknown as { updateComplete: Promise<boolean> }).updateComplete;
  return el;
}

describe('the branch editor cash rounding setting', () => {
  it('shows the rule the branch carries, and the other one beside it', async () => {
    const el = await openEditor('OFF');
    const select = el.querySelector('[data-testid="branch-cash-rounding"]') as HTMLSelectElement | null;
    expect(select).not.toBeNull();
    expect(select!.value).toBe('OFF');
    const values = Array.from(select!.options).map((o) => o.value);
    expect(values).toEqual(['OFF', 'NEAREST_5_CENTS']);
    el.remove();
  });

  it('saves the rule the manager picked', async () => {
    const el = await openEditor('OFF');
    const select = el.querySelector('[data-testid="branch-cash-rounding"]') as HTMLSelectElement;
    select.value = 'NEAREST_5_CENTS';
    select.dispatchEvent(new Event('change'));
    await (el as unknown as { updateComplete: Promise<boolean> }).updateComplete;
    const save = Array.from(el.querySelectorAll('button')).find((b) => b.textContent?.includes('Save changes')) as HTMLButtonElement;
    expect(save).toBeDefined();
    save.click();
    await new Promise((resolve) => setTimeout(resolve, 0));
    await (el as unknown as { updateComplete: Promise<boolean> }).updateComplete;
    expect(svc.updateBranch).toHaveBeenCalledWith(
      'b-1',
      expect.objectContaining({ cash_rounding_mode: 'NEAREST_5_CENTS' }),
      3,
    );
    el.remove();
  });

  it('leaves a branch that already rounds the cash showing its own rule', async () => {
    const el = await openEditor('NEAREST_5_CENTS');
    const select = el.querySelector('[data-testid="branch-cash-rounding"]') as HTMLSelectElement;
    expect(select.value).toBe('NEAREST_5_CENTS');
    el.remove();
  });
});
