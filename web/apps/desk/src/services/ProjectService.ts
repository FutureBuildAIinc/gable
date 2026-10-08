// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type {
    Project,
    ProjectDashboard,
    ProjectPage,
    CreateProjectRequest,
    UpdateProjectRequest
} from '../types/project';
import { ifMatch, parseApiError } from './apiError';
import { fetchWithAuth } from './fetchClient';

const API_URL = import.meta.env.VITE_API_URL || '';

/**
 * Wrapper around shared fetchWithAuth that handles response parsing.
 * 401 handling is delegated to fetchWithAuth (centralized interceptor).
 */
async function authedFetch<T>(url: string, options: RequestInit = {}, fallback: string): Promise<T> {
    const response = await fetchWithAuth(url, options);
    if (!response.ok) {
        throw await parseApiError(response, fallback);
    }
    return await response.json() as T;
}

export const ProjectService = {
    async listProjects(opts: { status?: 'active' | 'completed'; limit?: number; cursor?: string } = {}): Promise<ProjectPage> {
        const params = new URLSearchParams();
        if (opts.status) params.set('status', opts.status);
        if (opts.limit) params.set('limit', String(opts.limit));
        if (opts.cursor) params.set('cursor', opts.cursor);
        const q = params.toString();
        return authedFetch<ProjectPage>(`${API_URL}/api/portal/v1/projects${q ? `?${q}` : ''}`, {}, 'Failed to load projects');
    },

    async getProjectDashboard(id: string): Promise<ProjectDashboard> {
        return authedFetch<ProjectDashboard>(`${API_URL}/api/portal/v1/projects/${id}`, {}, 'Failed to load project');
    },

    async createProject(req: CreateProjectRequest): Promise<Project> {
        return authedFetch<Project>(`${API_URL}/api/portal/v1/projects`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        }, 'Failed to create project');
    },

    /** The PUT carries the revision it loaded; a 409 stale_revision reloads with a toast in the page. */
    async updateProject(id: string, req: UpdateProjectRequest, revision: number): Promise<Project> {
        return authedFetch<Project>(`${API_URL}/api/portal/v1/projects/${id}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json', 'If-Match': ifMatch(revision) },
            body: JSON.stringify(req),
        }, 'Failed to update project');
    },
};
