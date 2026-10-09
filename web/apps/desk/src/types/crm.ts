// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// CRM domain types, on the activities wire contract (ADR 0001): the
// activity_type vocabulary is lowercase, optional fields are null when unset
// (never omitted), and every record carries its revision.

// Contacts are on the customer contract (types/customer.ts); the activity feed reads the name only.
export type { Contact } from './customer';

export type ActivityType = 'call' | 'meeting' | 'email' | 'note';

export interface Activity {
    id: string;
    customer_id: string;
    contact_id: string | null;
    activity_type: ActivityType;
    description: string;
    logged_by: string | null;
    activity_date: string; // ISO 8601
    revision: number;
    created_at: string;
    updated_at: string;
}

export interface CreateActivityRequest {
    contact_id?: string;
    activity_type: ActivityType;
    description: string;
    activity_date?: string;
}

/** One page of the activities list envelope. */
export interface ActivityPage {
    items: Activity[];
    next_cursor: string | null;
    limit: number;
    total?: number;
}
