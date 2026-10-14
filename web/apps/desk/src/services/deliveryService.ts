// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type {
    Vehicle, Driver, Route, Delivery, Page,
    CreateVehicleRequest, UpdateVehicleRequest,
    CreateDriverRequest, UpdateDriverRequest,
    CreateRouteRequest,
    AssignOrderRequest, AssignOrderResult, TransitionDeliveryRequest,
} from '../types/delivery';
import { parseApiError } from './apiError';
import { fetchWithAuth } from './fetchClient';

const API_BASE = import.meta.env.VITE_API_URL || '';

/** The most rows a cursor walk collects (pages of 200), so a broken cursor cannot loop forever. */
export const LIST_WALK_CAP = 2000;

async function call<T>(path: string, init: RequestInit = {}): Promise<{ res: Response; body: T }> {
    const res = await fetchWithAuth(`${API_BASE}${path}`, init);
    if (!res.ok) throw await parseApiError(res, 'The delivery request failed');
    const body = (res.status === 204 ? undefined : await res.json()) as T;
    return { res, body };
}

async function page<T>(path: string): Promise<Page<T>> {
    const { body } = await call<Page<T>>(path);
    return body;
}

/** Walks a list envelope to its last page, bounded by LIST_WALK_CAP rows. */
async function walk<T>(path: string): Promise<T[]> {
    const out: T[] = [];
    let cursor: string | undefined;
    while (out.length < LIST_WALK_CAP) {
        const base = API_BASE || window.location.origin;
        const url = new URL(path, base);
        url.searchParams.set('limit', '200');
        if (cursor) url.searchParams.set('cursor', cursor);
        const one = await page<T>(url.pathname + (url.search || ''));
        out.push(...one.items);
        if (!one.next_cursor) break;
        cursor = one.next_cursor;
    }
    return out.slice(0, LIST_WALK_CAP);
}

const jsonInit = (method: string, body: unknown, extra: Record<string, string> = {}): RequestInit => ({
    method,
    headers: { 'Content-Type': 'application/json', ...extra },
    body: JSON.stringify(body),
});

