// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The category pricing wire (ADR 0001, ADR 0006 section 7.3): cursor lists, lowercase target and
 * rule types, a rule's value as value_ten_thousandths (fixed) or value_pct (every other type),
 * a quoted If-Match on every rule PUT and DELETE, and the one error envelope. Every failure is
 * thrown as an ApiError.
 */
import type {
  CategoryPricingAudit,
  CategoryPricingRule,
  CategoryPricingRulePage,
  CategoryRuleBulkItem,
  CategoryRuleCreate,
  CategoryRuleType,
  CategoryRuleUpdate,
  CategoryRuleValues,
  CategoryWrite,
  MatrixResponse,
  ProductCategory,
  ResolvedCategoryPrice,
} from '../types/category-pricing';
import { fetchWithAuth } from './fetchClient';
import { ifMatch, parseApiError } from './apiError';
import { dollarsToTenThousandths, normalizeDecimal, formatQuantity, tenThousandthsToInput } from '../lib/money';
import { formatPrice4 } from '../lib/utils';

const API_URL = import.meta.env.VITE_API_URL || '';

const JSON_HEADERS = { 'Content-Type': 'application/json' };

export interface ListRulesParams {
  /** Filters, all optional; anything else is a 400. */
  target_type?: 'account' | 'tier';
  tier?: string;
  customer_id?: string;
  category_id?: string;
  is_active?: boolean;
  limit?: number;
  cursor?: string | null;
  includeTotal?: boolean;
}

/** The query string of a rule list; only the documented parameters are sent. */
export function buildRuleQuery(params: ListRulesParams = {}): string {
  const q = new URLSearchParams();
  if (params.target_type) q.set('target_type', params.target_type);
  if (params.tier) q.set('tier', params.tier);
  if (params.customer_id) q.set('customer_id', params.customer_id);
  if (params.category_id) q.set('category_id', params.category_id);
  if (params.is_active !== undefined) q.set('is_active', String(params.is_active));
  if (params.limit) q.set('limit', String(params.limit));
  if (params.cursor) q.set('cursor', params.cursor);
  if (params.includeTotal) q.set('include', 'total');
  const s = q.toString();
  return s ? `?${s}` : '';
}

/** The most rules listAllRules collects (pages of 200). */
export const LIST_ALL_RULES_CAP = 2000;

async function expectOk(response: Response, fallback: string): Promise<Response> {
  if (!response.ok) throw await parseApiError(response, fallback);
  return response;
}

/**
 * How a rule's value reads on the matrix and the account table: a fixed rule is a price per
 * stocking unit ("$5.50"), the others a percentage ("-15%", "+12.5%", "M30%"). Integer and string
 * arithmetic only.
 */
export function formatRuleValue(rule: Pick<CategoryPricingRule, 'rule_type' | 'value_ten_thousandths' | 'value_pct'>): string {
  if (rule.rule_type === 'fixed') {
    return rule.value_ten_thousandths === null ? '' : formatPrice4(rule.value_ten_thousandths);
  }
  const pct = formatQuantity(rule.value_pct);
  switch (rule.rule_type) {
    case 'markdown': return `-${pct}%`;
    case 'markup': return `+${pct}%`;
    case 'margin': return `M${pct}%`;
    default: return pct;
  }
}

/** A stored percentage for an input box: trailing fraction zeros dropped, "15.0000" -> "15", "12.5000" -> "12.5". */
export function percentInputText(text: string | null | undefined): string {
  if (text === null || text === undefined) return '';
  return text.replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '');
}

/** The text a value input shows for a stored rule: a fixed price in dollars ("5.50"), a percentage as sent ("15"). */
export function ruleValueInputText(rule: Partial<Pick<CategoryPricingRule, 'rule_type' | 'value_ten_thousandths' | 'value_pct'>> | null | undefined): string {
  if (!rule) return '';
  if (rule.rule_type === 'fixed') {
    return rule.value_ten_thousandths === null || rule.value_ten_thousandths === undefined ? '' : tenThousandthsToInput(rule.value_ten_thousandths);
  }
  return percentInputText(rule.value_pct);
}

/**
 * The value fields of a rule write from what the user typed. A fixed rule reads the text as dollars
 * and sends value_ten_thousandths; every other type reads it as a percentage and sends value_pct as a
 * decimal string. A blank or malformed value is refused with a message; the margin floor is optional.
 */
export function ruleValuesFromInput(
  ruleType: CategoryRuleType,
  valueText: string,
  marginFloorText = '',
): { ok: true; values: CategoryRuleValues } | { ok: false; message: string } {
  const values: CategoryRuleValues = { rule_type: ruleType };
  if (ruleType === 'fixed') {
    const tt = dollarsToTenThousandths(valueText);
    if (tt === null) return { ok: false, message: 'Enter the fixed price in dollars, for example 5.50 (at most four decimal places)' };
    values.value_ten_thousandths = tt;
  } else {
    const pct = normalizeDecimal(valueText);
    if (pct === null) return { ok: false, message: 'Enter the percentage as a plain number, for example 12.5 (at most four decimal places)' };
    values.value_pct = pct;
  }
  if (marginFloorText.trim() !== '') {
    const floor = normalizeDecimal(marginFloorText);
    if (floor === null) return { ok: false, message: 'Enter the margin floor as a plain percentage, for example 20' };
    values.margin_floor_pct = floor;
  }
  return { ok: true, values };
}

/**
 * The body that updates a rule. The PUT replaces the rule's values, window and standing, so the
 * loaded rule supplies what the form does not show; the target fields are never sent. The revision
 * travels in If-Match.
 */
