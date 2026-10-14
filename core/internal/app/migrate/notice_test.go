// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package migrate

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestNoticeHandler_PrintsPrefixedNotice proves the notice handler that
// the migrate pool installs writes the notice body, prefixed with the
// migration file base name, to the destination writer (PR 70 review
// round 7 N1). The handler is the same one the live runner uses: the
// test pins the contract that an operator running migrate sees a NOTICE
// raised by the migration body.
func TestNoticeHandler_PrintsPrefixedNotice(t *testing.T) {
	var buf bytes.Buffer
	setNoticeWriter(&buf)
	defer setNoticeWriter(nil)

	setTxNoticeHandler("104_delivery_wire_contract.sql")
	defer resetNoticeHandler()

	handler := noticeHandler()
	handler(nil, &pgconn.Notice{Message: "migration 104: deliveries id=x old=old new=DELIVERED"})

	out := buf.String()
	if !strings.Contains(out, "[104_delivery_wire_contract.sql] NOTICE:") {
		t.Errorf("handler did not prefix the message with the file base: %q", out)
	}
	if !strings.Contains(out, "migration 104: deliveries id=x old=old new=DELIVERED") {
		t.Errorf("handler dropped the message body: %q", out)
	}
}

// TestNoticeHandler_PrefixesChangeWithBase proves the base the migrate
// loop sets just before the transaction runs is the one the notice is
// tagged with, so a NOTICE raised inside migration 103 carries the 103
// file base even when migration 104 also raises notices.
func TestNoticeHandler_PrefixesChangeWithBase(t *testing.T) {
	var buf bytes.Buffer
	setNoticeWriter(&buf)
	defer setNoticeWriter(nil)

	handler := noticeHandler()
	setTxNoticeHandler("103_drafts_links_scopes.sql")
	handler(nil, &pgconn.Notice{Message: "api key x (prefix p) carries a scope outside the grant grammar"})
	setTxNoticeHandler("104_delivery_wire_contract.sql")
	handler(nil, &pgconn.Notice{Message: "migration 104: deliveries id=y old=z new=DELIVERED"})

	out := buf.String()
	if !strings.Contains(out, "[103_drafts_links_scopes.sql] NOTICE: api key x") {
		t.Errorf("first notice was not prefixed with 103: %q", out)
	}
	if !strings.Contains(out, "[104_delivery_wire_contract.sql] NOTICE: migration 104") {
		t.Errorf("second notice was not prefixed with 104: %q", out)
	}
}

// TestNoticeHandler_NilWriterIsNoop proves the handler tolerates a nil
// destination writer (the runner sets it to os.Stdout, but the pool
// config is built before Run writes it).
func TestNoticeHandler_NilWriterIsNoop(t *testing.T) {
	setNoticeWriter(nil)
	handler := noticeHandler()
	handler(nil, &pgconn.Notice{Message: "x"})
}

// TestNoticeHandler_NilNoticeIgnored proves the handler does not panic
// when the server sends a nil notice (defensive; pgx should not but the
// contract here is that the migrate runner survives a misbehaving server).
func TestNoticeHandler_NilNoticeIgnored(t *testing.T) {
	var buf bytes.Buffer
	setNoticeWriter(&buf)
	handler := noticeHandler()
	handler(nil, nil)
	if buf.Len() != 0 {
		t.Errorf("nil notice wrote %q", buf.String())
	}
}

// TestNoticeHandler_ConcurrentSetters do not race (race detector catches
// a missing lock on noticeFileBase).
func TestNoticeHandler_ConcurrentSetters(t *testing.T) {
	var buf safeBuffer
	setNoticeWriter(&buf)
	defer setNoticeWriter(nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			setTxNoticeHandler("migration_" + string(rune('a'+i%26)) + ".sql")
			handler := noticeHandler()
			handler(nil, &pgconn.Notice{Message: "ok"})
		}(i)
	}
	wg.Wait()
	if !strings.Contains(buf.String(), "NOTICE: ok") {
		t.Errorf("no notice printed: %q", buf.String())
	}
}

// safeBuffer wraps bytes.Buffer with a mutex so concurrent writers do
// not race (the underlying buffer is not thread-safe; the test sets up
// the race, the production code never does).
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

var _ io.Writer = (*safeBuffer)(nil)
