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
    last_used_at?: string;
    revoked_at?: string;
}

export interface CreateKeyResponse {
    api_key: string;
    key: APIKey;
}

export interface AISettings {
    configured: boolean;
    source: 'admin' | 'env' | 'none';
    key_hint?: string;
    base_url?: string;
}

export interface RoutingSettings {
    configured: boolean;
    source: 'admin' | 'env' | 'none';
    key_hint?: string;
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
    staff_no?: string;
    role: string;
    active: boolean;
    created_at: string;
    updated_at: string;
    modules: string[];
}

/** Global state of an integration module — the kill switch, not a grant. */
export interface ModuleInfo {
    id: string;
    name: string;
    enabled: boolean;
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

export const techAdminService = {
    async listKeys(): Promise<APIKey[]> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/keys`);
        if (!response.ok) {
            throw new Error('Failed to fetch API keys');
        }
        const data = await response.json();
        return data || [];
    },

    async createKey(name: string, scopes: string[]): Promise<CreateKeyResponse> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/keys`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({ name, scopes }),
        });
        if (!response.ok) {
            throw new Error('Failed to create API key');
        }
        return response.json();
    },

    async revokeKey(id: string): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/keys/${id}`, {
            method: 'DELETE',
        });
        if (!response.ok) {
            throw new Error('Failed to revoke API key');
        }
    },

    // --- AI Settings ---

    async getAISettings(): Promise<AISettings> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/ai`);
        if (!response.ok) throw new Error('Failed to fetch AI settings');
        return response.json();
    },

    async saveAIKey(apiKey: string, baseUrl?: string): Promise<void> {
        const body: { api_key: string; base_url?: string } = { api_key: apiKey };
        if (baseUrl !== undefined) body.base_url = baseUrl;
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/ai`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
        });
        if (!response.ok) {
            const text = await response.text();
            throw new Error(text || 'Failed to save API key');
        }
    },

    async deleteAIKey(): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/ai`, {
            method: 'DELETE',
        });
        if (!response.ok) throw new Error('Failed to delete API key');
    },

    // --- Routing (OpenRouteService) Settings ---

    async getRoutingSettings(): Promise<RoutingSettings> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/routing`);
        if (!response.ok) throw new Error('Failed to fetch routing settings');
        return response.json();
    },

    async saveORSKey(apiKey: string): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/routing`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ api_key: apiKey }),
        });
        if (!response.ok) {
            const text = await response.text();
            throw new Error(text || 'Failed to save routing API key');
        }
    },

    async deleteORSKey(): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/settings/routing`, {
            method: 'DELETE',
        });
        if (!response.ok) throw new Error('Failed to delete routing API key');
    },

    // --- Staff Management & Module Access ---
    //
    // These five calls are the write side of AI_LM's login path: the roster and
    // grants they edit are exactly what POST /api/integration/validate-staff
    // reads. Entitlement there is active AND granted AND globally enabled.

    async listStaff(): Promise<StaffMember[]> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/staff`);
        if (!response.ok) throw new Error('Failed to fetch staff');
        const data = await response.json();
        return data || [];
    },

    async listModules(): Promise<ModuleInfo[]> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/modules`);
        if (!response.ok) throw new Error('Failed to fetch modules');
        const data = await response.json();
        return data || [];
    },

    async setModuleEnabled(moduleId: string, enabled: boolean): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/modules/${moduleId}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ enabled }),
        });
        if (!response.ok) throw new Error('Failed to update module');
    },

    async grantModule(staffId: string, moduleId: string): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/staff/${staffId}/modules`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ module_id: moduleId }),
        });
        if (!response.ok) throw new Error('Failed to grant module access');
    },

    async revokeModule(staffId: string, moduleId: string): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/staff/${staffId}/modules/${moduleId}`, {
            method: 'DELETE',
        });
        if (!response.ok) throw new Error('Failed to revoke module access');
    },

    /**
     * Readiness probe. Deliberately NOT under /api: the backend serves
     * /healthz/ready at the root and the deploy spec (.do/app-*.yaml) routes
     * /healthz to the backend with preserve_path_prefix, so it is same-origin
     * in a real deployment; app/vite.config.ts forwards it in dev.
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
