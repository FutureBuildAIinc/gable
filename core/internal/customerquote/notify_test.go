// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/gablelbm/gable/internal/notification"
)

// realMail is an email sender that is not the log only one: it has no
// DeliveryDescription, as the SMTP sender has none.
type realMail struct{ bodies []string }

func (r *realMail) SendInvoice(context.Context, string, string, []byte) error { return nil }
func (r *realMail) SendDeliveryNotification(_ context.Context, _ string, _ string, body string) error {
	r.bodies = append(r.bodies, body)
	return nil
}
func (r *realMail) SendEmailWithAttachment(context.Context, []string, string, string, string, []byte) error {
	return nil
}

func TestMailNotifier_ALogOnlySenderNeverSeesTheLink(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	link := "https://quotes.example/q/" + strings.Repeat("Ab1_", 10) + "xyz"
	n := customerquote.NewMailNotifier(notification.NewLogEmailService(logger), customerquote.NewStubSMS(logger), logger)
	delivered, err := n.SendLink(context.Background(), customerquote.Contact{Name: "Pat", Email: "pat@homeowner.example"}, "Wire Builders", "Deck", link)
	if err != nil || delivered {
		t.Fatalf("a log only sender: delivered=%v err=%v", delivered, err)
	}
	if strings.Contains(logs.String(), link) || strings.Contains(logs.String(), "Ab1_Ab1_") {
		t.Fatalf("the link reached the log:\n%s", logs.String())
	}

	// A real sender is handed the link.
	rm := &realMail{}
	n = customerquote.NewMailNotifier(rm, customerquote.NewStubSMS(logger), logger)
	delivered, err = n.SendLink(context.Background(), customerquote.Contact{Email: "pat@homeowner.example"}, "Wire Builders", "Deck", link)
	if err != nil || !delivered || len(rm.bodies) != 1 || !strings.Contains(rm.bodies[0], link) {
		t.Fatalf("a real sender: delivered=%v err=%v bodies=%v", delivered, err, rm.bodies)
	}
	// No address, nothing to send.
	if ok, _ := n.SendLink(context.Background(), customerquote.Contact{Name: "Pat"}, "Wire Builders", "Deck", link); ok {
		t.Error("a link was reported delivered with no address")
	}
}

func TestMailNotifier_TheSmsStubSendsNothingAndLogsNoCode(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	sms := customerquote.NewStubSMS(logger)
	n := customerquote.NewMailNotifier(&realMail{}, sms, logger)
	if err := n.SendCode(context.Background(), "sms", "555 010 2020", "Wire Builders", "482913"); err != nil {
		t.Fatal(err)
	}
	m, ok := sms.Last()
	if !ok || !strings.Contains(m.Body, "482913") || m.To != "555 010 2020" {
		t.Fatalf("the stub's last message: %+v %v", m, ok)
	}
	if strings.Contains(logs.String(), "482913") || strings.Contains(logs.String(), "555 010") {
		t.Errorf("the stub logged the code or the number:\n%s", logs.String())
	}
	if err := n.SendCode(context.Background(), "carrier pigeon", "x", "y", "123456"); err == nil {
		t.Error("an unknown channel was accepted")
	}
}
