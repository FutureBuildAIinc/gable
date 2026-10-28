// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** A customer's tax exemption certificate (the tax routes). */
export interface TaxExemption {
    id: string;
    customer_id: string;
    exempt_reason: string;
    certificate_number: string;
    issuing_state: string | null;
    effective_date: string;
    expiry_date: string | null;
    is_active: boolean;
    created_at: string;
    /** The server's word for the certificate's standing on the customer's branch day. */
    status?: 'active' | 'expired' | 'future' | 'ended';
}

/** The body of the exemption create route. */
export interface TaxExemptionRequest {
    customer_id: string;
    exempt_reason: string;
    certificate_number: string;
    issuing_state: string;
    effective_date?: string;
    expiry_date?: string;
}
