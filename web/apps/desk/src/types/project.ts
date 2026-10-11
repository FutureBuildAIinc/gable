// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Project domain types, on the projects wire contract (ADR 0001): the status
// vocabulary is lowercase, every record carries its revision, and a
// dashboard item's total is integer cents.

export type ProjectStatus = 'active' | 'completed' | 'inactive';

export interface Project {
    id: string;
    customer_id: string;
    name: string;
    status: ProjectStatus;
    revision: number;
    created_at: string;
    updated_at: string;
}

export interface ProjectItem {
    id: string;
    type: 'order' | 'delivery' | 'invoice';
    status: string;
    total_cents: number | null;
    created_at: string;
    reference: string;
}

export interface ProjectDashboard {
    project: Project;
    orders: ProjectItem[];
    deliveries: ProjectItem[];
    invoices: ProjectItem[];
}

export interface CreateProjectRequest {
    name: string;
}

export interface UpdateProjectRequest {
    name?: string;
    status?: 'active' | 'completed';
    revision?: number;
}

/** One page of the projects list envelope. */
export interface ProjectPage {
    items: Project[];
    next_cursor: string | null;
    limit: number;
    total?: number;
}
