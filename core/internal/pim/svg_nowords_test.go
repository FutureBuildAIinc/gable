// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pim

import (
	"strings"
	"testing"
)

// FROM-PRINCIPAL 118 a (N8, review round 1 P3-4): the prompt asks for no words
// on a drawing, and the sanitiser is the second guard: a model's text and
// tspan elements are dropped with their words, and the shapes stay.
func TestSanitizeSVG_DropsTheModelsWords(t *testing.T) {
	in := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256"><rect width="9" height="9"/><text x="1" y="1">BRAND 100 FT</text><g><text x="2" y="2"><tspan>LABEL</tspan></text></g></svg>`
	out, err := SanitizeSVG([]byte(in))
	if err != nil {
		t.Fatalf("sanitise: %v", err)
	}
	s := string(out)
	for _, bad := range []string{"<text", "<tspan", "BRAND", "LABEL"} {
		if strings.Contains(s, bad) {
			t.Errorf("the drawing still holds %q: %s", bad, s)
		}
	}
	if !strings.Contains(s, "<rect") {
		t.Errorf("the shape was dropped: %s", s)
	}
}
