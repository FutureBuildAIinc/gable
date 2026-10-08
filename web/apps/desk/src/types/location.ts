// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The location and branch wire types come from the generated contract (core/api/fragments/location.yaml
// through @gable/api-client); nothing here restates a field by hand. The type is lowercase on the wire,
// optional fields are present as null, and every row carries the revision a write must name (If-Match).
// BranchSummary and UserLocation belong to the user grant routes, which keep bare arrays.

import type { components } from '@gable/api-client';

type Schemas = components['schemas'];

export type LocationType = Schemas['LocationType'];
export type Location = Schemas['Location'];
export type LocationPage = Schemas['LocationPage'];
export type LocationUpdate = Schemas['LocationUpdate'];
export type CreateLocationRequest = Schemas['LocationCreateRequest'];
export type BranchSummary = Schemas['BranchSummary'] & { is_home: boolean };
export type UserLocation = Schemas['UserLocation'];
