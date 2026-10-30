// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"net"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/emaildelivery"
	"github.com/gablelbm/gable/internal/notification"
	"github.com/google/uuid"
)

// deliveryOutcome is the one outcome a send wrote to its queued row.
type deliveryOutcome struct {
	id     uuid.UUID
	status string
	answer string
	failed string
}

type recordedDeliveries struct {
	mu       sync.Mutex
	entries  []emaildelivery.Entry
	rows     []uuid.UUID
	outcomes []deliveryOutcome
}

func (r *recordedDeliveries) Record(_ context.Context, e emaildelivery.Entry) (*emaildelivery.Delivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := uuid.New()
	r.entries = append(r.entries, e)
	r.rows = append(r.rows, id)
	return &emaildelivery.Delivery{ID: id, Status: e.Status}, nil
}

func (r *recordedDeliveries) SetOutcome(_ context.Context, id uuid.UUID, status, answer, sendError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, deliveryOutcome{id: id, status: status, answer: answer, failed: sendError})
	return nil
}

// A sender that accepts the connection and never speaks ends in an error
// within the send timeout, the queued row is resolved failed once, and the
// goroutine that sent is gone.
func TestDispatchAHungSenderEndsInOneFailedRow(t *testing.T) {
	before := runtime.NumGoroutine()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
		done  = make(chan struct{})
	)
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()

	host, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	sender, err := notification.NewSMTPEmailService(notification.SMTPConfig{
		Host: host, Port: port, From: "d@example.test", TLS: notification.SMTPTLSNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordedDeliveries{}
	h := NewHandler(nil, nil, nil, nil, sender).WithDeliveries(rec)
	h.sendTimeout = 300 * time.Millisecond

	start := time.Now()
	h.dispatch(context.Background(), outgoing{
		to: "c@example.test", subject: "s", body: "b", filename: "r.pdf", pdf: []byte("%PDF"),
		entry: emaildelivery.Entry{DocumentType: "invoice"},
	})
	h.Wait()
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the hung send took %s; the timeout is 300ms", took)
	}

	rec.mu.Lock()
	entries := rec.entries
	outcomes := rec.outcomes
	rows := rec.rows
	rec.mu.Unlock()
	if len(entries) != 1 || len(rows) != 1 {
		t.Fatalf("delivery rows = %d, want exactly one queued row", len(rows))
	}
	if entries[0].Status != emaildelivery.StatusQueued {
		t.Errorf("the row written before the send = %q, want queued", entries[0].Status)
	}
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want exactly one", outcomes)
	}
	if outcomes[0].status != emaildelivery.StatusFailed || outcomes[0].failed == "" {
		t.Errorf("outcome = %+v, want a failed outcome with the error", outcomes[0])
	}
	if outcomes[0].id != rows[0] {
		t.Errorf("outcome id %s does not name the queued row %s", outcomes[0].id, rows[0])
	}

	ln.Close()
	<-done
	mu.Lock()
	for _, c := range conns {
		c.Close()
	}
	mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("goroutines: %d before, %d after; the send left one behind", before, n)
	}
}

// WaitContext is what shutdown calls: it returns true once the sends in
// flight have recorded, and false when the context ends first, so a hung send
// cannot hold the process past the shutdown deadline.
func TestWaitContextIsBounded(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil, nil)
	if !h.WaitContext(context.Background()) {
		t.Fatal("nothing in flight should be waited out at once")
	}
	h.sends.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if h.WaitContext(ctx) {
		t.Error("a send in flight was reported finished")
	}
	h.sends.Done()
	if !h.WaitContext(context.Background()) {
		t.Error("the finished send was not waited out")
	}
}