export function ruleUpdateFromRule(rule: CategoryPricingRule, values: CategoryRuleValues): CategoryRuleUpdate {
  return {
    ...values,
    starts_at: rule.starts_at,
    expires_at: rule.expires_at,
    is_active: rule.is_active,
    priority: rule.priority,
  };
}

export const categoryPricingService = {
  // --- Categories ---
  /** Both views answer the envelope with everything on one page; the tree view nests children. */
  listCategories: async (view: 'tree' | 'flat' = 'tree'): Promise<ProductCategory[]> => {
    const res = await expectOk(await fetchWithAuth(`${API_URL}/api/v1/pricing/categories?view=${view}`), 'Failed to load categories');
    const page: { items: ProductCategory[] } = await res.json();
    return page.items;
  },

  createCategory: async (data: CategoryWrite): Promise<ProductCategory> => {
    const res = await expectOk(
      await fetchWithAuth(`${API_URL}/api/v1/pricing/categories`, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(data) }),
      'Failed to create category',
    );
    return res.json();
  },

  updateCategory: async (id: string, data: CategoryWrite): Promise<ProductCategory> => {
    const res = await expectOk(
      await fetchWithAuth(`${API_URL}/api/v1/pricing/categories/${id}`, { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(data) }),
      'Failed to update category',
    );
    return res.json();
  },

  // --- Rules ---
  listRules: async (params: ListRulesParams = {}): Promise<CategoryPricingRulePage> => {
    const res = await expectOk(await fetchWithAuth(`${API_URL}/api/v1/pricing/category-rules${buildRuleQuery(params)}`), 'Failed to load rules');
    return res.json();
  },

  /** Pages by cursor until the last page or LIST_ALL_RULES_CAP rules. */
  listAllRules: async (params: Omit<ListRulesParams, 'limit' | 'cursor' | 'includeTotal'> = {}): Promise<CategoryPricingRule[]> => {
    const all: CategoryPricingRule[] = [];
    let cursor: string | null = null;
    while (all.length < LIST_ALL_RULES_CAP) {
      const page: CategoryPricingRulePage = await categoryPricingService.listRules({ ...params, limit: 200, cursor });
      all.push(...page.items);
      if (!page.next_cursor) break;
      cursor = page.next_cursor;
    }
    return all.slice(0, LIST_ALL_RULES_CAP);
  },

  createRule: async (rule: CategoryRuleCreate): Promise<CategoryPricingRule> => {
    const res = await expectOk(
      await fetchWithAuth(`${API_URL}/api/v1/pricing/category-rules`, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(rule) }),
      'Failed to create rule',
    );
    return res.json();
  },

  /** Replaces the rule's values on the revision the page loaded; a target field in the body is a 400. */
  updateRule: async (id: string, rule: CategoryRuleUpdate, revision: number): Promise<CategoryPricingRule> => {
    const res = await expectOk(
      await fetchWithAuth(`${API_URL}/api/v1/pricing/category-rules/${id}`, {
        method: 'PUT',
        headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
        body: JSON.stringify(rule),
      }),
      'Failed to update rule',
    );
    return res.json();
  },

  deleteRule: async (id: string, revision: number): Promise<void> => {
    await expectOk(
      await fetchWithAuth(`${API_URL}/api/v1/pricing/category-rules/${id}`, { method: 'DELETE', headers: { 'If-Match': ifMatch(revision) } }),
      'Failed to delete rule',
    );
  },

  // --- Bulk ---
  /** An element that carries an id replaces the rule that has it; the others create. */
  bulkUpsertRules: async (rules: CategoryRuleBulkItem[]): Promise<{ count: number }> => {
    const res = await expectOk(
      await fetchWithAuth(`${API_URL}/api/v1/pricing/category-rules/bulk`, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(rules) }),
      'Failed to bulk upsert rules',
    );
    return res.json();
  },

  bulkDeleteRules: async (ids: string[]): Promise<void> => {
    await expectOk(
      await fetchWithAuth(`${API_URL}/api/v1/pricing/category-rules/bulk`, { method: 'DELETE', headers: JSON_HEADERS, body: JSON.stringify({ ids }) }),
      'Failed to bulk delete rules',
    );
  },

  // --- Audit ---
  /** The rule's audit entries; the envelope answers everything on one page. */
  getRuleAudit: async (ruleId: string): Promise<CategoryPricingAudit[]> => {
    const res = await expectOk(await fetchWithAuth(`${API_URL}/api/v1/pricing/category-rules/${ruleId}/audit`), 'Failed to load audit trail');
    const page: { items: CategoryPricingAudit[] } = await res.json();
    return page.items;
  },

  // --- Matrix ---
  getMatrix: async (): Promise<MatrixResponse> => {
    const res = await expectOk(await fetchWithAuth(`${API_URL}/api/v1/pricing/matrix`), 'Failed to load matrix');
    const m: { categories: ProductCategory[] | null; tiers: string[] | null; cells: MatrixResponse['cells'] | null } = await res.json();
    return { categories: m.categories ?? [], tiers: m.tiers ?? [], cells: m.cells ?? [] };
  },

  // --- Resolution Preview ---
  resolvePreview: async (productId: string, customerId?: string, tier?: string): Promise<ResolvedCategoryPrice> => {
    const params = new URLSearchParams({ product_id: productId });
    if (customerId) params.set('customer_id', customerId);
    if (tier) params.set('tier', tier);
    const res = await expectOk(await fetchWithAuth(`${API_URL}/api/v1/pricing/resolve?${params.toString()}`), 'Failed to resolve price');
    return res.json();
  },
};
