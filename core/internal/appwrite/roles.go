// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package appwrite

import (
	"errors"
	"strings"
)

// RolePrefix marks an Appwrite team role as a Gable role.
const RolePrefix = "gable-"

// gableRoles is the closed set MapRoles may yield; anything else on a
// membership is ignored.
var gableRoles = map[string]bool{
	"owner":      true,
	"admin":      true,
	"sales":      true,
	"finance":    true,
	"purchasing": true,
	"warehouse":  true,
	"cashier":    true,
	"driver":     true,
}

// MapRoles maps Appwrite team roles onto Gable roles: the gable- prefix is
// stripped, and any string that is not a known Gable role is ignored.
func MapRoles(roles []string) []string {
	var out []string
	for _, r := range roles {
		if !strings.HasPrefix(r, RolePrefix) {
			continue
		}
		role := strings.TrimPrefix(r, RolePrefix)
		if gableRoles[role] {
			out = append(out, role)
		}
	}
	return out
}

// The fixed refusals of the admission decision.
var (
	ErrNoMembership          = errors.New("appwrite: no membership in the staff team")
	ErrMembershipUnconfirmed = errors.New("appwrite: staff team membership is not confirmed")
	ErrUserBlocked           = errors.New("appwrite: user is blocked or deleted")
	ErrEmailUnverified       = errors.New("appwrite: user email is not verified")
	ErrOwnerRole             = errors.New("appwrite: membership holds the appwrite owner role")
	ErrNoGableRoles          = errors.New("appwrite: membership holds no gable role")
)

// Admit decides whether an Appwrite account may enter Gable. It refuses an
// absent or unconfirmed staff membership, a blocked or deleted user, an
// unverified email, a membership holding Appwrite's owner role at all, and a
// membership whose roles map to no Gable role.
func Admit(user User, m Membership, found bool) error {
	if !found {
		return ErrNoMembership
	}
	if !m.Confirmed {
		return ErrMembershipUnconfirmed
	}
	if !user.Status {
		return ErrUserBlocked
	}
	if !user.EmailVerification {
		return ErrEmailUnverified
	}
	for _, r := range m.Roles {
		if r == "owner" {
			return ErrOwnerRole
		}
	}
	if len(MapRoles(m.Roles)) == 0 {
		return ErrNoGableRoles
	}
	return nil
}
