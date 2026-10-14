// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The shared storage key the desk and the door use to remember the user's
 * currently selected branch (the localStorage entry `fetchClient.ts` reads to
 * stamp `X-Branch-Id` onto every call). The desk's `BranchContext` owns the
 * value; custody's `signInDev` drops it when a persona signs in, so the next
 * desk boot opens on the persona's home yard. Both reach for the same
 * literal: it lives here so neither has to repeat it, and a rename is a single
 * diff.
 */
export const BRANCH_STORAGE_KEY = 'gable_current_branch_id';
