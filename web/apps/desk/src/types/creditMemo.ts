// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The credit memo on the wire contract (ADR 0005 section 6.3), derived from the
// generated client. Every total, quantity and extension is NEGATIVE; a draft has
// no number until it is posted.

import type { components } from '@gable/api-client';

type Schemas = components['schemas'];

export type CreditMemo = Schemas['CreditMemo'];
export type CreditMemoSummary = Schemas['CreditMemoSummary'];
export type CreditMemoLine = Schemas['CreditMemoLine'];
export type CreditMemoPage = Schemas['CreditMemoPage'];
export type CreditMemoRequest = Schemas['CreditMemoRequest'];
export type CreditMemoLineRequest = Schemas['CreditMemoLineRequest'];
export type CreditMemoTransitionRequest = Schemas['CreditMemoTransitionRequest'];
export type CreditMemoStatus = Schemas['CreditMemoStatus'];
export type CreditReasonCode = CreditMemoSummary['reason_code'];

export const CREDIT_MEMO_STATUSES: CreditMemoStatus[] = ['draft', 'open', 'partial', 'applied', 'void'];
export const CREDIT_REASON_CODES: CreditReasonCode[] = ['return', 'price_adjustment', 'damage', 'other'];

const REASON_LABELS: Record<CreditReasonCode, string> = {
    return: 'Return',
    price_adjustment: 'Price adjustment',
    damage: 'Damage',
    other: 'Other',
};

export const formatReasonCode = (code: CreditReasonCode): string => REASON_LABELS[code] ?? code;

export const formatCreditMemoStatus = (status: CreditMemoStatus): string =>
    status.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase());

export type CreditMemoStatusColor = 'default' | 'info' | 'success' | 'warning' | 'error';

export const getCreditMemoStatusColor = (status: CreditMemoStatus): CreditMemoStatusColor => {
    switch (status) {
        case 'draft': return 'default';
        case 'open': return 'warning';
        case 'partial': return 'info';
        case 'applied': return 'success';
        case 'void': return 'error';
        default: return 'default';
    }
};

/** The number for display: a draft has none yet. */
export const creditMemoLabel = (memo: { number: string | null }): string => memo.number ?? 'Draft';
