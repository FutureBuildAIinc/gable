// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// The seed draws from one seeded random stream. A loop over a map hands the
// stream's draws to a different customer every run, so which yard owns which
// seeded order (and with it every per-branch figure) changed between two runs
// of the same seed. The loop walks the names in sorted order.
func TestSortedNamesIsStable(t *testing.T) {
	m := map[string]uuid.UUID{}
	for _, n := range []string{"d", "b", "a", "c", "e"} {
		m[n] = uuid.New()
	}
	want := []string{"a", "b", "c", "d", "e"}
	for i := 0; i < 20; i++ {
		if got := sortedNames(m); !reflect.DeepEqual(got, want) {
			t.Fatalf("sortedNames = %v, want %v", got, want)
		}
	}
}
