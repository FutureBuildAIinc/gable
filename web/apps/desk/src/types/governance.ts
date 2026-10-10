// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

export type RFCStatus = 'draft' | 'review' | 'approved' | 'rejected';

/**
 * One RFC as the list serves it: everything but the body, so a list page
 * never drags the content along.
 */
export interface RFCSummary {
    id: string;
    /** The human readable document number (RFC-000001). */
    number: string;
    title: string;
    status: RFCStatus;
    author_id: string | null;
    revision: number;
    created_at: string;
    updated_at: string;
}

export interface RFC extends RFCSummary {
    problem_statement: string;
    proposed_solution: string;
    /** Generated at create; null only on rows the legacy seed wrote without one. */
    content: string | null;
}

export interface CreateRFCInput {
    title: string;
    problem_statement: string;
    proposed_solution: string;
}
