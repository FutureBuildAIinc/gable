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
)

type recordedDeliveries struct {
	mu      sync.Mutex
	entries []emaildelivery.Entry
}

func (r *recordedDeliveries) Record(_ context.Context, e emaildelivery.Entry) (*emaildelivery.Delivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	return &emaildelivery.Delivery{}, nil
}

// A sender that accepts the connection and never speaks ends in an error
// within the send timeout, the delivery row records the failure once, and the
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
	rec.mu.Unlock()
	if len(entries) != 1 {
		t.Fatalf("delivery rows = %d, want exactly one", len(entries))
	}
	if entries[0].Status != emaildelivery.StatusFailed || entries[0].Error == "" {
		t.Errorf("row = %+v, want a failed row with the error", entries[0])
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
