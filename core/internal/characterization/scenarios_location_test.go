// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

// Location groups: the branch/location hierarchy and the user-branch grants.
// Branches and users scope every other module, so their read and write shapes
// are pinned first. The group runs before anything else writes audit rows:
// GET /api/v1/users is the union of user_locations and audit_log subjects, and
// its empty answer ([]) is part of the contract.

func locationGroups() []groupDef {
	return []groupDef{{
		name: "location",
		steps: []stepDef{
			{name: "branch.list", method: "GET", path: "/api/v1/branches"},
			{
				name:   "branch.create",
				method: "POST",
				path:   "/api/v1/branches",
				body: map[string]any{
					"code": "GOLD", "name": "Golden Branch",
					"address": "2 Golden Way", "city": "Kelowna", "state": "BC", "zip": "V1X 7K2",
					"timezone": "America/Vancouver",
				},
				extract: map[string]string{"newBranch": "/id"},
			},
			{name: "branch.get", method: "GET", path: "/api/v1/branches/{newBranch}"},
			// Update: type and parent are not mutable from the endpoint; the
			// rename is the observable change.
			{
				name:   "branch.update",
				method: "PUT",
				path:   "/api/v1/branches/{newBranch}",
				body:   map[string]any{"code": "GOLD", "name": "Golden Branch Renamed"},
			},
			{name: "location.list", method: "GET", path: "/api/v1/locations"},
			{
				name:   "location.create",
				method: "POST",
				path:   "/api/v1/locations",
				body: map[string]any{
					"code": "GOLD-YARD", "type": "YARD", "name": "Golden Yard",
					"parent_id": "{newBranch}",
				},
				extract: map[string]string{"myLocation": "/id"},
			},
			{name: "location.get", method: "GET", path: "/api/v1/locations/{myLocation}"},
			// The branch reader refuses a non-branch row: type discipline.
			{name: "branch.get.not_a_branch", method: "GET", path: "/api/v1/branches/{myLocation}"},
			// Dev mode without claims: all active branches, first flagged home.
			{name: "me.branches", method: "GET", path: "/api/v1/me/branches"},
			// No grants and no audit rows yet: pins [] rather than null.
			{name: "users.list_empty", method: "GET", path: "/api/v1/users"},
			{
				name:   "users.grant",
				method: "POST",
				path:   "/api/v1/users/golden-user-1/branches",
				body:   map[string]any{"branch_id": "{newBranch}", "is_home": true},
			},
			{name: "users.branches", method: "GET", path: "/api/v1/users/golden-user-1/branches"},
			{name: "users.home_branch", method: "PUT", path: "/api/v1/users/golden-user-1/home-branch",
				body: map[string]any{"branch_id": "{newBranch}"}},
			{name: "users.list_after_grant", method: "GET", path: "/api/v1/users"},
			// A yard is not a branch: the grant's own validation answer.
			{
				name:   "users.grant.not_a_branch",
				method: "POST",
				path:   "/api/v1/users/golden-user-1/branches",
				body:   map[string]any{"branch_id": "{myLocation}"},
			},
			// Setting a home branch for a user with no grant.
			{name: "users.home_branch.no_grant", method: "PUT", path: "/api/v1/users/golden-user-2/home-branch",
				body: map[string]any{"branch_id": "{newBranch}"}},
			{name: "branch.users", method: "GET", path: "/api/v1/branches/{newBranch}/users"},
			{name: "users.revoke", method: "DELETE",
				path: "/api/v1/users/golden-user-1/branches/{newBranch}"},
			// Delete branch: soft archive (active=false), so the default list
			// no longer shows it.
			{name: "branch.delete", method: "DELETE", path: "/api/v1/branches/{newBranch}"},
			{name: "branch.list_after_delete", method: "GET", path: "/api/v1/branches"},
		},
	}}
}
