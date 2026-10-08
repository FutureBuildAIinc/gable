// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The location and branch wire (ADR 0001, ADR 0007): cursor lists, a lowercase type, optional
 * fields present as null, a quoted If-Match on every PUT and DELETE, and the one error envelope.
 * Every failure of those routes is thrown as an ApiError. The user grant routes
 * (/me/branches, /users/...) are unchanged bare arrays.
 */
import type {
    BranchSummary,
    CreateLocationRequest,
    Location,
    LocationPage,
    LocationUpdate,
    UserLocation,
} from '../types/location';
import { fetchWithAuth } from './fetchClient';
import { ifMatch, parseApiError } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

export interface ListLocationsParams {
    limit?: number;
    cursor?: string | null;
    /** Ask for the total row count (include=total). */
    includeTotal?: boolean;
    /** Branches only: true includes archived branches. */
    includeInactive?: boolean;
}

/** The query string of a location or branch list; only these parameters are accepted (anything else is a 400). */
export function buildLocationQuery(params: ListLocationsParams = {}): string {
    const q = new URLSearchParams();
    if (params.includeInactive) q.set('include_inactive', 'true');
    if (params.limit) q.set('limit', String(params.limit));
    if (params.cursor) q.set('cursor', params.cursor);
    if (params.includeTotal) q.set('include', 'total');
    const s = q.toString();
    return s ? `?${s}` : '';
}

/** The most rows listAll collects (pages of 200), so a huge site never hangs a picker. */
export const LIST_ALL_LOCATIONS_CAP = 2000;

/**
 * The whole row of a loaded location as a PUT body. The update replaces every optional field, so
 * a field left out would be cleared; type and parent_id are fixed at create and never sent.
 * The revision travels in If-Match, not here.
 */
export function locationUpdateFromLocation(loc: Location): LocationUpdate {
    return {
        path: loc.path,
        code: loc.code,
        description: loc.description,
        name: loc.name,
        address: loc.address,
        city: loc.city,
        state: loc.state,
        zip: loc.zip,
        phone: loc.phone,
        tax_jurisdiction_code: loc.tax_jurisdiction_code,
        default_tax_rate: loc.default_tax_rate,
        timezone: loc.timezone,
        active: loc.active,
    };
}

const JSON_HEADERS = { 'Content-Type': 'application/json' };

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (!response.ok) throw await parseApiError(response, fallback);
    return response;
}

async function collectAll(fetchPage: (cursor: string | null) => Promise<LocationPage>): Promise<Location[]> {
    const all: Location[] = [];
    let cursor: string | null = null;
    while (all.length < LIST_ALL_LOCATIONS_CAP) {
        const page: LocationPage = await fetchPage(cursor);
        all.push(...page.items);
        if (!page.next_cursor) break;
        cursor = page.next_cursor;
    }
    return all.slice(0, LIST_ALL_LOCATIONS_CAP);
}

