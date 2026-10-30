// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package appwrite

import (
	"errors"
	"reflect"
	"testing"
)

func TestMapRoles(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"gable prefixes stripped", []string{"gable-owner", "gable-sales"}, []string{"owner", "sales"}},
		{"all eight gable roles", []string{"gable-owner", "gable-admin", "gable-sales", "gable-finance", "gable-purchasing", "gable-warehouse", "gable-cashier", "gable-driver"},
			[]string{"owner", "admin", "sales", "finance", "purchasing", "warehouse", "cashier", "driver"}},
		{"unknown strings ignored", []string{"bogus", "gable-sales", "admin"}, []string{"sales"}},
		{"unknown gable prefixed role ignored", []string{"gable-bogus"}, nil},
		{"appwrite owner ignored by mapping", []string{"owner"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MapRoles(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("MapRoles(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func admitOKUser() User {
	return User{ID: "u1", Status: true, EmailVerification: true}
}

func TestAdmit(t *testing.T) {
	cases := []struct {
		name       string
		user       User
		membership Membership
		found      bool
		wantErr    error
	}{
		{"member admitted", admitOKUser(), Membership{Roles: []string{"gable-cashier"}, Confirmed: true}, true, nil},
		{"absent membership refused", admitOKUser(), Membership{}, false, ErrNoMembership},
		{"unconfirmed membership refused", admitOKUser(), Membership{Roles: []string{"gable-sales"}, Confirmed: false}, true, ErrMembershipUnconfirmed},
		{"blocked user refused", User{ID: "u1", Status: false, EmailVerification: true}, Membership{Roles: []string{"gable-sales"}, Confirmed: true}, true, ErrUserBlocked},
		{"unverified email refused", User{ID: "u1", Status: true, EmailVerification: false}, Membership{Roles: []string{"gable-sales"}, Confirmed: true}, true, ErrEmailUnverified},
		{"appwrite owner refused even with gable role", admitOKUser(), Membership{Roles: []string{"owner", "gable-admin"}, Confirmed: true}, true, ErrOwnerRole},
		{"appwrite owner alone refused", admitOKUser(), Membership{Roles: []string{"owner"}, Confirmed: true}, true, ErrOwnerRole},
		{"only unknown roles refused", admitOKUser(), Membership{Roles: []string{"bogus"}, Confirmed: true}, true, ErrNoGableRoles},
		{"empty roles refused", admitOKUser(), Membership{Roles: []string{}, Confirmed: true}, true, ErrNoGableRoles},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Admit(tc.user, tc.membership, tc.found)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Admit() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
