// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { fetchWithAuth } from './fetchClient';
import { parseApiError } from './apiError';
import { posService } from './POSService';

const API_URL = import.meta.env.VITE_API_URL || '';
const JSON_HEADERS = { 'Content-Type': 'application/json' };

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (response.ok) return response;
    throw await parseApiError(response, fallback);
}

export type OfflineTender = 'cash' | 'check' | 'account';

/** A branch's resolved offline policy (GET /api/v1/pos/offline/policy, built in slice O1). */
export interface OfflinePolicy {
    branch_id: string;
    enabled: boolean;
    tenders: OfflineTender[];
    sale_cap_cents: number;
    check_cap_cents: number;
    account_cap_cents: number;
    queue_total_cap_cents: number;
    max_queued: number;
    enrolled_register_count: number;
    max_account_exposure_cents: number;
    source: 'branch' | 'default' | 'none';
    updated_by: string | null;
    updated_at: string | null;
    revision: number | null;
}

export interface OfflinePolicyInput {
    enabled: boolean;
    tenders: OfflineTender[];
    sale_cap_cents: number;
    check_cap_cents: number;
    account_cap_cents: number;
    queue_total_cap_cents: number;
    max_queued: number;
}

/** The ways a manager settles an exception (POST .../exceptions/{id}/resolve, slice O4). */
export type ExceptionResolution = 'retry' | 'accept' | 'late_post' | 'write_off' | 'post_separate' | 'restate' | 'review';

/** One offline exception as GET /api/v1/pos/offline/exceptions answers it (ADR 0011 section 3.4). */
export interface OfflineException {
    id: string;
    sale_id: string;
    register_id: string;
    branch_id: string;
    device_epoch: number;
    device_seq: number;
    offline_ref: string;
    till_session_id: string | null;
    business_date: string;
    reason_code: string;
    reason_message: string;
    total_cents: number;
    status: 'open' | 'held' | 'resolved' | 'relinked' | 'superseded' | 'found' | 'reviewed';
    /** What the server allows for this reason: the screen offers exactly these. */
    actions: ExceptionResolution[];
    revision: number;
    created_at: string;
}

/**
 * One register's device (GET /api/v1/pos/offline/registers/{id}/device, slice
 * O2), folded to what the screen shows. status none is a register never
 * enrolled; next_epoch is the epoch the next enrolment must name.
 */
export interface RegisterDevice {
    register_id: string;
    next_epoch: number;
    epoch: number;
    status: 'active' | 'revoked' | 'none';
    enrolled_by: string | null;
    enrolled_at: string | null;
    revoked_at: string | null;
}

interface WireDevice {
    device_epoch: number;
    enrolled_by: string;
    enrolled_at: string;
    revoked_at: string | null;
}

interface WireEnrolment {
    register_id: string;
    next_epoch: number;
    device: WireDevice | null;
    previous: WireDevice | null;
}

function foldDevice(w: WireEnrolment): RegisterDevice {
    const d = w.device;
    const p = w.previous;
    const shown = d ?? p;
    return {
        register_id: w.register_id,
        next_epoch: w.next_epoch,
        epoch: shown?.device_epoch ?? 0,
        status: d ? 'active' : p ? 'revoked' : 'none',
        enrolled_by: shown?.enrolled_by ?? null,
        enrolled_at: shown?.enrolled_at ?? null,
        revoked_at: d ? null : p?.revoked_at ?? null,
    };
}

/** The manager's proof at the register: their staff number and own counter PIN. */
export interface ManagerProof {
    manager: string;
    pin: string;
}

export const offlineAdminService = {
    async policy(branchID: string): Promise<OfflinePolicy> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/offline/policy?branch_id=${encodeURIComponent(branchID)}`),
            'Failed to read the offline settings',
        );
        return response.json();
    },

    async savePolicy(branchID: string, input: OfflinePolicyInput): Promise<OfflinePolicy> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/offline/policy`, {
                method: 'PUT',
                headers: { ...JSON_HEADERS, 'Idempotency-Key': crypto.randomUUID() },
                body: JSON.stringify({ branch_id: branchID, ...input }),
            }),
            'Failed to save the offline settings',
        );
        return response.json();
    },

    /** Back to the dealer wide defaults, or off where there are none. */
    async clearPolicy(branchID: string): Promise<OfflinePolicy> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/offline/policy`, {
                method: 'PUT',
                headers: { ...JSON_HEADERS, 'Idempotency-Key': crypto.randomUUID() },
                body: JSON.stringify({ branch_id: branchID, clear: true }),
            }),
            'Failed to clear the offline settings',
        );
        return response.json();
    },

    /** The branch's open and held exceptions, every page (a manager settles them all). */
    async exceptions(branchID: string): Promise<OfflineException[]> {
        const all: OfflineException[] = [];
        let cursor = '';
        for (;;) {
            const url = `${API_URL}/api/v1/pos/offline/exceptions?branch_id=${encodeURIComponent(branchID)}&status=open`
                + (cursor ? `&cursor=${encodeURIComponent(cursor)}` : '');
            const response = await expectOk(await fetchWithAuth(url), 'Failed to load the offline exceptions');
            const page = (await response.json()) as { items?: OfflineException[]; next_cursor?: string | null };
            if (Array.isArray(page.items)) all.push(...page.items);
            if (!page.next_cursor) return all;
            cursor = page.next_cursor;
        }
    },

    /** The row's revision rides the body: a second manager's settle of the same row is refused. */
    async resolveException(item: OfflineException, action: ExceptionResolution, reason: string): Promise<OfflineException> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/offline/exceptions/${encodeURIComponent(item.id)}/resolve`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'Idempotency-Key': crypto.randomUUID() },
                body: JSON.stringify({ action, reason, revision: item.revision }),
            }),
            'Failed to resolve the exception',
        );
        return response.json();
    },

    /** The yard's registers, each with its device: there is no list route, so one read per register. */
    async devices(branchID: string): Promise<RegisterDevice[]> {
        const registers = (await posService.listRegisters()).filter((r) => r.branch_id === branchID);
        return Promise.all(registers.map(async (r) => {
            const response = await expectOk(
                await fetchWithAuth(`${API_URL}/api/v1/pos/offline/registers/${encodeURIComponent(r.id)}/device`),
                'Failed to load the registers',
            );
            return foldDevice((await response.json()) as WireEnrolment);
        }));
    },

    /** The public key comes from the register's own shell keychain; the epoch is the register's next_epoch. */
    async enrolDevice(registerID: string, epoch: number, publicKey: string, proof: ManagerProof): Promise<RegisterDevice> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/offline/registers/${encodeURIComponent(registerID)}/device`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'Idempotency-Key': crypto.randomUUID() },
                body: JSON.stringify({ device_epoch: epoch, public_key: publicKey, ...proof }),
            }),
            'Failed to enrol the register',
        );
        return foldDevice((await response.json()) as WireEnrolment);
    },

    async revokeDevice(registerID: string, epoch: number, reason: string, proof: ManagerProof): Promise<RegisterDevice> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/offline/registers/${encodeURIComponent(registerID)}/device/revoke`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'Idempotency-Key': crypto.randomUUID() },
                body: JSON.stringify({ device_epoch: epoch, reason, ...proof }),
            }),
            'Failed to revoke the register',
        );
        return foldDevice((await response.json()) as WireEnrolment);
    },
};

/**
 * SEAM C1: the register's own shell holds the device key and hands the page
 * its public half only. Until C1's command exists a browser has none, and the
 * enrolment panel says to enrol from the register's work shell.
 */
export async function shellDevicePublicKey(): Promise<string | null> {
    return null;
}
