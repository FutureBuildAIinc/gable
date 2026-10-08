// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package audit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// auditRow is the slice of audit_log the actor tests assert on.
type auditRow struct {
	Action    string
	ActorKind string
	ActorID   *string
	ActingAs  *string
	Tool      *string
	UserID    *string
}

func fetchRows(t *testing.T, db *database.DB, entityID uuid.UUID) []auditRow {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT action, actor_kind, actor_id, acting_as, tool, user_id
		   FROM audit_log WHERE entity_id = $1 ORDER BY created_at`, entityID)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.Action, &r.ActorKind, &r.ActorID, &r.ActingAs, &r.Tool, &r.UserID); err != nil {
			t.Fatalf("scan audit_log: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// insertCustomerMutation is a stand-in mutation executed through the given
// context's executor, so it shares the caller's transaction when there is one:
// the tests prove the audit row shares the mutation's fate.
func insertCustomerMutation(t *testing.T, db *database.DB, ctx context.Context) {
	t.Helper()
	id := uuid.New()
	if _, err := db.GetExecutor(ctx).Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ($1, 'Audit Test Customer', $2,
		         (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		id, "AUD-"+id.String()[:8]); err != nil {
		t.Fatalf("insert customer mutation: %v", err)
	}
}

func TestLog_RolledBackTransactionLeavesNoAuditRow(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)
	entityID := uuid.New()

	err := db.RunInTx(context.Background(), func(txCtx context.Context) error {
		insertCustomerMutation(t, db, txCtx)
		logger.Log(txCtx, audit.Entry{
			Action:     "customer.mutated",
			EntityType: "customer",
			EntityID:   entityID,
		})
		return errors.New("deliberate rollback: mutation and audit must both disappear")
	})
	if err == nil {
		t.Fatal("expected the transaction to return the deliberate error")
	}

	// Give any stray asynchronous write ample time to land: the assertion is
	// that NO row exists, so the test must not pass by winning a race.
	time.Sleep(300 * time.Millisecond)

	if rows := fetchRows(t, db, entityID); len(rows) != 0 {
		t.Fatalf("rolled back mutation left %d audit row(s); audit written inside the transaction must roll back with it", len(rows))
	}
}

func TestLog_CommittedTransactionWritesExactlyOneRow(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)
	entityID := uuid.New()

	if err := db.RunInTx(context.Background(), func(txCtx context.Context) error {
		insertCustomerMutation(t, db, txCtx)
		logger.Log(txCtx, audit.Entry{
			Action:     "customer.mutated",
			EntityType: "customer",
			EntityID:   entityID,
		})
		return nil
	}); err != nil {
		t.Fatalf("transaction: %v", err)
	}

	rows := fetchRows(t, db, entityID)
	if len(rows) != 1 {
		t.Fatalf("committed mutation wrote %d audit row(s), want exactly 1", len(rows))
	}
}

func TestLog_PoolPathIsSynchronous(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)
	entityID := uuid.New()

	// No transaction in the context: the write goes to the pool, and it must
	// be visible the moment Log returns — a caller with no transaction gets
	// its row and its errors immediately, never "later, maybe".
	logger.Log(context.Background(), audit.Entry{
		Action:     "pool.path",
		EntityType: "pool_test",
		EntityID:   entityID,
	})

	if rows := fetchRows(t, db, entityID); len(rows) != 1 {
		t.Fatalf("audit row visible immediately after Log returned: %d row(s), want 1 (write must be synchronous)", len(rows))
	}
}

func TestLog_PoolPathSurvivesCancelledRequestContext(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)
	entityID := uuid.New()

	// The shape the review flagged: the mutation has committed, the client
	// then disconnects, and the request context is cancelled by the time the
	// after-commit audit write runs. With no transaction in ctx there is
	// nothing left to roll back, so the row must still land — writing it with
	// the dead request context would drop it and orphan the committed
	// mutation (till close, module grant, app toggles).
	claims := &middleware.UserClaims{}
	claims.Subject = "user-321"
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), middleware.UserContextKey, claims))
	cancel()

	if err := logger.Log(ctx, audit.Entry{
		Action:     "till.closed",
		EntityType: "cancelled_ctx_test",
		EntityID:   entityID,
	}); err != nil {
		t.Fatalf("Log with a cancelled request context: %v", err)
	}

	rows := fetchRows(t, db, entityID)
	if len(rows) != 1 {
		t.Fatalf("audit rows after Log with a cancelled context = %d, want 1 (the pool path must not inherit the request's cancellation)", len(rows))
	}
	if rows[0].ActorID == nil || *rows[0].ActorID != "user-321" {
		t.Errorf("actor_id = %v, want user-321 (values must survive the WithoutCancel wrap)", rows[0].ActorID)
	}
}

func TestLog_ActorIDIsResolvedActor_UserIDKeepsLegacyOverride(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)
	entityID := uuid.New()

	// Some callers (OverrideExposure, the pricing exposure adapter) pass an
	// explicit entry.UserID. That value is a legacy attribution and stays in
	// user_id; actor_id records what pkg/actor resolved for the request, so a
	// key or agent row can never carry a different actor_id than its kind
	// claims.
	claims := &middleware.UserClaims{}
	claims.Subject = "user-123"
	ctx := context.WithValue(context.Background(), middleware.UserContextKey, claims)

	logger.Log(ctx, audit.Entry{
		Action:     "exposure.overridden",
		EntityType: "override_test",
		EntityID:   entityID,
		UserID:     "legacy-attribution-9",
	})

	rows := fetchRows(t, db, entityID)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.ActorID == nil || *r.ActorID != "user-123" {
		t.Errorf("actor_id = %v, want the resolved actor user-123, never the legacy override", r.ActorID)
	}
	if r.UserID == nil || *r.UserID != "legacy-attribution-9" {
		t.Errorf("user_id = %v, want the legacy override legacy-attribution-9", r.UserID)
	}
}

func TestLog_UnattributedCallRecordsAnonymous(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)
	entityID := uuid.New()

	// No claims, no key, no marker (dev mode, background jobs): the row is
	// anonymous, not a phantom user — kind 'anonymous', null actor_id.
	logger.Log(context.Background(), audit.Entry{
		Action:     "system.swept",
		EntityType: "anonymous_test",
		EntityID:   entityID,
	})

	rows := fetchRows(t, db, entityID)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.ActorKind != actor.KindAnonymous {
		t.Errorf("actor_kind = %q, want %q", r.ActorKind, actor.KindAnonymous)
	}
	if r.ActorID != nil {
		t.Errorf("actor_id = %v, want NULL (no identity to record)", r.ActorID)
	}
}

func TestLog_UserCallRecordsSubject(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)
	entityID := uuid.New()

	claims := &middleware.UserClaims{}
	claims.Subject = "user-123"
	ctx := context.WithValue(context.Background(), middleware.UserContextKey, claims)

	logger.Log(ctx, audit.Entry{
		Action:     "user.action",
		EntityType: "user_test",
		EntityID:   entityID,
	})

	rows := fetchRows(t, db, entityID)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.ActorKind != actor.KindUser {
		t.Errorf("actor_kind = %q, want %q", r.ActorKind, actor.KindUser)
	}
	if r.ActorID == nil || *r.ActorID != "user-123" {
		t.Errorf("actor_id = %v, want the JWT subject user-123", r.ActorID)
	}
	if r.UserID == nil || *r.UserID != "user-123" {
		t.Errorf("user_id = %v, want the JWT subject user-123", r.UserID)
	}
}

func TestLog_KeyCallRecordsKeyID(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)
	entityID := uuid.New()

	// The key id arrives as a context value set by the scoped-key middleware
	// (R1-13); the test sets it directly.
	ctx := actor.WithKeyID(context.Background(), "key-456")

	logger.Log(ctx, audit.Entry{
		Action:     "key.action",
		EntityType: "key_test",
		EntityID:   entityID,
	})

	rows := fetchRows(t, db, entityID)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.ActorKind != actor.KindKey {
		t.Errorf("actor_kind = %q, want %q", r.ActorKind, actor.KindKey)
	}
	if r.ActorID == nil || *r.ActorID != "key-456" {
		t.Errorf("actor_id = %v, want the key id key-456", r.ActorID)
	}
	// user_id meant "a user" until actor_kind existed; a key id in it would
	// list machine keys among users in every legacy report grouping by it.
	// The key id lives in actor_id only.
	if r.UserID != nil {
		t.Errorf("user_id = %v, want NULL (a key id never lands in the legacy user column)", *r.UserID)
	}
}

func TestLog_AgentCallRecordsUserMarkerAndTool(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)
	entityID := uuid.New()

	// The request carries the agent headers plus the acted-for user's token.
	var reqCtx context.Context
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
	req.Header.Set(actor.HeaderActingAs, "agent")
	req.Header.Set(actor.HeaderAgentTool, "quote.create")
	handler := actor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCtx = r.Context()
	}))
	handler.ServeHTTP(rec, req)

	claims := &middleware.UserClaims{}
	claims.Subject = "user-789"
	ctx := context.WithValue(reqCtx, middleware.UserContextKey, claims)

	logger.Log(ctx, audit.Entry{
		Action:     "agent.action",
		EntityType: "agent_test",
		EntityID:   entityID,
	})

	rows := fetchRows(t, db, entityID)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.ActorKind != actor.KindAgent {
		t.Errorf("actor_kind = %q, want %q", r.ActorKind, actor.KindAgent)
	}
	if r.ActorID == nil || *r.ActorID != "user-789" {
		t.Errorf("actor_id = %v, want the acted-for user's subject user-789", r.ActorID)
	}
	if r.ActingAs == nil || *r.ActingAs != "agent" {
		t.Errorf("acting_as = %v, want the marker %q", r.ActingAs, "agent")
	}
	if r.Tool == nil || *r.Tool != "quote.create" {
		t.Errorf("tool = %v, want quote.create", r.Tool)
	}
	if r.UserID == nil || *r.UserID != "user-789" {
		t.Errorf("user_id = %v, want the acted-for user's subject user-789", r.UserID)
	}
}
