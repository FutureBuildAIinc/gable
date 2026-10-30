// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's door to the work shell's offline core (ADR 0011 section 5):
// the named Tauri commands behind the shell's origin fence, nothing else. The
// page asks and displays: the core prices, signs, keeps the queue, fetches
// its own snapshot and runs the sync with the keychain's token. A browser tab
// has no core, so the counter there sells online only and says so.

import type {
    OfflineCartPreview,
    OfflineEnrolment,
    OfflineError,
    OfflineQueue,
    OfflineSaleRequest,
    OfflineSignedSale,
    OfflineSnapshot,
    OfflineSnapshotRefresh,
    OfflineSyncReport,
} from '../types/offline';

type Invoke = (command: string, args?: Record<string, unknown>) => Promise<unknown>;

/** A refusal from the core, with its code and its own words. */
export class OfflineCounterError extends Error {
    readonly code: string;
    constructor(e: OfflineError) {
        super(e.message);
        this.code = e.code;
        this.name = 'OfflineCounterError';
    }
}

let override: Invoke | null | undefined;

/** Tests set the core's stand in here; null removes it. */
export function setOfflineInvoke(fn: Invoke | null | undefined) {
    override = fn;
}

function invoker(): Invoke | null {
    if (override !== undefined) return override;
    const w = globalThis as unknown as { __TAURI_INTERNALS__?: { invoke?: Invoke } };
    return typeof w.__TAURI_INTERNALS__?.invoke === 'function' ? w.__TAURI_INTERNALS__.invoke.bind(w.__TAURI_INTERNALS__) : null;
}

function asError(raw: unknown): OfflineCounterError {
    if (raw && typeof raw === 'object' && 'code' in raw && 'message' in raw) {
        const e = raw as OfflineError;
        return new OfflineCounterError({ code: String(e.code), message: String(e.message) });
    }
    return new OfflineCounterError({ code: 'core_unavailable', message: typeof raw === 'string' ? raw : 'The register\'s offline store did not answer.' });
}

async function call<T>(command: string, args?: Record<string, unknown>): Promise<T> {
    const invoke = invoker();
    if (!invoke) throw new OfflineCounterError({ code: 'no_core', message: 'This counter runs in a browser tab: it sells online only.' });
    try {
        return await invoke(command, args) as T;
    } catch (raw) {
        throw asError(raw);
    }
}

export const offlineCounter = {
    /** Whether the page runs in the work shell, where the core can sell offline. */
    available(): boolean {
        return invoker() !== null;
    },
    enrolment(): Promise<OfflineEnrolment> {
        return call('offline_enrolment_status');
    },
    snapshot(): Promise<OfflineSnapshot> {
        return call('load_offline_snapshot');
    },
    refreshSnapshot(): Promise<OfflineSnapshotRefresh> {
        return call('refresh_offline_snapshot');
    },
    price(request: OfflineSaleRequest): Promise<OfflineCartPreview> {
        return call('price_offline_cart', { request });
    },
    sign(request: OfflineSaleRequest): Promise<OfflineSignedSale> {
        return call('sign_and_queue_offline_sale', { request });
    },
    queue(): Promise<OfflineQueue> {
        return call('offline_queue_status');
    },
    sync(): Promise<OfflineSyncReport> {
        return call('sync_offline_queue');
    },
};
