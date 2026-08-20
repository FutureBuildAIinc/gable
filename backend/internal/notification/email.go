// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package notification

import (
	"context"
	"fmt"
	"log/slog"
)

// EmailService defines the interface for sending emails.
//
// SendEmailWithAttachment is the general-purpose member: SendInvoice and
// SendDeliveryNotification are the two fixed-shape messages the ERP sends,
// while scheduled reports need an arbitrary subject, body and file. It also
// satisfies reporting.EmailSender structurally, which is how the report
// scheduler is wired in cmd/server/main.go.
type EmailService interface {
	SendInvoice(ctx context.Context, to string, invoiceID string, pdfBytes []byte) error
	SendDeliveryNotification(ctx context.Context, to string, subject string, body string) error
	SendEmailWithAttachment(ctx context.Context, to []string, subject string, body string, filename string, content []byte) error
}

// LogEmailService is a dev/demo fallback that logs emails instead of sending.
//
// It is the ONLY EmailService implementation in this repository, and
// cmd/server/main.go wires it unconditionally — so every email this
// application "sends" (invoices, delivery notifications, scheduled reports) is
// a structured log line and nothing leaves the process. That is a deliberate
// state, not an oversight: there is no SMTP/SendGrid configuration anywhere in
// the tree, and a fallback that silently dropped mail without a trace would be
// worse than one that records exactly what it would have sent.
//
// To deliver for real, add an SMTP- or provider-backed type implementing
// EmailService and swap the single `notification.NewLogEmailService(logger)`
// call in cmd/server/main.go for it. Nothing else has to change: every consumer
// (document.Handler, DeliveryNotifier, ExposureNotifier, reporting.Scheduler)
// depends on the interface, not on this type.
type LogEmailService struct {
	logger *slog.Logger
}

// NewLogEmailService creates a logging-only email service.
func NewLogEmailService(logger *slog.Logger) *LogEmailService {
	return &LogEmailService{logger: logger}
}

func (s *LogEmailService) SendInvoice(ctx context.Context, to string, invoiceID string, pdfBytes []byte) error {
	s.logger.Info("MOCK EMAIL SENT",
		"to", to,
		"subject", fmt.Sprintf("Invoice #%s", invoiceID),
		"attachment_size", len(pdfBytes),
		"body", "Please find your invoice attached.",
	)
	return nil
}

func (s *LogEmailService) SendDeliveryNotification(ctx context.Context, to string, subject string, body string) error {
	s.logger.Info("MOCK DELIVERY EMAIL SENT",
		"to", to,
		"subject", subject,
		"body", body,
	)
	return nil
}

// SendEmailWithAttachment logs an email carrying a generated file — today the
// scheduled reports produced by reporting.Scheduler.
//
// What is real and what is not: the caller's work IS real. A scheduled report
// really is queried out of Postgres and really is rendered to CSV bytes, and
// this method receives those bytes and records their size. Only the transport
// is simulated, exactly as it is for invoices and delivery notifications above.
// Recipients, subject, filename and byte count are logged so an operator can
// confirm from the log that the right report went to the right people; the file
// itself is not written anywhere.
//
// Swapping in real SMTP is a matter of building a MIME multipart/mixed message
// (text/plain body part + base64 application/octet-stream attachment part named
// `filename`) and handing it to net/smtp or a provider SDK, plus configuration
// for host/port/credentials/From. See the LogEmailService doc comment for where
// the swap happens.
func (s *LogEmailService) SendEmailWithAttachment(ctx context.Context, to []string, subject string, body string, filename string, content []byte) error {
	s.logger.Info("MOCK EMAIL WITH ATTACHMENT SENT",
		"to", to,
		"recipient_count", len(to),
		"subject", subject,
		"body", body,
		"attachment_filename", filename,
		"attachment_size", len(content),
	)
	return nil
}

// DeliveryDescription states, in one line fit for display to an operator, what
// actually happens to an email handed to this service.
//
// It exists so that surfaces which advertise their own reliability — see
// reporting.ScheduleExecution — can derive that claim from the email service
// that is really wired, instead of hard-coding a sentence that goes stale the
// moment someone swaps in SMTP. A real sender simply does not implement this
// method and the generic description is used instead.
func (s *LogEmailService) DeliveryDescription() string {
	return "Email is log-only in this deployment: the report is generated and handed to notification.LogEmailService, " +
		"which records the recipients, subject, filename and size in the server log instead of sending. " +
		"Replace the NewLogEmailService call in cmd/server/main.go with an SMTP-backed EmailService to deliver for real."
}
