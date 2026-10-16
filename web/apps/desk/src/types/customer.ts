// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The customer wire types come from the generated contract (core/api/fragments/customer.yaml
// through @gable/api-client); nothing here restates a field by hand. Money is integer cents
// (credit_limit_cents is null for no limit, zero for no credit), the pricing
// program is the customer's price profile id, a percent is a decimal string, and
// every write names the revision it read (If-Match).

import type { components } from '@gable/api-client';

type Schemas = components['schemas'];

export type Customer = Schemas['Customer'];
export type CustomerPage = Schemas['CustomerPage'];
export type CustomerRequest = Schemas['CustomerRequest'];
export type CustomerUpdateRequest = Schemas['CustomerUpdateRequest'];
export type PriceProfile = Schemas['PricingProfile'];
export type PriceProfilePage = Schemas['PricingProfilePage'];
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

/** Every contact role the wire accepts (a contact may also have none). */
export type ContactRole = NonNullable<Contact['role']>;
export const CONTACT_ROLES: ContactRole[] = ['Buyer', 'AP', 'Owner', 'Site Super'];