export const LocationService = {
    // -------- Locations (physical sub-locations under a branch) ----------

    async listLocations(params: Omit<ListLocationsParams, 'includeInactive'> = {}): Promise<LocationPage> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/locations${buildLocationQuery(params)}`),
            'Failed to fetch locations',
        );
        return response.json();
    },

    /** Pages by cursor until the last page or LIST_ALL_LOCATIONS_CAP rows, for the location pickers. */
    async listAllLocations(): Promise<Location[]> {
        return collectAll(cursor => LocationService.listLocations({ limit: 200, cursor }));
    },

    async createLocation(data: CreateLocationRequest): Promise<Location> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/locations`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(data),
            }),
            'Failed to create location',
        );
        return response.json();
    },

    /** Replaces the mutable fields on the revision the page loaded; the body may not carry type or parent_id. */
    async updateLocation(id: string, data: LocationUpdate, revision: number): Promise<Location> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/locations/${id}`, {
                method: 'PUT',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify(data),
            }),
            'Failed to update location',
        );
        return response.json();
    },

    /** Soft archive (active false) on the revision the page loaded. */
    async deleteLocation(id: string, revision: number): Promise<void> {
        await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/locations/${id}`, {
                method: 'DELETE',
                headers: { 'If-Match': ifMatch(revision) },
            }),
            'Failed to archive location',
        );
    },

    // -------- Branches (top-level locations: type 'branch') --------------

    async listBranches(params: ListLocationsParams = {}): Promise<LocationPage> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/branches${buildLocationQuery(params)}`),
            'Failed to fetch branches',
        );
        return response.json();
    },

    /** Pages by cursor until the last page or LIST_ALL_LOCATIONS_CAP branches. */
    async listAllBranches(includeInactive = false): Promise<Location[]> {
        return collectAll(cursor => LocationService.listBranches({ limit: 200, cursor, includeInactive }));
    },

    async getBranch(id: string): Promise<Location> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/branches/${id}`),
            'Failed to fetch branch',
        );
        return response.json();
    },

    /** The branch route forces the type; a branch is root level and needs a name (the path defaults to it). */
    async createBranch(data: Omit<CreateLocationRequest, 'type'>): Promise<Location> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/branches`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(data),
            }),
            'Failed to create branch',
        );
        return response.json();
    },

    async updateBranch(id: string, data: LocationUpdate, revision: number): Promise<Location> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/branches/${id}`, {
                method: 'PUT',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify(data),
            }),
            'Failed to update branch',
        );
        return response.json();
    },

    async archiveBranch(id: string, revision: number): Promise<void> {
        await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/branches/${id}`, {
                method: 'DELETE',
                headers: { 'If-Match': ifMatch(revision) },
            }),
            'Failed to archive branch',
        );
    },

    /** The branch's rows ordered by path; the envelope answers everything on one page. */
    async getBranchTree(id: string): Promise<Location[]> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/branches/${id}/tree`),
            'Failed to fetch branch tree',
        );
        const page: LocationPage = await response.json();
        return page.items;
    },

    // -------- User <-> branch grants -------------------------------------

    /**
     * Returns the active branches granted to the current JWT subject. Used
     * to populate the global branch switcher.
     */
    async getMyBranches(): Promise<BranchSummary[]> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/me/branches`);
        if (!response.ok) {
            throw new Error('Failed to fetch user branches');
        }
        const data = await response.json();
        return Array.isArray(data) ? data : [];
    },

    async listUserBranches(userSub: string): Promise<BranchSummary[]> {
        const response = await fetchWithAuth(
            `${API_URL}/api/v1/users/${encodeURIComponent(userSub)}/branches`,
        );
        if (!response.ok) {
            throw new Error('Failed to fetch user branches');
        }
        const data = await response.json();
        return Array.isArray(data) ? data : [];
    },

    async grantUserBranch(
        userSub: string,
        branchId: string,
        isHome = false,
    ): Promise<UserLocation> {
        const response = await fetchWithAuth(
            `${API_URL}/api/v1/users/${encodeURIComponent(userSub)}/branches`,
            {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ branch_id: branchId, is_home: isHome }),
            },
        );
        if (!response.ok) {
            throw new Error('Failed to grant user branch');
        }
        return response.json();
    },

    async revokeUserBranch(userSub: string, branchId: string): Promise<void> {
        const response = await fetchWithAuth(
            `${API_URL}/api/v1/users/${encodeURIComponent(userSub)}/branches/${branchId}`,
            { method: 'DELETE' },
        );
        if (!response.ok) {
            throw new Error('Failed to revoke user branch');
        }
    },

    async listKnownUsers(): Promise<string[]> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/users`);
        if (!response.ok) {
            throw new Error('Failed to fetch users');
        }
        const data = await response.json();
        return Array.isArray(data) ? data : [];
    },

    async listBranchUsers(branchId: string): Promise<UserLocation[]> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/branches/${branchId}/users`);
        if (!response.ok) {
            throw new Error('Failed to fetch branch users');
        }
        const data = await response.json();
        return Array.isArray(data) ? data : [];
    },

    async setHomeBranch(userSub: string, branchId: string): Promise<void> {
        const response = await fetchWithAuth(
            `${API_URL}/api/v1/users/${encodeURIComponent(userSub)}/home-branch`,
            {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ branch_id: branchId }),
            },
        );
        if (!response.ok) {
            throw new Error('Failed to set home branch');
        }
    },
};
