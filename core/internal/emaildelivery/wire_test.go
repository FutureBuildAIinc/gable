// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package emaildelivery_test

// The delivery record on the wire (ADR 0001): the list envelope with its
// cursor walk, its filters, the unknown parameter refusal, and the branch
// wall (a caller held to one branch reads only that branch's rows).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gablelbm/gable/internal/emaildelivery"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

type fixture struct {
	t     *testing.T
	db    *database.DB
	svc   *emaildelivery.Service
	mux   http.Handler
	wallA *uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	svc := emaildelivery.NewService(emaildelivery.NewRepository(db))
	mux := http.NewServeMux()
	emaildelivery.NewHandler(svc).RegisterRoutes(mux, nil)
	f := &fixture{t: t, db: db, svc: svc}
	f.mux = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.wallA != nil {
			r = r.WithContext(branchctx.With(r.Context(), &branchctx.Context{BranchID: f.wallA, UserSub: "u-wall"}))
		}
		mux.ServeHTTP(w, r)
	})
	return f
}

func (f *fixture) branch() uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	code := "ED-" + uuid.NewString()[:8]
	if err := f.db.Pool.QueryRow(context.Background(),
		`INSERT INTO locations (path, type, code, name) VALUES ($1, 'BRANCH', $1, $1) RETURNING id`, code).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM email_deliveries WHERE branch_id = $1`, id)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, id)
	})
	return id
}

func (f *fixture) get(path string) (int, map[string]any, []byte) {
	f.t.Helper()
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	raw, _ := io.ReadAll(rec.Result().Body)
	var body map[string]any
	dec := json.NewDecoder(httptestReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&body)
	return rec.Code, body, raw
}

func (f *fixture) record(branch *uuid.UUID, docType, recipient, status string) emaildelivery.Delivery {
	f.t.Helper()
	d, err := f.svc.Record(context.Background(), emaildelivery.Entry{
		BranchID: branch, Recipient: recipient, Subject: "Subject for " + recipient,
		DocumentType: docType, DocumentNumber: "N-1", Status: status, SenderAnswer: "250 ok",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return *d
}

func TestEmailDeliveriesListAndFilters(t *testing.T) {
	f := newFixture(t)
	b := f.branch()
	tag := uuid.NewString()[:8] + "@example.test"
	f.record(&b, "invoice", "inv-"+tag, "sent")
	f.record(&b, "statement", "stm-"+tag, "sent")
	f.record(&b, "statement", "stf-"+tag, "failed")

	code, body, raw := f.get("/api/v1/email-deliveries?document_type=statement&limit=1&include=total")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, raw)
	}
	items := body["items"].([]any)
	if len(items) != 1 || body["next_cursor"] == nil || body["total"] == nil {
		t.Fatalf("envelope wrong: %s", raw)
	}
	row := items[0].(map[string]any)
	for _, k := range []string{"id", "created_at", "branch_id", "recipient", "subject", "document_type", "document_id", "document_number", "status", "sender_answer", "error", "sent_by"} {
		if _, ok := row[k]; !ok {
			t.Errorf("field %q missing: %s", k, raw)
		}
	}
	// walk the failed filter: exactly our row among the failed ones with our tag
	_, body, raw = f.get("/api/v1/email-deliveries?status=failed&limit=200")
	found := 0
	for _, it := range body["items"].([]any) {
		if it.(map[string]any)["recipient"] == "stf-"+tag {
			found++
		}
	}
	if found != 1 {
		t.Errorf("status filter did not find the failed row once: %s", raw)
	}
	for _, bad := range []string{"?bogus=1", "?document_type=Invoice", "?status=mailed", "?limit=0", "?cursor=zzz"} {
		if code, _, raw := f.get("/api/v1/email-deliveries" + bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", bad, code, raw)
		}
	}
}

func TestEmailDeliveriesBranchWall(t *testing.T) {
	f := newFixture(t)
	a, b := f.branch(), f.branch()
	tag := uuid.NewString()[:8]
	f.record(&a, "invoice", "a-"+tag+"@example.test", "sent")
	f.record(&b, "invoice", "b-"+tag+"@example.test", "sent")

	f.wallA = &a
	q := url.Values{"limit": {"200"}}.Encode()
	_, body, raw := f.get("/api/v1/email-deliveries?" + q)
	sawA, sawB := false, false
	for _, it := range body["items"].([]any) {
		switch it.(map[string]any)["recipient"] {
		case "a-" + tag + "@example.test":
			sawA = true
		case "b-" + tag + "@example.test":
			sawB = true
		}
	}
	if !sawA || sawB {
		t.Errorf("a caller held to branch A saw A=%v B=%v: %s", sawA, sawB, raw)
	}
}

func httptestReader(raw []byte) io.Reader { return bytes.NewReader(raw) }
