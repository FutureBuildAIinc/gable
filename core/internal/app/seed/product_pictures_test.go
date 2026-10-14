// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// RULE: every picture path the seed writes names an illustration the desk
// ships, so a tile never points at a missing file.
func TestEveryPicturePathHasAnIllustration(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "..", "web", "apps", "desk", "public", "catalog-illustrations")
	families := map[string]bool{}
	for _, f := range baseProductFamilies {
		families[f] = true
	}
	for _, line := range bytes.Split(catalogData, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r catalogRow
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		if r.ImageFamily == "" {
			t.Errorf("%s names no image family", r.SKU)
			continue
		}
		families[r.ImageFamily] = true
	}
	for f := range families {
		if _, err := os.Stat(filepath.Join(dir, f+".svg")); err != nil {
			t.Errorf("picture %s: %v", picturePath(f), err)
		}
	}
}
