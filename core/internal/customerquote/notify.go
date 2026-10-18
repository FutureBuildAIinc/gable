// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/gablelbm/gable/internal/notification"
)

// SMSSender sends a text message. The stub below is what the service is
// wired with: there is no live SMS.
type SMSSender interface {
	SendSMS(ctx context.Context, to, body string) error
}

// StubSMS is the SMS sender with no carrier behind it. It keeps the messages
// in memory so a test or a local run can read the last one, and logs that a
// message was not sent, naming no number and no code.
type StubSMS struct {
	mu   sync.Mutex
	sent []SMSMessage
	log  *slog.Logger
}

// SMSMessage is one message the stub took.
type SMSMessage struct {
	To   string
	Body string
}

// NewStubSMS builds the stub.
func NewStubSMS(logger *slog.Logger) *StubSMS {
	if logger == nil {
		logger = slog.Default()
	}
	return &StubSMS{log: logger}
}

// SendSMS keeps the message and sends nothing.
func (s *StubSMS) SendSMS(_ context.Context, to, body string) error {
	s.mu.Lock()
	s.sent = append(s.sent, SMSMessage{To: to, Body: body})
	s.mu.Unlock()
	s.log.Info("sms stub: a message was not sent (no live sms)")
	return nil
}

// Last returns the most recent message the stub took.
func (s *StubSMS) Last() (SMSMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		return SMSMessage{}, false
	}
	return s.sent[len(s.sent)-1], true
}

// logOnly is the shape of the log only email sender (it alone describes
// itself); the real sender does not implement it.
type logOnly interface{ DeliveryDescription() string }

// MailNotifier sends the link and the code through Gable's email path and an
// SMS sender.
type MailNotifier struct {
	email notification.EmailService
	sms   SMSSender
	log   *slog.Logger
}

// NewMailNotifier builds the notifier.
func NewMailNotifier(email notification.EmailService, sms SMSSender, logger *slog.Logger) *MailNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &MailNotifier{email: email, sms: sms, log: logger}
}

// SendLink emails the capability link. A log only sender would write the
// link into the server log, and a link is a credential, so the link is not
// handed to it: nothing is sent and the contractor shares the link they were
// given.
func (m *MailNotifier) SendLink(ctx context.Context, to Contact, contractor, title, link string) (bool, error) {
	if m.email == nil || to.Email == "" {
		return false, nil
	}
	if _, isLog := m.email.(logOnly); isLog {
		m.log.Info("customer quote link was not emailed: the email sender is log only, and a link is never logged")
		return false, nil
	}
	subject := fmt.Sprintf("%s sent you a quote: %s", contractor, title)
	body := fmt.Sprintf("%s has sent you a quote to review and approve.\n\nOpen it here:\n%s\n\nThis link is private to you. Anyone who has it can see the quote.", contractor, link)
	if err := m.email.SendDeliveryNotification(ctx, to.Email, subject, body); err != nil {
		return false, err
	}
	return true, nil
}

// SendCode delivers the verification code. The code is single use and lives
// minutes; a log only email sender may record it, as it records every
// message it is given.
func (m *MailNotifier) SendCode(ctx context.Context, channel, destination, contractor, code string) error {
	switch channel {
	case "email":
		if m.email == nil {
			return errors.New("no email sender")
		}
		return m.email.SendDeliveryNotification(ctx, destination, contractor+": your verification code",
			fmt.Sprintf("Your verification code is %s. It works once and expires in %d minutes.", code, int(CodeTTL.Minutes())))
	case "sms":
		if m.sms == nil {
			return errors.New("no sms sender")
		}
		return m.sms.SendSMS(ctx, destination, fmt.Sprintf("%s verification code: %s", contractor, code))
	}
	return fmt.Errorf("channel %q", channel)
}
