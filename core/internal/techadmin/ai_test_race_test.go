// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/techadmin"
)

// orderedTester answers its first call only once released (the save whose test
// is slow) and refuses every later call (the save that came after it).
type orderedTester struct {
	calls   int32
	started chan struct{}
	release chan struct{}
}

func (o *orderedTester) TestConnection(context.Context) error {
	if atomic.AddInt32(&o.calls, 1) == 1 {
		close(o.started)
		<-o.release
		return nil
	}
	return &ai.RefusedError{Provider: "Anthropic", Detail: "stand-in refusal",
		Message: "Anthropic refused the saved key. An administrator can enter it again in Tech Admin, AI Settings."}
}

// RULE (N7): two saves whose tests finish out of order never leave the older
// key's pass standing over the newer key's refusal. Save A's test is slow and
// passes; save B commits after it and its test is refused; A's test then ends.
// The last save's key reads refused.
func TestWire_TwoSavesWhoseTestsFinishOutOfOrderReadTheLastKeysResult(t *testing.T) {
	tester := &orderedTester{started: make(chan struct{}), release: make(chan struct{})}
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service {
		return s.WithVault(testVault(t)).WithAITester(tester)
	})
	ctx := context.Background()
	cur, err := f.svc.GetAISettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rev := cur.Revision

	var wg sync.WaitGroup
	wg.Add(1)
	var aErr error
	go func() {
		defer wg.Done()
		_, aErr = f.svc.SaveAISettingsFull(ctx,
			techadmin.AISave{Provider: "anthropic", AnthropicAPIKey: fakeAnthropicKey},
			techadmin.Precondition{Revision: &rev})
	}()
	<-tester.started // A has committed (revision rev+1) and its test is in flight

	next := rev + 1
	if _, err := f.svc.SaveAISettingsFull(ctx,
		techadmin.AISave{Provider: "anthropic", AnthropicAPIKey: fakeAnthropicKey + "b"},
		techadmin.Precondition{Revision: &next}); err != nil {
		t.Fatalf("save B: %v", err)
	}
	close(tester.release) // A's test ends last
	wg.Wait()
	if aErr != nil {
		t.Fatalf("save A: %v", aErr)
	}

	got := f.do("GET", "/api/v1/admin/settings/ai", nil)
	if got.status != http.StatusOK || got.body["test_status"] != "refused" {
		t.Fatalf("the last save's key reads %v, want refused: %s", got.body["test_status"], got.raw)
	}
	if strings.Contains(string(got.raw), fakeAnthropicKey) {
		t.Error("the document holds a key")
	}
}

// A pressed Test connection against a provider that cannot be reached does not
// say the key is saved (only a save says that): it says the provider could not
// be reached and to try again later.
func TestWire_APressedTestOnAnUnreachableProviderDoesNotSayTheKeyIsSaved(t *testing.T) {
	client := unreachableClient(t)
	f := newFixtureWith(t, func(s *techadmin.Service) *techadmin.Service {
		return s.WithVault(testVault(t)).WithAITester(client)
	})
	r := f.do("POST", "/api/v1/admin/settings/ai/test", nil)
	if r.status != http.StatusOK {
		t.Fatalf("test = %d: %s", r.status, r.raw)
	}
	msg, _ := r.body["message"].(string)
	if want := "Anthropic could not be reached; try Test connection again later."; msg != want {
		t.Errorf("message = %q, want %q", msg, want)
	}
}
