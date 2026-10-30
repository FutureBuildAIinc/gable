// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

// CP-14: what a send actually did reaches the delivery record. A sender that
// only logs (the log sender, the default stack with no SMTP) records logged,
// never sent; the SMTP sender keeps sent, with the server's own answer. The
// row is written queued on the sending thread, then resolved once, so the 202
// can name it and the desk can follow it.

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/emaildelivery"
	"github.com/gablelbm/gable/internal/notification"
)

// scriptSMTP is a one message SMTP server on port 0 that answers the end of
// data with dataReply, the way a real server answers 250 with its own words.
func scriptSMTP(t *testing.T, dataReply string) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		r := bufio.NewReader(c)
		write := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		write("220 script.local ESMTP")
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					write(dataReply)
				}
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				write("250-script.local")
				write("250 8BITMIME")
			case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
				write("250 OK")
			case line == "DATA":
				write("354 end with a single dot")
				inData = true
			case line == "QUIT":
				write("221 bye")
				return
			}
		}
	}()
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
}

// The log sender is what a stack with no SMTP runs: the row is written queued,
// then resolved logged, never sent, with the sender's own disclosure as the
// answer and no error.
func TestCP14_LogSenderRecordsLogged(t *testing.T) {
	rec := &recordedDeliveries{}
	h := NewHandler(nil, nil, nil, nil, notification.NewLogEmailService(slog.New(slog.DiscardHandler))).WithDeliveries(rec)

	h.dispatch(context.Background(), outgoing{
		to: "c@example.test", subject: "s", body: "b", filename: "r.pdf", pdf: []byte("%PDF"),
		entry: emaildelivery.Entry{DocumentType: "invoice", DocumentNumber: "IN-1"},
	})
	h.Wait()

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.rows) != 1 || len(rec.entries) != 1 || rec.entries[0].Status != emaildelivery.StatusQueued {
		t.Fatalf("queued rows = %v, want exactly one queued row written before the send", rec.rows)
	}
	if len(rec.outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want exactly one", rec.outcomes)
	}
	o := rec.outcomes[0]
	if o.id != rec.rows[0] {
		t.Errorf("outcome id %s does not name the queued row %s", o.id, rec.rows[0])
	}
	if o.status != emaildelivery.StatusLogged {
		t.Errorf("log sender outcome status = %q, want logged", o.status)
	}
	if o.answer != "logged by the log sender, not sent" {
		t.Errorf("log sender outcome answer = %q, want the sender's own disclosure", o.answer)
	}
	if o.failed != "" {
		t.Errorf("log sender outcome error = %q, want none", o.failed)
	}
}

// The SMTP sender keeps sent and failed: a send the server accepts resolves
// sent with the server's own 250 answer as the sender answer.
func TestCP14_SMTPSenderRecordsSent(t *testing.T) {
	const serverAnswer = "250 2.0.0 Ok: queued as cp14proof"
	host, port := scriptSMTP(t, serverAnswer)
	sender, err := notification.NewSMTPEmailService(notification.SMTPConfig{
		Host: host, Port: port, From: "d@example.test", TLS: notification.SMTPTLSNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordedDeliveries{}
	h := NewHandler(nil, nil, nil, nil, sender).WithDeliveries(rec)

	h.dispatch(context.Background(), outgoing{
		to: "c@example.test", subject: "s", body: "b", filename: "r.pdf", pdf: []byte("%PDF"),
		entry: emaildelivery.Entry{DocumentType: "invoice", DocumentNumber: "IN-1"},
	})
	h.Wait()

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.rows) != 1 || len(rec.entries) != 1 || rec.entries[0].Status != emaildelivery.StatusQueued {
		t.Fatalf("queued rows = %v, want exactly one queued row written before the send", rec.rows)
	}
	if len(rec.outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want exactly one", rec.outcomes)
	}
	o := rec.outcomes[0]
	if o.id != rec.rows[0] {
		t.Errorf("outcome id %s does not name the queued row %s", o.id, rec.rows[0])
	}
	if o.status != emaildelivery.StatusSent {
		t.Errorf("smtp outcome status = %q, want sent", o.status)
	}
	if o.answer != serverAnswer {
		t.Errorf("smtp outcome answer = %q, want the server's own %q", o.answer, serverAnswer)
	}
	if o.failed != "" {
		t.Errorf("smtp outcome error = %q, want none", o.failed)
	}
}
