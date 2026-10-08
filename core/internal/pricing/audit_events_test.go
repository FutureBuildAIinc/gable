// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// Every pricing write (a rule create, a category create or update, a
// category rule write) carries its audit row and its outbox event in the
// same transaction (review r2, N-4): a failing event write, or a failing
// audit write, rolls the write back; a good write records exactly one of
// each, the event last. A category rule's audit trail stays its own
// category_pricing_audit table; the event is new.

import (
	"context"
	"errors"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type failingEvents struct{}

func (failingEvents) Write(context.Context, outbox.Event) error {
	return errors.New("outbox insert failed")
}

type failingPricingAudit struct{}

func (failingPricingAudit) Log(context.Context, pricing.AuditEntry) error {
	return errors.New("audit insert failed")
}

type capturingAudit struct{ entries []pricing.AuditEntry }

func (c *capturingAudit) Log(_ context.Context, e pricing.AuditEntry) error {
	c.entries = append(c.entries, e)
	return nil
}

func wiredRuleService(db *database.DB) *pricing.Service {
	return pricing.NewService(pricing.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
}

func ruleDraft() *pricing.PricingRule {
	return &pricing.PricingRule{
		Name: "AUD-" + uuid.NewString()[:10], RuleType: pricing.RuleTypeQuantityBreak,
		IsActive: true, Revision: 1,
	}
}

func categoryDraft() *pricing.ProductCategory {
	slug := "aud-" + uuid.NewString()[:8]
	return &pricing.ProductCategory{Name: "Audit " + slug, Slug: slug, Path: "aud_" + slug[4:], IsActive: true}
}

func countRows(t *testing.T, db *database.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRuleCreate_AuditAndEventInOneTransaction: the create writes one audit
// row and one pricing_rule.created event; either failing rolls the create
// back.
func TestRuleCreate_AuditAndEventInOneTransaction(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()

	good := wiredRuleService(db).WithAudit(&capturingAudit{})
	rule := ruleDraft()
	if err := good.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, rule.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, rule.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pricing_rules WHERE id = $1`, rule.ID)
	})
	if n := countRows(t, db, `SELECT count(*) FROM events_outbox WHERE type = 'pricing_rule.created' AND entity_id = $1`, rule.ID); n != 1 {
		t.Fatalf("%d pricing_rule.created events, want 1", n)
	}

	badEvent := pricing.NewService(pricing.NewRepository(db)).
		WithOutbox(failingEvents{}).WithTxRunner(db).WithAudit(&capturingAudit{})
	failed := ruleDraft()
	if err := badEvent.CreateRule(ctx, failed); err == nil {
		t.Fatal("the create succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM pricing_rules WHERE id = $1`, failed.ID); n != 0 {
		t.Fatalf("%d rules survived a rolled back create", n)
	}

	badAudit := wiredRuleService(db).WithAudit(failingPricingAudit{})
	failed = ruleDraft()
	if err := badAudit.CreateRule(ctx, failed); err == nil {
		t.Fatal("the create succeeded though its audit row could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM pricing_rules WHERE id = $1`, failed.ID); n != 0 {
		t.Fatalf("%d rules survived a rolled back create", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM events_outbox WHERE entity_id = $1`, failed.ID); n != 0 {
		t.Fatalf("%d events survived a rolled back create, want 0", n)
	}
}

// TestCategoryWrites_AuditAndEventInOneTransaction: a category create and
// update write their audit rows and category.created / category.updated
// events; a failing event write rolls the write back, and a category rule's
// write now leaves a category_rule.* event beside its own audit trail.
func TestCategoryWrites_AuditAndEventInOneTransaction(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	newCat := func(events pricing.EventRecorder, audits pricing.AuditLogger) *pricing.CategoryPricingService {
		svc := pricing.NewCategoryPricingService(pricing.NewCategoryRepository(db)).
			WithTxRunner(db)
		if events != nil {
			svc = svc.WithOutbox(events)
		}
		if audits != nil {
			svc = svc.WithAudit(audits)
		}
		return svc
	}

	good := newCat(outbox.NewWriter(db, ""), &capturingAudit{})
	cat := categoryDraft()
	if err := good.CreateCategory(ctx, cat); err != nil {
		t.Fatal(err)
	}
	cat.Name = cat.Name + " II"
	if err := good.UpdateCategory(ctx, cat); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, cat.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, cat.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_categories WHERE id = $1`, cat.ID)
	})
	assertOne := func(query string, id uuid.UUID, what string) {
		t.Helper()
		if n := countRows(t, db, query, id); n != 1 {
			t.Fatalf("%d %s, want 1", n, what)
		}
	}
	assertOne(`SELECT count(*) FROM events_outbox WHERE type = 'category.created' AND entity_id = $1`, cat.ID, "category.created events")
	assertOne(`SELECT count(*) FROM events_outbox WHERE type = 'category.updated' AND entity_id = $1`, cat.ID, "category.updated events")

	badEvent := newCat(failingEvents{}, &capturingAudit{})
	failed := categoryDraft()
	if err := badEvent.CreateCategory(ctx, failed); err == nil {
		t.Fatal("the create succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM product_categories WHERE id = $1`, failed.ID); n != 0 {
		t.Fatalf("%d categories survived a rolled back create", n)
	}

	badAudit := newCat(outbox.NewWriter(db, ""), failingPricingAudit{})
	failed = categoryDraft()
	if err := badAudit.CreateCategory(ctx, failed); err == nil {
		t.Fatal("the create succeeded though its audit row could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM product_categories WHERE id = $1`, failed.ID); n != 0 {
		t.Fatalf("%d categories survived a rolled back create", n)
	}

	// A category rule's create leaves its own audit entry and now a
	// category_rule.created event, in the same transaction.
	tier := "AUDTIER"
	rule := &pricing.CategoryPricingRule{
		TargetType: pricing.TargetTypeTier, Tier: tier, CategoryID: cat.ID,
		RuleType: pricing.CategoryRuleMarkup, ValuePct: qty(t, "12.5"), IsActive: true, Priority: 3,
	}
	if err := good.CreateCategoryRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, rule.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM category_pricing_audit WHERE rule_id = $1`, rule.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM category_pricing_rules WHERE id = $1`, rule.ID)
	})
	assertOne(`SELECT count(*) FROM category_pricing_audit WHERE rule_id = $1`, rule.ID, "category rule audit entries")
	assertOne(`SELECT count(*) FROM events_outbox WHERE type = 'category_rule.created' AND entity_id = $1`, rule.ID, "category_rule.created events")

	failedRule := &pricing.CategoryPricingRule{
		TargetType: pricing.TargetTypeTier, Tier: tier + "2", CategoryID: cat.ID,
		RuleType: pricing.CategoryRuleMarkdown, ValuePct: qty(t, "10"), IsActive: true,
	}
	if err := badEvent.CreateCategoryRule(ctx, failedRule); err == nil {
		t.Fatal("the rule create succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM category_pricing_rules WHERE id = $1`, failedRule.ID); n != 0 {
		t.Fatalf("%d rules survived a rolled back create", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM category_pricing_audit WHERE rule_id = $1`, failedRule.ID); n != 0 {
		t.Fatalf("%d audit entries survived a rolled back create, want 0", n)
	}
}

func qty(t *testing.T, s string) *httpx.Quantity {
	t.Helper()
	q, err := httpx.ParseQuantity(s)
	if err != nil {
		t.Fatal(err)
	}
	return &q
}
