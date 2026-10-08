// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The category pricing wire types come from the generated contract (core/api/fragments/pricing.yaml
// through @gable/api-client); nothing here restates a field by hand. The target and rule types are
// lowercase, a rule's value is value_ten_thousandths (a fixed rule, an integer price at scale 4) or
// value_pct (every other type, a decimal string), and every rule carries the revision a write must
// name (If-Match). Tier names stay the uppercase vocabulary the matrix answers with.

import type { components } from '@gable/api-client';

type Schemas = components['schemas'];

export type ProductCategory = Schemas['PricingProductCategory'];
export type CategoryWrite = Schemas['PricingProductCategoryWrite'];
export type CategoryPricingRule = Schemas['PricingCategoryRule'];
export type CategoryPricingRulePage = Schemas['PricingCategoryRulePage'];
export type CategoryRuleValues = Schemas['PricingCategoryRuleValues'];
export type CategoryRuleCreate = Schemas['PricingCategoryRuleCreateRequest'];
export type CategoryRuleBulkItem = Schemas['PricingCategoryRuleBulkItem'];
export type CategoryRuleUpdate = Schemas['PricingCategoryRuleUpdateRequest'];
export type MatrixCell = Schemas['PricingMatrixCell'];
export type ResolvedCategoryPrice = Schemas['PricingResolvedCategoryPrice'];
export type CategoryPricingAudit = Schemas['PricingCategoryRuleAudit'];

export type TargetType = CategoryPricingRule['target_type'];
export type CategoryRuleType = CategoryPricingRule['rule_type'];

/** The matrix with its three lists made arrays (the wire sends null for an empty list). */
export interface MatrixResponse {
  categories: ProductCategory[];
  tiers: string[];
  cells: MatrixCell[];
}
