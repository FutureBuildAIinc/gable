// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { fetchWithAuth } from './fetchClient';

const API_URL = import.meta.env.VITE_API_URL || '';

export interface APIKey {
    id: string;
    name: string;
    prefix: string;
    scopes: string[];
    created_at: string;
    last_used_at: string | null;
    revoked_at: string | null;
}

export interface CreateKeyResponse {
    api_key: string;
    key: APIKey;
}

interface ListPage<T> {
    items: T[];
    next_cursor: string | null;
    limit: number;
    total?: number;
}

export interface AISettings {
    configured: boolean;
    source: 'admin' | 'env' | 'none';
    key_hint: string | null;
    base_url: string | null;
    revision: number;
}

export interface RoutingSettings {
    configured: boolean;
    source: 'admin' | 'env' | 'none';
    key_hint: string | null;
    revision: number;
}

/**
 * A row of the dealer staff roster (`staff`). This is NOT an ERP user — it is
 * the identity AI_LM authenticates against via POST /api/integration/validate-staff.
 *
 * `modules` is the raw set of granted module ids, deliberately NOT filtered by
 * the global `modules.<id>.enabled` flag: the grant checkbox must keep showing
 * what was granted even while the module is switched off globally, otherwise
 * flipping the kill switch would look like it had wiped every grant.
 */
export interface StaffMember {
    id: string;
    email: string;
    full_name: string;
    staff_no: string | null;
    role: string;
    active: boolean;
    revision: number;
    created_at: string;
    updated_at: string;
    modules: string[];
}

/** Global state of an integration module — the kill switch, not a grant. */
export interface ModuleInfo {
    id: string;
    name: string;
    enabled: boolean;
    revision: number;
}

/**
 * The body of `GET /healthz/ready` (backend cmd/server/main.go). The endpoint
 * answers 200 with status "ok" when the database pool pings, and 503 with
 * status "degraded" when it does not — so a non-2xx response is still a
 * meaningful health report and must be parsed, not thrown away.
 */
export interface ReadinessCheck {
    status: string;
    pool_total?: number;
    pool_idle?: number;
    pool_in_use?: number;
    pool_max?: number;
}

export interface Readiness {
    status: string;
    uptime: string;
    checks: Record<string, ReadinessCheck>;
}

/** The error envelope of ADR 0001: code, message, field details. */
interface WireErrorBody {
    error?: { code?: string; message?: string; details?: { field?: string; message?: string }[] };
}

async function readError(response: Response): Promise<string> {
    try {
        const body = (await response.json()) as WireErrorBody;
        const parts = (body.error?.details ?? []).map((d) => (d.field ? `${d.field}: ${d.message}` : d.message));
        return [body.error?.message ?? `Request failed (${response.status})`, ...parts].join('; ');
    } catch {
        return `Request failed (${response.status})`;
    }
}

async function failOn(response: Response): Promise<void> {
    if (!response.ok) throw new Error(await readError(response));
}

/** Walks a cursor list to exhaustion; the admin surfaces are small. */
async function walkList<T>(path: string): Promise<T[]> {
    const out: T[] = [];
    let cursor = '';
    for (let page = 0; page < 50; page++) {
        const url = `${API_URL}${path}${path.includes('?') ? '&' : '?'}limit=200${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`;
        const response = await fetchWithAuth(url);
        if (!response.ok) throw new Error(await readError(response));
        const data = (await response.json()) as ListPage<T>;
        out.push(...data.items);
        if (!data.next_cursor) break;
        cursor = data.next_cursor;
    }
    return out;
}

