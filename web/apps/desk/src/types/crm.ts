// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// CRM domain types

// Contacts are on the customer contract (types/customer.ts); the activity feed reads the name only.
export type { Contact } from './customer';

export type ActivityType = 'CALL' | 'MEETING' | 'EMAIL' | 'NOTE';

export interface Activity {
    id: string;
    customer_id: string;
    contact_id?: string;
    activity_type: ActivityType;
    description: string;
    logged_by?: string;
    activity_date: string; // ISO 8601
    created_at: string;
    updated_at: string;
}

export interface CreateActivityRequest {
    contact_id?: string;
    activity_type: ActivityType;
    description: string;
    activity_date: string;
}