export const deliveryService = {
    // Fleet

    listVehicles: async (): Promise<Vehicle[]> => walk('/api/v1/delivery/vehicles'),

    createVehicle: async (req: CreateVehicleRequest): Promise<Vehicle> =>
        (await call<Vehicle>('/api/v1/delivery/vehicles', jsonInit('POST', req))).body,

    getVehicle: async (id: string): Promise<Vehicle> =>
        (await call<Vehicle>(`/api/v1/delivery/vehicles/${id}`)).body,

    /** The write sends the revision the edit form loaded; a stale one throws and the caller reloads.
     *  A fresh re-read here would defeat the revision the user was editing on. */
    updateVehicle: async (id: string, req: UpdateVehicleRequest, currentRevision: number): Promise<Vehicle> => {
        return (await call<Vehicle>(`/api/v1/delivery/vehicles/${id}`,
            jsonInit('PUT', { ...req, revision: currentRevision }))).body;
    },

    deleteVehicle: async (id: string, currentRevision: number): Promise<void> => {
        await call<void>(`/api/v1/delivery/vehicles/${id}`, {
            method: 'DELETE',
            headers: { 'If-Match': `"${currentRevision}"` },
        });
    },

    uploadVehiclePhoto: async (id: string, file: File): Promise<string> => {
        const form = new FormData();
        form.append('photo', file);
        const body = await call<Vehicle>(`/api/v1/delivery/vehicles/${id}/photo`, { method: 'POST', body: form });
        return body.body.photo_url ?? '';
    },

    // Drivers

    listDrivers: async (): Promise<Driver[]> => walk('/api/v1/delivery/drivers'),

    createDriver: async (req: CreateDriverRequest): Promise<Driver> =>
        (await call<Driver>('/api/v1/delivery/drivers', jsonInit('POST', req))).body,

    getDriver: async (id: string): Promise<Driver> =>
        (await call<Driver>(`/api/v1/delivery/drivers/${id}`)).body,

    updateDriver: async (id: string, req: UpdateDriverRequest, currentRevision: number): Promise<Driver> => {
        return (await call<Driver>(`/api/v1/delivery/drivers/${id}`,
            jsonInit('PUT', { ...req, revision: currentRevision }))).body;
    },

    deleteDriver: async (id: string, currentRevision: number): Promise<void> => {
        await call<void>(`/api/v1/delivery/drivers/${id}`, {
            method: 'DELETE',
            headers: { 'If-Match': `"${currentRevision}"` },
        });
    },

    uploadDriverPhoto: async (id: string, file: File): Promise<string> => {
        const form = new FormData();
        form.append('photo', file);
        const body = await call<Driver>(`/api/v1/delivery/drivers/${id}/photo`, { method: 'POST', body: form });
        return body.body.photo_url ?? '';
    },

    // Routes

    /** The board read: routes with their stops embedded, in one payload. */
    listRoutes: async (date?: string, driverId?: string, status?: string): Promise<Route[]> => {
        const params = new URLSearchParams();
        if (date) params.set('date', date);
        if (driverId) params.set('driver_id', driverId);
        if (status) params.set('status', status);
        params.set('include', 'stops');
        const q = params.toString();
        return walk(`/api/v1/delivery/routes${q ? `?${q}` : ''}`);
    },

    createRoute: async (req: CreateRouteRequest): Promise<Route> =>
        (await call<Route>('/api/v1/delivery/routes', jsonInit('POST', req))).body,

    getRoute: async (id: string): Promise<Route> =>
        (await call<Route>(`/api/v1/delivery/routes/${id}`)).body,

    /** A route transition (dispatch or complete) on the revision the caller loaded. */
    transitionRoute: async (id: string, to: 'in_transit' | 'completed', currentRevision: number): Promise<Route> => {
        return (await call<Route>(`/api/v1/delivery/routes/${id}/transitions`,
            jsonInit('POST', { to, revision: currentRevision }))).body;
    },

    dispatchRoute: async (id: string, currentRevision: number): Promise<Route> =>
        deliveryService.transitionRoute(id, 'in_transit', currentRevision),

    completeRoute: async (id: string, currentRevision: number): Promise<Route> =>
        deliveryService.transitionRoute(id, 'completed', currentRevision),

    reorderStops: async (routeId: string, orderedDeliveryIds: string[], currentRevision: number): Promise<Route> => {
        return (await call<Route>(`/api/v1/delivery/routes/${routeId}/reorder`,
            jsonInit('POST', { ordered_delivery_ids: orderedDeliveryIds },
                { 'If-Match': `"${currentRevision}"` }))).body;
    },

    optimizeRoute: async (routeId: string, currentRevision: number): Promise<Route> => {
        return (await call<Route>(`/api/v1/delivery/routes/${routeId}/optimize`,
            { method: 'POST', headers: { 'If-Match': `"${currentRevision}"` } })).body;
    },

    // Stops

    listDeliveries: async (routeId: string): Promise<Delivery[]> =>
        walk(`/api/v1/delivery/routes/${routeId}/deliveries`),

    getDelivery: async (id: string): Promise<Delivery> =>
        (await call<Delivery>(`/api/v1/delivery/deliveries/${id}`)).body,

    assignOrder: async (req: AssignOrderRequest): Promise<AssignOrderResult> =>
        (await call<AssignOrderResult>('/api/v1/delivery/deliveries', jsonInit('POST', req))).body,

    /** Completes a stop on the revision the caller loaded; a stale one throws and the caller reloads. */
    updateStatus: async (id: string, req: TransitionDeliveryRequest, currentRevision: number): Promise<Delivery> => {
        return (await call<Delivery>(`/api/v1/delivery/deliveries/${id}/transitions`,
            jsonInit('POST', { ...req, revision: currentRevision }))).body;
    },

    uploadPODPhoto: async (deliveryId: string, file: File, photoType: string = 'site'): Promise<{ id: string; photo_url: string }> => {
        const form = new FormData();
        form.append('photo', file);
        form.append('photo_type', photoType);
        return (await call<{ id: string; photo_url: string }>(
            `/api/v1/delivery/deliveries/${deliveryId}/pod-photo`, { method: 'POST', body: form })).body;
    },

    getPODPhotos: async (deliveryId: string): Promise<{ id: string; photo_url: string; photo_type: string; uploaded_at: string }[]> =>
        walk(`/api/v1/delivery/deliveries/${deliveryId}/pod-photos`),
};
