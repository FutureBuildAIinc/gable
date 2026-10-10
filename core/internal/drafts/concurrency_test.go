// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts_test

// The transaction proofs of ADR 0007 section 11's drafts core row, at pool
// size 4 with three contenders (the recipe's gated saturation shape: a
// transaction that reaches for a second pool connection deadlocks the test
// instead of passing on a roomy pool): three writers on one revision have
// one winner, a promotion racing edits has exactly one winner, three
// promoters produce one promotion, and the creates and transitions
// saturate the pool without a second connection.

import (
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// TestConcurrency_ThreeWritersOneRevision pins the co-editing protocol's
// server half (section 2.5): three writers built on revision n produce
// exactly one winner, and the draft moves exactly one revision.
func TestConcurrency_ThreeWritersOneRevision(t *testing.T) {
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	id := f.create()

	results := make(chan int, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload(), "revision": 1})
			results <- r.status
		}()
	}
	wg.Wait()
	close(results)
	var winners, stale int
	for status := range results {
		switch status {
		case http.StatusOK:
			winners++
		case http.StatusConflict:
			stale++
		default:
			t.Errorf("a racing writer answered %d", status)
		}
	}
	if winners != 1 || stale != 2 {
		t.Errorf("three writers on one revision: %d winners, %d refused; want one and two", winners, stale)
	}
	got := f.do("GET", "/api/v1/drafts/quotes/"+id, nil)
	if num(t, got.body, "revision") != 2 {
		t.Errorf("revision = %v, want 2 (one winner moved it once)", got.body["revision"])
	}
}

// TestConcurrency_PromotionRacingEdits pins one winner across the kinds of
// write: two payload saves and one promotion, all built on revision 1, put
// exactly one act through and move the draft exactly one revision.
func TestConcurrency_PromotionRacingEdits(t *testing.T) {
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	id := f.create()

	results := make(chan int, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var r resp
			switch i {
			case 0, 1:
				r = f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload(), "revision": 1})
			case 2:
				r = f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1})
			}
			results <- r.status
		}(i)
	}
	wg.Wait()
	close(results)
	var winners int
	for status := range results {
		if status == http.StatusOK || status == http.StatusCreated {
			winners++
		} else if status != http.StatusConflict {
			t.Errorf("a racing contender answered %d", status)
		}
	}
	if winners != 1 {
		t.Errorf("a promotion racing two edits: %d winners, want exactly one", winners)
	}
	got := f.do("GET", "/api/v1/drafts/quotes/"+id, nil)
	if num(t, got.body, "revision") != 2 {
		t.Errorf("revision = %v, want 2 (exactly one winner moved it once)", got.body["revision"])
	}
}

// TestConcurrency_ThreePromoters pins the promotion's own race: three
// confirmations of one revision produce exactly one quote; the losers are
// told the draft already went (or that their revision is stale, whichever
// lands first).
func TestConcurrency_ThreePromoters(t *testing.T) {
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	id := f.create()

	results := make(chan int, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1})
			results <- r.status
		}()
	}
	wg.Wait()
	close(results)
	var created, refused int
	for status := range results {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			refused++
		default:
			t.Errorf("a racing promoter answered %d", status)
		}
	}
	if created != 1 || refused != 2 {
		t.Errorf("three promoters: %d created, %d refused; want one and two", created, refused)
	}
}

// TestConcurrency_CreateAndTransitionSaturation pins the pool rule on the
// writes that do not share one row: three concurrent creates all land (each
// one transaction, one connection), and three concurrent transitions of one
// revision move the draft once, to discarded, with the losers told why.
func TestConcurrency_CreateAndTransitionSaturation(t *testing.T) {
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))

	// Three concurrent creates: all succeed, each its own transaction.
	results := make(chan int, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()})
			results <- r.status
		}()
	}
	wg.Wait()
	close(results)
	for status := range results {
		if status != http.StatusCreated {
			t.Errorf("a racing create answered %d", status)
		}
	}

	// Three concurrent transitions of one draft at one revision: one
	// discard wins.
	id := f.create()
	results2 := make(chan int, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.do("POST", "/api/v1/drafts/quotes/"+id+"/transitions", map[string]any{"to": "discarded", "revision": 1})
			results2 <- r.status
		}()
	}
	wg.Wait()
	close(results2)
	var winners int
	for status := range results2 {
		if status == http.StatusOK {
			winners++
		} else if status != http.StatusConflict {
			t.Errorf("a racing transition answered %d", status)
		}
	}
	if winners != 1 {
		t.Errorf("three transitions of one revision: %d winners, want one", winners)
	}
	got := f.do("GET", "/api/v1/drafts/quotes/"+id, nil)
	if str(t, got.body, "status") != "discarded" || num(t, got.body, "revision") != 2 {
		t.Errorf("the draft after the race = %v %v, want discarded at 2", got.body["status"], got.body["revision"])
	}
}
