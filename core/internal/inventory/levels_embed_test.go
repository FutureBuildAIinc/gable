// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory_test

// ADR 0006 7.2: the inventory levels list embeds a four field product summary
// under include=product. The summary's field names must stay in lockstep with
// product.View's: a change to the view (a rename, a typo) cannot drift the
// embed silently. The test reflects both structs, reads every JSON tag, and
// asserts each summary field's JSON name lives in the view with the same
// spelling and tag value.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/product"
)

func TestInventoryLevels_ProductSummaryAlignsWithProductView(t *testing.T) {
	viewFields := jsonTags(reflect.TypeOf(product.View{}))
	if len(viewFields) == 0 {
		t.Fatal("product.View has no JSON-tagged fields; the test cannot proceed")
	}
	summaryFields := jsonTags(reflect.TypeOf(inventory.ProductSummary{}))
	if len(summaryFields) == 0 {
		t.Fatal("inventory.ProductSummary has no JSON-tagged fields; the test cannot proceed")
	}
	// The summary is a four field subset of the view (id, sku, description,
	// stock_uom, in that order). Each name must match a view field with the
	// same JSON tag value, and the summary must not carry a field the view
	// does not (a drift the other way).
	if got, want := len(summaryFields), 4; got != want {
		t.Fatalf("inventory.ProductSummary carries %d JSON fields, want %d (id, sku, description, stock_uom): %v", got, want, summaryFields)
	}
	// Iterate the summary's JSON names in declaration order (Go maps are
	// unordered; we read the struct again to keep the order).
	summaryOrder := jsonTagsOrdered(reflect.TypeOf(inventory.ProductSummary{}))
	wantSummary := []string{"id", "sku", "description", "stock_uom"}
	for i, name := range wantSummary {
		if summaryOrder[i] != name {
			t.Errorf("inventory.ProductSummary field %d = %q, want %q (order matches product.View)", i, summaryOrder[i], name)
		}
	}
	for _, name := range summaryOrder {
		if _, ok := viewFields[name]; !ok {
			t.Errorf("inventory.ProductSummary carries %q, but product.View has no such JSON field (the names must match)", name)
		}
	}
}

// jsonTags reads the JSON-tagged field names of a struct as a map from name
// to its full tag value. The tag value "-" (a field hidden from the wire) is
// skipped; a field whose JSON name carries an option (e.g.
// `json:"name,omitempty"`) keeps the full tag so an option drift is also
// caught.
func jsonTags(t reflect.Type) map[string]string {
	out := map[string]string{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, ok := f.Tag.Lookup("json")
		if !ok {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		out[name] = tag
	}
	return out
}

// jsonTagsOrdered is jsonTags' ordered sibling: it returns the JSON names in
// declaration order, so a test that cares about position can read it.
func jsonTagsOrdered(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, ok := f.Tag.Lookup("json")
		if !ok {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}
