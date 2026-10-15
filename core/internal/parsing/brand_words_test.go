// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package parsing

import (
	"os"
	"strings"
	"testing"
)

// brandWords are real brands the intake's sample list and its tests never
// carry: a sample line is named for what the product is.
var brandWords = []string{
	"quikrete", "tyvek", "simpson", "strong-tie", "proframe angle", "trueline",
	"hardie", "romex", "dryline", "firecode",
}

// FROM-PRINCIPAL 118 a (N5): the canned sample list and the service's own
// tests hold plain product words only.
func TestSampleListAndItsTestsHoldNoBrandWord(t *testing.T) {
	sources := map[string]string{"sample list": generateFallbackMaterialList()}
	for _, name := range []string{"service.go", "service_test.go", "service_degradation_test.go", "../ai/openrouter.go", "../tax/service_test.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sources[name] = string(data)
	}
	for name, text := range sources {
		lower := strings.ToLower(text)
		for _, w := range brandWords {
			if strings.Contains(lower, w) {
				t.Errorf("%s holds the brand word %q", name, w)
			}
		}
	}
}
