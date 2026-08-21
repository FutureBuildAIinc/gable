// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl

import (
	"context"
	"errors"
	"testing"

	"github.com/gablelbm/gable/internal/domain"
)

// The QBO adapter is a stub. The contract that matters is that it says so at
// every entry point: a SyncCheck that returns nil in front of a
// PostJournalEntry that always errors advertises a healthy GL integration
// that posts nothing. Pinned so the stub cannot drift back to "healthy"
// without someone implementing the API call.

func TestQBOSyncCheckReportsUnimplemented(t *testing.T) {
	err := NewQBOAdapter("realm-123").SyncCheck(context.Background())
	if err == nil {
		t.Fatal("SyncCheck returned nil: the unimplemented QBO adapter must fail its own health check")
	}
	if !errors.Is(err, errQBONotImplemented) {
		t.Errorf("SyncCheck error = %v, want one wrapping errQBONotImplemented", err)
	}
}

func TestQBOPostJournalEntryReportsUnimplemented(t *testing.T) {
	id, err := NewQBOAdapter("realm-123").PostJournalEntry(context.Background(), domain.JournalEntry{ReferenceID: "ref-1"})
	if err == nil {
		t.Fatal("PostJournalEntry returned nil error for an unimplemented adapter")
	}
	if !errors.Is(err, errQBONotImplemented) {
		t.Errorf("PostJournalEntry error = %v, want one wrapping errQBONotImplemented", err)
	}
	if id != "" {
		t.Errorf("PostJournalEntry returned external id %q on failure, want empty", id)
	}
}

// The mock exists to make the GL path exercisable in tests and demos, so
// unlike the QBO stub it is legitimately healthy — it really does "post".
func TestMockAdapterIsHealthyAndPosts(t *testing.T) {
	m := NewMockGLAdapter()
	if err := m.SyncCheck(context.Background()); err != nil {
		t.Errorf("MockGLAdapter.SyncCheck = %v, want nil", err)
	}
	id, err := m.PostJournalEntry(context.Background(), domain.JournalEntry{ReferenceID: "ref-1"})
	if err != nil || id == "" {
		t.Errorf("MockGLAdapter.PostJournalEntry = (%q, %v), want a non-empty id and nil error", id, err)
	}
}