export const techAdminService = {
    async listKeys(): Promise<APIKey[]> {
        return walkList<APIKey>('/api/v1/admin/keys');
    },

    async createKey(name: string, scopes: string[]): Promise<CreateKeyResponse> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/keys`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({ name, scopes }),
        });
        await failOn(response);
        return response.json();
    },

    async revokeKey(id: string): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/keys/${id}`, {
            method: 'DELETE',
        });
        await failOn(response);
    },

    // --- AI Settings ---
    //
    // The settings are documents on a revision (ADR 0001 section 11): every
    // save and delete sends the revision it read as its If-Match, and one
    // stale retry re-reads and tries again (a second editor saved first).

    async getAISettings(): Promise<AISettings> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/ai`);
        await failOn(response);
        return response.json();
    },

    async saveAIKey(apiKey: string, baseUrl?: string): Promise<void> {
        const body: { api_key: string; base_url?: string } = { api_key: apiKey };
        if (baseUrl !== undefined) body.base_url = baseUrl;
        for (let attempt = 0; attempt < 2; attempt++) {
            const current = await this.getAISettings();
            const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/ai`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json', 'If-Match': `"${current.revision}"` },
                body: JSON.stringify(body),
            });
            if (response.status === 409 && attempt === 0) continue;
            await failOn(response);
            return;
        }
    },

    async deleteAIKey(): Promise<void> {
        for (let attempt = 0; attempt < 2; attempt++) {
            const current = await this.getAISettings();
            const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/ai`, {
                method: 'DELETE',
                headers: { 'If-Match': `"${current.revision}"` },
            });
            if (response.status === 409 && attempt === 0) continue;
            await failOn(response);
            return;
        }
    },

    // --- Routing (OpenRouteService) Settings ---

    async getRoutingSettings(): Promise<RoutingSettings> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/routing`);
        await failOn(response);
        return response.json();
    },

    async saveORSKey(apiKey: string): Promise<void> {
        for (let attempt = 0; attempt < 2; attempt++) {
            const current = await this.getRoutingSettings();
            const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/routing`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json', 'If-Match': `"${current.revision}"` },
                body: JSON.stringify({ api_key: apiKey }),
            });
            if (response.status === 409 && attempt === 0) continue;
            await failOn(response);
            return;
        }
    },

    async deleteORSKey(): Promise<void> {
        for (let attempt = 0; attempt < 2; attempt++) {
            const current = await this.getRoutingSettings();
            const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/routing`, {
                method: 'DELETE',
                headers: { 'If-Match': `"${current.revision}"` },
            });
            if (response.status === 409 && attempt === 0) continue;
            await failOn(response);
            return;
        }
    },

    // --- Staff Management & Module Access ---
    //
    // These calls are the write side of AI_LM's login path: the roster and
    // grants they edit are exactly what POST /api/integration/validate-staff
    // reads. Entitlement there is active AND granted AND globally enabled.

    async listStaff(): Promise<StaffMember[]> {
        return walkList<StaffMember>('/api/v1/admin/staff');
    },

    async listModules(): Promise<ModuleInfo[]> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/modules`);
        if (!response.ok) throw new Error('Failed to fetch modules');
        const data = (await response.json()) as ListPage<ModuleInfo>;
        return data.items;
    },

    async setModuleEnabled(moduleId: string, enabled: boolean): Promise<void> {
        for (let attempt = 0; attempt < 2; attempt++) {
            try {
                const modules = await this.listModules();
                const target = modules.find((m) => m.id === moduleId);
                if (!target) throw new Error('Failed to update module');
                const response = await fetchWithAuth(`${API_URL}/api/v1/admin/modules/${moduleId}`, {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json', 'If-Match': `"${target.revision}"` },
                    body: JSON.stringify({ enabled }),
                });
                if (response.status === 409 && attempt === 0) continue;
                await failOn(response);
                return;
            } catch (err) {
                throw new Error(`Failed to update module${err instanceof Error && err.message ? `: ${err.message}` : ''}`);
            }
        }
    },

    async grantModule(staffId: string, moduleId: string): Promise<void> {
        for (let attempt = 0; attempt < 2; attempt++) {
            const member = await this.getStaff(staffId);
            const response = await fetchWithAuth(`${API_URL}/api/v1/admin/staff/${staffId}/modules`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', 'If-Match': `"${member.revision}"` },
                body: JSON.stringify({ module_id: moduleId }),
            });
            if (response.status === 409 && attempt === 0) continue;
            if (!response.ok) throw new Error(`Failed to grant module access: ${await readError(response)}`);
            return;
        }
    },

    async revokeModule(staffId: string, moduleId: string): Promise<void> {
        for (let attempt = 0; attempt < 2; attempt++) {
            const member = await this.getStaff(staffId);
            const response = await fetchWithAuth(`${API_URL}/api/v1/admin/staff/${staffId}/modules/${moduleId}`, {
                method: 'DELETE',
                headers: { 'If-Match': `"${member.revision}"` },
            });
            if (response.status === 409 && attempt === 0) continue;
            if (!response.ok) throw new Error(`Failed to revoke module access: ${await readError(response)}`);
            return;
        }
    },

    async getStaff(id: string): Promise<StaffMember> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/staff/${id}`);
        await failOn(response);
        return response.json();
    },

    /**
     * Readiness probe. Deliberately NOT under /api: the backend serves
     * /healthz/ready at the root and the deploy spec (.do/app-*.yaml) routes
     * /healthz to the backend with preserve_path_prefix, so it is same-origin
     * in a real deployment; web/apps/desk/vite.config.ts forwards it in dev.
     *
     * A 503 is a health *report* ("degraded"), not a transport failure, so the
     * body is parsed on any status that carries JSON. Plain `fetch` rather than
     * fetchWithAuth: the endpoint is public (main.go PublicPaths) and a 401
     * interceptor firing off a health poll would be wrong.
     */
    async getReadiness(): Promise<Readiness> {
        const response = await fetch(`${API_URL}/healthz/ready`, {
            headers: { Accept: 'application/json' },
        });
        let body: unknown;
        try {
            body = await response.json();
        } catch {
            throw new Error(
                `Readiness endpoint returned ${response.status} with a non-JSON body`,
            );
        }
        const readiness = body as Partial<Readiness>;
        if (typeof readiness?.status !== 'string') {
            throw new Error(`Readiness endpoint returned an unrecognised body`);
        }
        return { checks: {}, uptime: '', ...readiness } as Readiness;
    },
};
// --- EDI Trading Partner Types & Service ---

export interface EDITradingPartner {
    id: string;
    name: string;
    isa_sender_id: string;
    isa_sender_qualifier: string;
    isa_receiver_id: string;
    isa_receiver_qualifier: string;
    gs_sender_id: string;
    gs_receiver_id: string;
    edi_version: string;
    transport_type: string;
    transport_config: string;
    supported_documents: string[];
    is_active: boolean;
    notes: string;
    created_at: string;
    updated_at: string;
}

export const ediService = {
    async listPartners(): Promise<EDITradingPartner[]> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/edi/partners`);
        if (!response.ok) throw new Error('Failed to fetch EDI partners');
        return response.json();
    },

    async deletePartner(id: string): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/edi/partners/${id}`, { method: 'DELETE' });
        if (!response.ok) throw new Error('Failed to delete EDI partner');
    },

    async togglePartner(partner: EDITradingPartner): Promise<EDITradingPartner> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/edi/partners/${partner.id}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ...partner, is_active: !partner.is_active }),
        });
        if (!response.ok) throw new Error('Failed to update EDI partner');
        return response.json();
    },
};
