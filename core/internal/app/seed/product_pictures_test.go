// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// RULE: the base seed's picture map keys only SKUs the base seed writes, and
// no key holds a brand word, so a renamed product keeps its picture.
func TestBasePictureKeysAreSeededSKUs(t *testing.T) {
	src, err := os.ReadFile("seed.go")
	if err != nil {
		t.Fatal(err)
	}
	for sku := range baseProductFamilies {
		if !strings.Contains(string(src), `"`+sku+`"`) {
			t.Errorf("picture key %q is not a SKU the base seed writes", sku)
		}
		for _, word := range bannedSeedWords {
			if strings.Contains(strings.ToLower(sku), word) {
				t.Errorf("picture key %q holds %q", sku, word)
			}
		}
	}
}

// RULE: no illustration's title or text names a maker's mark.
func TestIllustrationsNameNoBrand(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "..", "web", "apps", "desk", "public", "catalog-illustrations")
	files, err := filepath.Glob(filepath.Join(dir, "*.svg"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no illustrations found: %v", err)
	}
	decora := regexp.MustCompile(`\bdecora\b`)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(raw)) + " " + strings.ToLower(filepath.Base(f))
		for _, word := range bannedSeedWords {
			if strings.Contains(lower, word) {
				t.Errorf("%s holds %q", filepath.Base(f), word)
			}
		}
		if decora.MatchString(lower) {
			t.Errorf("%s holds the mark Decora", filepath.Base(f))
		}
	}
}
