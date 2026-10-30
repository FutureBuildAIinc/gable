// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package emaildelivery_test

// CP-14: the logged status on the wire. A send the log sender took (no SMTP
// on this server) records logged and never sent, and the list filter status
// accepts logged and queued beside sent and failed.

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestCP14_ListFilterLogged(t *testing.T) {
	f := newFixture(t)
	b := f.branch()
	tag := uuid.NewString()[:8]
	f.record(&b, "invoice", "log-"+tag+"@example.test", "logged")
	f.record(&b, "invoice", "ok-"+tag+"@example.test", "sent")
	f.record(&b, "invoice", "bad-"+tag+"@example.test", "failed")
	f.record(&b, "invoice", "wait-"+tag+"@example.test", "queued")

	code, body, raw := f.get("/api/v1/email-deliveries?status=logged&limit=200")
	if code != http.StatusOK {
		t.Fatalf("list logged: %d %s", code, raw)
	}
	found := 0
	for _, it := range body["items"].([]any) {
		row := it.(map[string]any)
		if row["recipient"] == "log-"+tag+"@example.test" {
			found++
			if row["status"] != "logged" {
				t.Errorf("row status = %v, want logged: %s", row["status"], raw)
			}
		}
		if row["recipient"] == "ok-"+tag+"@example.test" || row["recipient"] == "bad-"+tag+"@example.test" {
			t.Errorf("status=logged returned %v", row["recipient"])
		}
	}
	if found != 1 {
		t.Errorf("status=logged found the logged row %d times, want once: %s", found, raw)
	}

	// queued is on the wire vocabulary too: the row a send has not resolved.
	if code, _, raw := f.get("/api/v1/email-deliveries?status=queued&limit=200"); code != http.StatusOK {
		t.Errorf("list queued: %d %s, want 200", code, raw)
	}
	// a status outside the vocabulary is still the 400 of ADR 0001.
	if code, _, raw := f.get("/api/v1/email-deliveries?status=mailed"); code != http.StatusBadRequest {
		t.Errorf("status=mailed: %d %s, want 400", code, raw)
	}
}
