// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package location

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// RULE (CP-17a): a branch is saved with its own country, or with the dealer's
// default country when it has none. A branch with neither cannot be saved:
// the country picks the branch's large cash rule, and a silent fallback or a
// branch with no rule is the mistake that only surfaces at a filing deadline.
func TestBranchCountry_NeitherItsOwnNorTheDealerDefaultCannotBeSaved(t *testing.T) {
	// the dealer has no default country: the branch must bring its own
	repo := newFakeRepo()
	repo.noDefaultCountry = true
	loc := Location{Type: LocTypeBranch, Code: "VAN", Name: strp("Vancouver")}
	err := NewService(repo).CreateLocation(context.Background(), &loc)
	if err == nil || !strings.Contains(errText(err), "country_code") {
		t.Fatalf("a branch with neither its own country nor the dealer's default = %v, want the refusal naming country_code", err)
	}
	if len(repo.created) != 0 {
		t.Errorf("the refused branch wrote %d rows, want none", len(repo.created))
	}

	// the same branch is saved when the dealer holds a default country
	withDefault := newFakeRepo()
	saved := Location{Type: LocTypeBranch, Code: "VAN", Name: strp("Vancouver")}
	if err := NewService(withDefault).CreateLocation(context.Background(), &saved); err != nil {
		t.Fatalf("a branch with the dealer's default country: %v", err)
	}
	if len(withDefault.created) != 1 {
		t.Errorf("the branch with the dealer's default country wrote %d rows, want one", len(withDefault.created))
	}

	// and with its own country, whatever the dealer holds
	own := newFakeRepo()
	own.noDefaultCountry = true
	country := "CA"
	withOwn := Location{Type: LocTypeBranch, Code: "VAN", Name: strp("Vancouver"), CountryCode: &country}
	if err := NewService(own).CreateLocation(context.Background(), &withOwn); err != nil {
		t.Fatalf("a branch with its own country: %v", err)
	}

	// a country that is not two letters is refused, naming the field
	bad := newFakeRepo()
	tooLong := "USA"
	wrong := Location{Type: LocTypeBranch, Code: "VAN", Name: strp("Vancouver"), CountryCode: &tooLong}
	if err := NewService(bad).CreateLocation(context.Background(), &wrong); err == nil || !strings.Contains(errText(err), "country_code") {
		t.Fatalf("a branch with a country of three letters = %v, want the refusal naming country_code", err)
	}

	// a row that is not a branch holds no country at all
	yard := newFakeRepo()
	yard.noDefaultCountry = true
	parent := uuid.New()
	sub := Location{Type: LocTypeBin, Code: "B2", ParentID: &parent}
	if err := NewService(yard).CreateLocation(context.Background(), &sub); err != nil {
		t.Fatalf("a bin with no country: %v", err)
	}
}

// RULE (CP-17a): the update holds the same rule as the create: a branch that
// would end with neither its own country nor the dealer's default cannot be
// saved.
func TestBranchCountry_TheUpdateHoldsTheSameRule(t *testing.T) {
	repo := newFakeRepo()
	repo.noDefaultCountry = true
	id := uuid.New()
	repo.locations[id] = &Location{ID: id, Type: LocTypeBranch, Code: "VAN", Name: strp("Vancouver"), Revision: 1}

	loc := Location{ID: id, Type: LocTypeBranch, Code: "VAN", Name: strp("Vancouver"), Revision: 1}
	if err := NewService(repo).UpdateLocation(context.Background(), &loc, 1); err == nil ||
		!strings.Contains(errText(err), "country_code") {
		t.Fatalf("a branch update with neither country = %v, want the refusal naming country_code", err)
	}
	if len(repo.updated) != 0 {
		t.Errorf("the refused update wrote %d rows, want none", len(repo.updated))
	}

	// with the dealer's default country the update is saved
	withDefault := newFakeRepo()
	withDefault.locations[id] = &Location{ID: id, Type: LocTypeBranch, Code: "VAN", Name: strp("Vancouver"), Revision: 1}
	saved := Location{ID: id, Type: LocTypeBranch, Code: "VAN", Name: strp("Vancouver"), Revision: 1}
	if err := NewService(withDefault).UpdateLocation(context.Background(), &saved, 1); err != nil {
		t.Fatalf("a branch update with the dealer's default country: %v", err)
	}
}
