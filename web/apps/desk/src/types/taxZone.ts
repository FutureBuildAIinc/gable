// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** One part of a zone's rate, with its own effective window. */
export interface TaxZoneRatePart {
  name: string;
  rate: string;
  effective_from: string;
  effective_to?: string;
}

/** The postal code, or postal code range, a zone covers. */
export interface TaxZoneBoundary {
  country: string;
  postal_code_low: string;
  postal_code_high: string;
  extension_low?: string;
  extension_high?: string;
}

/** A tax jurisdiction and its rate parts, as the tax zones route answers it. */
export interface TaxZone {
  id: string;
  country: string;
  jurisdiction_code: string;
  jurisdiction_type: string;
  jurisdiction_name: string;
  state_code?: string;
  county_code?: string;
  place_code?: string;
  province_code?: string;
  zone_kind: string;
  sourcing: string;
  effective_from: string;
  effective_to: string | null;
  data_source: string;
  data_version: string;
  rates: TaxZoneRatePart[];
  boundaries: TaxZoneBoundary[];
}

/** The create body of a zone. */
export interface TaxZoneCreateRequest {
  country: string;
  jurisdiction_code: string;
  jurisdiction_type: string;
  jurisdiction_name?: string;
  state_code?: string;
  county_code?: string;
  place_code?: string;
  province_code?: string;
  zone_kind?: string;
  sourcing?: string;
  effective_from: string;
  data_source?: string;
  data_version?: string;
  rates: TaxZoneRatePart[];
  boundaries: TaxZoneBoundary[];
}

/** The close body: the date the zone's window ends. */
export interface TaxZoneCloseRequest {
  effective_to: string;
}
