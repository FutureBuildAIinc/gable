// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { fetchWithAuth } from './fetchClient';
import { parseApiError } from './apiError';

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

/**
 * One offline exception (ADR 0011 section 3.4). SEAM O4: the list and the
 * resolve routes are built on refactor/cp-offline-o4; this is their contract
 * as the ADR states it, faked in the screens' tests until that branch lands.
 */
export interface OfflineException {
    id: string;
    sale_id: string;
    register_id: string;
    epoch: number;
    sequence: number;
    reason_code: string;
    message: string;
    status: 'open' | 'held' | 'resolved';
    session_id: string | null;
    business_date: string;
    total_cents: number;
    tendered_by_method: Record<string, number>;
    offline_reference: string;
    created_at: string;
}

export type ExceptionResolution =
    | 'retry' | 'accept' | 'restate_stock' | 'late_post' | 'write_off' | 'separate_sale' | 'settled_replaced';

/**
 * One register's device (ADR 0011 section 6.1). SEAM O2: the device routes are
 * built on refactor/cp-offline-o2; status none is a register never enrolled.
 */
export interface RegisterDevice {
    register_id: string;
    epoch: number;
    status: 'active' | 'revoked' | 'none';
    enrolled_by: string | null;
    enrolled_at: string | null;
    revoked_at: string | null;
    synced_high_sequence: number;
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

    /** SEAM O4. */
    async exceptions(branchID: string): Promise<OfflineException[]> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/offline/exceptions?branch_id=${encodeURIComponent(branchID)}&status=open`),
            'Failed to load the offline exceptions',
        );
        const page = (await response.json()) as { items?: OfflineException[] };
        return Array.isArray(page.items) ? page.items : [];
    },

    /** SEAM O4. */
    async resolveException(id: string, resolution: ExceptionResolution, reason: string): Promise<OfflineException> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/offline/exceptions/${id}/resolve`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'Idempotency-Key': crypto.randomUUID() },
                body: JSON.stringify({ resolution, reason }),
            }),
            'Failed to resolve the exception',
        );
        return response.json();
    },

    /** SEAM O2. */
    async devices(branchID: string): Promise<RegisterDevice[]> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/offline/devices?branch_id=${encodeURIComponent(branchID)}`),
            'Failed to load the registers',
        );
        const page = (await response.json()) as { items?: RegisterDevice[] };
        return Array.isArray(page.items) ? page.items : [];
    },

    /** SEAM O2: the public key comes from the register's own shell keychain. */
    async enrolDevice(registerID: string, publicKey: string, proof: ManagerProof): Promise<RegisterDevice> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/offline/devices`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'Idempotency-Key': crypto.randomUUID() },
                body: JSON.stringify({ register_id: registerID, public_key: publicKey, ...proof }),
            }),
            'Failed to enrol the register',
        );
        return response.json();
    },

    /** SEAM O2. */
    async revokeDevice(registerID: string, proof: ManagerProof): Promise<RegisterDevice> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/offline/devices/${encodeURIComponent(registerID)}/revoke`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'Idempotency-Key': crypto.randomUUID() },
                body: JSON.stringify(proof),
            }),
            'Failed to revoke the register',
        );
        return response.json();
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
