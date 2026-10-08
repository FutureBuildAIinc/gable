// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The wire error envelope of ADR 0001 and the quoted If-Match value, shared by every
// service whose routes are on the converted contract.

export interface ApiErrorDetail {
    field?: string;
    message: string;
    code?: string;
}

/**
 * Every non 2xx answer of the converted routes (quotes, customers) is the one error envelope of ADR 0001:
 * { error: { code, message, details? }, meta: { request_id } }. This carries it.
 */
export class ApiError extends Error {
    readonly status: number;
    readonly code: string;
    readonly details: ApiErrorDetail[];
    readonly requestId: string | undefined;

    constructor(status: number, code: string, message: string, details: ApiErrorDetail[] = [], requestId?: string) {
        super(message);
        this.name = 'ApiError';
        this.status = status;
        this.code = code;
        this.details = details;
        this.requestId = requestId;
    }

    get isStaleRevision(): boolean {
        return this.code === 'stale_revision';
    }

    /** The message, with each field problem or blocker listed on its own line for validation_failed and invalid_state_transition. */
    get displayMessage(): string {
        if ((this.code === 'validation_failed' || this.code === 'invalid_state_transition') && this.details.length > 0) {
            const lines = this.details.map(d => (d.field ? `${d.field}: ${d.message}` : d.message));
            return `${this.message}\n${lines.join('\n')}`;
        }
        return this.message;
    }
}

/** Text for a toast or inline notice from any thrown value. */
export function apiErrorMessage(err: unknown, fallback = 'Something went wrong'): string {
    if (err instanceof ApiError) return err.displayMessage;
    if (err instanceof Error && err.message) return err.message;
    return fallback;
}

/** Parses the error envelope out of a failed response; tolerates a non-JSON body. */
export async function parseApiError(response: Response, fallback: string): Promise<ApiError> {
    let body: unknown = null;
    try {
        body = await response.json();
    } catch {
        body = null;
    }
    const env = (body && typeof body === 'object' ? body : {}) as {
        error?: { code?: unknown; message?: unknown; details?: unknown };
        meta?: { request_id?: unknown };
    };
    const err = env.error;
    const code = typeof err?.code === 'string' ? err.code : 'unknown_error';
    const message = typeof err?.message === 'string' && err.message ? err.message : fallback;
    const details: ApiErrorDetail[] = Array.isArray(err?.details)
        ? (err.details as unknown[])
              .filter((d): d is Record<string, unknown> => !!d && typeof d === 'object')
              .map(d => ({
                  field: typeof d.field === 'string' ? d.field : undefined,
                  message: typeof d.message === 'string' ? d.message : '',
                  code: typeof d.code === 'string' ? d.code : undefined,
              }))
        : [];
    const requestId = typeof env.meta?.request_id === 'string' ? env.meta.request_id : undefined;
    return new ApiError(response.status, code, message, details, requestId);
}

/** The If-Match value for a revision: quoted, as an ETag is. */
export function ifMatch(revision: number): string {
    return `"${revision}"`;
}

/**
 * The problem text per field of a 400 validation_failed, for showing beside the inputs.
 * Several problems on one field are joined; an error without field details gives an empty map.
 */
export function fieldErrorMap(err: unknown): Record<string, string> {
    const out: Record<string, string> = {};
    if (!(err instanceof ApiError)) return out;
    for (const d of err.details) {
        if (!d.field) continue;
        out[d.field] = out[d.field] ? `${out[d.field]}; ${d.message}` : d.message;
    }
    return out;
}
