// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// postAfterFirstRead runs a hook (a competing post that commits) right after
// the first credit memo read that still sees a draft: the window between
// voidCredit's unlocked read and its lock.
type postAfterFirstRead struct {
	invoice.Store
	hook func()
}

func (p *postAfterFirstRead) GetCreditMemo(ctx context.Context, id uuid.UUID) (*invoice.CreditMemo, error) {
	cm, err := p.Store.GetCreditMemo(ctx, id)
	if err == nil && cm.Status == invoice.CreditDraft && p.hook != nil {
		h := p.hook
		p.hook = nil
		h()
	}
	return cm, err
}

// RULE (review F2): voidCredit decides the finance role and the customer's
// credit serialization from its unlocked read. A draft that a finance user's
// post commits between that read and the lock is no longer a draft: the void
// must not run on the open memo with the role check skipped, it answers a
// retryable 409 and leaves the memo posted.
func TestVoidOfADraftThatIsPostedMeanwhileIsRetryable(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, _ := f.invoice("10")
	line := f.firstLineID(invID)
	cm := f.createCredit(f.creditBody(invID, returnLine(line, "-2", false)))
	id := str(t, cm.body, "id")

	logger := slog.Default()
	glSvc := gl.NewService(gl.NewRepository(db), nil, logger)
	acct := account.NewService(db, glSvc, logger)
	racing := &postAfterFirstRead{Store: invoice.NewRepository(db), hook: func() {
		if r := f.postCredit(id, 1); r.status != http.StatusOK {
			t.Errorf("competing post = %d: %s", r.status, r.raw)
		}
	}}
	svc := invoice.NewService(racing, glSvc, acct, db).WithStock(inventory.NewService(inventory.NewRepository(db)))

	rev := int64(2) // the revision the posted memo carries
	_, err := svc.TransitionCreditMemo(context.Background(), uuid.MustParse(id), invoice.CreditVoid,
		invoice.Precondition{Revision: &rev}, invoice.Transition{Reason: "oops", Actor: "sales-user", Role: "sales"})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Fatalf("void of a draft posted meanwhile = %v, want a 409", err)
	}
	if got := f.do("GET", "/api/v1/credit-memos/"+id, nil); str(t, got.body, "status") != "open" {
		t.Errorf("memo is %s, want it left open", got.raw)
	}
}
