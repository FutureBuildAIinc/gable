// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The customer wire types come from the generated contract (core/api/fragments/customer.yaml
// through @gable/api-client); nothing here restates a field by hand. Money is integer cents
// (credit_limit_cents is null for no limit, zero for no credit), the tier is lowercase, a
// percent is a decimal string, and every write names the revision it read (If-Match).

import type { components } from '@gable/api-client';

type Schemas = components['schemas'];

export type Customer = Schemas['Customer'];
export type CustomerPage = Schemas['CustomerPage'];
export type CustomerRequest = Schemas['CustomerRequest'];
export type CustomerUpdateRequest = Schemas['CustomerUpdateRequest'];
export type CustomerTier = Schemas['CustomerTier'];
export type PriceLevel = Schemas['PriceLevel'];
export type PriceLevelPage = Schemas['PriceLevelPage'];
export type PaymentTermsRef = Schemas['PaymentTermsRef'];
export type PaymentTermsRecord = Schemas['PaymentTermsRecord'];
export type PaymentTermsRecordPage = Schemas['PaymentTermsRecordPage'];
export type EscalationPolicy = Schemas['EscalationPolicy'];
export type EscalationPolicyRequest = Schemas['EscalationPolicyRequest'];
export type ShipTo = Schemas['ShipTo'];
export type ShipToPage = Schemas['ShipToPage'];
export type ShipToRequest = Schemas['ShipToRequest'];
export type Contact = Schemas['Contact'];
export type ContactPage = Schemas['ContactPage'];
export type ContactRequest = Schemas['ContactRequest'];
export type ContactUpdateRequest = Schemas['ContactUpdateRequest'];

/** Every tier the wire accepts, in the order a select shows them. The Record keys must match the contract exactly. */
const TIER_SET: Record<CustomerTier, true> = { retail: true, silver: true, gold: true, platinum: true };
export const CUSTOMER_TIERS = Object.keys(TIER_SET) as CustomerTier[];

/** Every contact role the wire accepts (a contact may also have none). */
export type ContactRole = NonNullable<Contact['role']>;
export const CONTACT_ROLES: ContactRole[] = ['Buyer', 'AP', 'Owner', 'Site Super'];
