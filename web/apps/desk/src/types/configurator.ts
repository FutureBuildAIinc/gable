// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

export interface ConfiguratorRule {
    id: string;
    attribute_type: string;
    attribute_value: string;
    depends_on_type: string;
    depends_on_value: string;
    is_allowed: boolean;
    error_message: string | null;
    created_at: string;
    updated_at: string;
}

export interface ValidationConflict {
    attribute_type: string;
    attribute_value: string;
    depends_on_type: string;
    depends_on_value: string;
    message: string;
}

export interface ValidateConfigResponse {
    valid: boolean;
    /** Always an array, empty when the selections are valid. */
    conflicts: ValidationConflict[];
}

/**
 * A configured item's configuration: the choices the dealer made, in the
 * canonical form the server wrote them. The category names are the option
 * catalog's own (snake_case) and the values are the option names the catalog
 * stores. The server prices a line that carries one from the stored options.
 */
export interface ConfiguredConfiguration {
    product_type?: string;
    /** The chosen option for each attribute category. */
    selections: Record<string, string>;
    /** The door's width in inches, when the configuration carries one. */
    width?: number;
    /** The door's height in inches, when the configuration carries one. */
    height?: number;
}

/**
 * The configured item the server answers: its SKU, the description the
 * documents carry, the product row it became, the server's price and cost
 * and the canonical configuration they came from. The money is in
 * ten-thousandths of the major unit.
 */
export interface BuildSKUResponse {
    sku: string;
    description: string;
    product_id: string;
    unit_price_ten_thousandths: number;
    unit_cost_ten_thousandths: number;
    configuration_json: ConfiguredConfiguration;
}

export interface AvailableOption {
    value: string;
    allowed: boolean;
    message: string | null;
}

export interface ConfiguratorPreset {
    id: string;
    name: string;
    description: string | null;
    product_type: string;
    /** The JSON the dealer stored, carried as JSON (it used to be base64 text). */
    config: Record<string, unknown>;
    is_active: boolean;
    created_at: string;
    updated_at: string;
}

export interface BlueprintScanResponse {
    extracted_dimensions: Record<string, string>;
    mismatches: Mismatch[];
    summary: string;
}

export interface Mismatch {
    field: string;
    blueprint_value: string;
    config_value: string;
    severity: 'warning' | 'error';
    message: string;
}

// Wizard step definitions
export interface ConfigStep {
    key: string;
    label: string;
    attributeType: string;
    description: string;
}

export const CONFIGURATOR_STEPS: ConfigStep[] = [
    { key: 'product_type', label: 'Product Type', attributeType: 'ProductType', description: 'Select the product category' },
    { key: 'species', label: 'Species', attributeType: 'Species', description: 'Choose wood species' },
    { key: 'grade', label: 'Grade & Treatment', attributeType: 'Grade', description: 'Select grade and treatment options' },
    { key: 'dimensions', label: 'Dimensions', attributeType: 'Dimensions', description: 'Specify dimensions' },
    { key: 'review', label: 'Review & Build', attributeType: '', description: 'Review and generate SKU' },
];
