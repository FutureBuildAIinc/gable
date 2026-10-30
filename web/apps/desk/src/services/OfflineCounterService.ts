// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's door to the work shell's offline core (ADR 0011 section 5):
// the named Tauri commands behind the shell's origin fence, nothing else. The
// page asks and displays: the core prices, signs, keeps the queue, fetches
// its own snapshot and runs the sync with the keychain's token. A browser tab
// has no core, so the counter there sells online only and says so.

import { authMethodsConfig, resetAuthMethodsCache } from '@gable/auth';
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

const SERVER_UNREACHABLE = 'The server cannot be reached: the register keeps its sales and sends them when the connection returns.';

/** The words for each fixed code the shell's password sign in answers. */
const PASSWORD_WORDS: Record<string, string> = {
    sign_in_refused: 'That email and password did not sign this register in.',
    sign_in_limited: 'Too many tries. Wait a little, then try again.',
    sign_in_not_allowed: 'This sign in is not allowed for this register. Ask a manager.',
    sign_in_unavailable: SERVER_UNREACHABLE,
    no_server: 'This register has no server set: it has no server to sign in to. Ask a manager.',
};

/** Maps a raw shell answer to a coded error whose words come from the table above, never from the answer. */
function passwordSignInError(raw: unknown): OfflineCounterError {
    const code = typeof raw === 'string' ? raw : raw && typeof raw === 'object' && 'code' in raw ? String((raw as { code: unknown }).code) : '';
    if (code in PASSWORD_WORDS) return new OfflineCounterError({ code, message: PASSWORD_WORDS[code] });
    return new OfflineCounterError({ code: 'sign_in_failed', message: 'The register could not be signed in. Ask a manager.' });
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
    /**
     * The shell's own sign in, which puts the token in the keychain the core
     * reads (a web sign in writes only the cookie). The dealer's first method
     * picks the door: email and password answers 'form' and starts nothing,
     * the strip then shows its own form; otherwise the first redirect method
     * starts, as the shell starts redirect methods only. A degraded answer
     * means the server did not answer: the register says so and forgets it,
     * so the next tap asks the server again.
     */
    async signIn(): Promise<'form' | 'started'> {
        const config = await authMethodsConfig(import.meta.env.VITE_API_URL || '');
        if (config.degraded) {
            resetAuthMethodsCache();
            throw new OfflineCounterError({ code: 'server_unreachable', message: SERVER_UNREACHABLE });
        }
        if (config.methods[0]?.method === 'email_password') return 'form';
        const method = config.methods.find((m) => m.kind === 'redirect');
        if (!method) throw new OfflineCounterError({ code: 'no_sign_in_method', message: 'This dealer allows no sign in method a register can start. Ask a manager.' });
        await call('begin_sign_in', { method: method.method });
        return 'started';
    },
    /**
     * The password sign in (CP-13): one call into the shell, which answers
     * nothing or a fixed code. The password is a parameter of this call
     * only; the shell's answers are bare code strings, mapped here to words
     * that never carry what the clerk typed.
     */
    async signInWithPassword(email: string, password: string): Promise<void> {
        const invoke = invoker();
        if (!invoke) throw new OfflineCounterError({ code: 'no_core', message: 'This counter runs in a browser tab: it sells online only.' });
        try {
            await invoke('sign_in_with_password', { email, password });
        } catch (raw) {
            throw passwordSignInError(raw);
        }
    },
};
