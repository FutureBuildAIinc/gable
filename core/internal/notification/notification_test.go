// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package notification

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Notifications are customer-facing: an SMS or email that names the wrong order
// or goes to the wrong number is visible to the dealer's customer. The Twilio
// client is exercised through an injected RoundTripper, so no network traffic
// leaves the test.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// --- test doubles --------------------------------------------------------

type capturedRequest struct {
	method  string
	url     string
	body    string
	headers http.Header
	user    string
	pass    string
	hasAuth bool
}

// roundTripper records the outbound request and replays a canned response.
type roundTripper struct {
	mu       sync.Mutex
	requests []capturedRequest
	status   int
	body     string
	err      error
}

func (rt *roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	// A real http.Transport aborts before dialling when the request's context
	// is already done. Reproducing that here is what makes the cancellation
	// test meaningful: it can only pass if the service attached the caller's
	// context to the request (http.NewRequestWithContext) rather than building
	// a context-free one.
	if err := r.Context().Err(); err != nil {
		return nil, err
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	var body string
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}
	user, pass, ok := r.BasicAuth()
	rt.requests = append(rt.requests, capturedRequest{
		method:  r.Method,
		url:     r.URL.String(),
		body:    body,
		headers: r.Header.Clone(),
		user:    user,
		pass:    pass,
		hasAuth: ok,
	})

	if rt.err != nil {
		return nil, rt.err
	}
	status := rt.status
	if status == 0 {
		status = http.StatusCreated
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

// newTwilioWithTransport builds the real service and swaps its HTTP client for
// one backed by rt. The client field is unexported, which is why this test
// lives in the package.
func newTwilioWithTransport(cfg TwilioConfig, rt http.RoundTripper) *TwilioSMSService {
	s := NewTwilioSMSService(cfg, testLogger())
	s.client = &http.Client{Transport: rt, Timeout: 10 * time.Second}
	return s
}

type recordingSMS struct {
	sent []struct{ to, body string }
	err  error
}

func (r *recordingSMS) SendSMS(_ context.Context, to, body string) error {
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, struct{ to, body string }{to, body})
	return nil
}

type recordingEmail struct {
	invoices []struct {
		to, invoiceID string
		size          int
	}
	deliveries  []struct{ to, subject, body string }
	attachments []struct {
		to                []string
		subject, filename string
		size              int
	}
	err error
}

func (r *recordingEmail) SendInvoice(_ context.Context, to, invoiceID string, pdf []byte) error {
	if r.err != nil {
		return r.err
	}
	r.invoices = append(r.invoices, struct {
		to, invoiceID string
		size          int
	}{to, invoiceID, len(pdf)})
	return nil
}

func (r *recordingEmail) SendDeliveryNotification(_ context.Context, to, subject, body string) error {
	if r.err != nil {
		return r.err
	}
	r.deliveries = append(r.deliveries, struct{ to, subject, body string }{to, subject, body})
	return nil
}

func (r *recordingEmail) SendEmailWithAttachment(_ context.Context, to []string, subject, _, filename string, content []byte) error {
	if r.err != nil {
		return r.err
	}
	r.attachments = append(r.attachments, struct {
		to                []string
		subject, filename string
		size              int
	}{to, subject, filename, len(content)})
	return nil
}

var (
	_ SMSService   = (*recordingSMS)(nil)
	_ EmailService = (*recordingEmail)(nil)
)

// --- Twilio SMS ----------------------------------------------------------

// CORRECTNESS: the request Twilio receives must be a form-encoded POST to the
// account's Messages endpoint, authenticated with the account SID and auth
// token, carrying the exact To/From/Body triple it was given.
func TestTwilioSendSMS_RequestShape(t *testing.T) {
	rt := &roundTripper{status: http.StatusCreated, body: `{"sid":"SM1"}`}
	cfg := TwilioConfig{AccountSID: "AC123", AuthToken: "tok-secret", FromNumber: "+15550001111"}
	svc := newTwilioWithTransport(cfg, rt)

	if err := svc.SendSMS(context.Background(), "+15559998888", "Your order #1001 is on the way"); err != nil {
		t.Fatalf("SendSMS: %v", err)
	}

	if len(rt.requests) != 1 {
		t.Fatalf("made %d HTTP requests, want 1", len(rt.requests))
	}
	req := rt.requests[0]

	if req.method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.method)
	}
	if want := "https://api.twilio.com/2010-04-01/Accounts/AC123/Messages.json"; req.url != want {
		t.Errorf("url = %s, want %s", req.url, want)
	}
	if got := req.headers.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", got)
	}
	if !req.hasAuth || req.user != "AC123" || req.pass != "tok-secret" {
		t.Errorf("basic auth = (%q, %q, ok=%v), want the account SID and auth token", req.user, req.pass, req.hasAuth)
	}

	form, err := url.ParseQuery(req.body)
	if err != nil {
		t.Fatalf("the body is not form-encoded: %v", err)
	}
	if got := form.Get("To"); got != "+15559998888" {
		t.Errorf("To = %q", got)
	}
	if got := form.Get("From"); got != "+15550001111" {
		t.Errorf("From = %q, want the configured sender", got)
	}
	if got := form.Get("Body"); got != "Your order #1001 is on the way" {
		t.Errorf("Body = %q", got)
	}
}

// CORRECTNESS: message bodies contain customer text. Ampersands, plus signs,
// newlines and non-ASCII must survive form encoding intact — a mangled body is
// what the customer reads.
func TestTwilioSendSMS_BodyEncoding(t *testing.T) {
	bodies := []string{
		"Order #1001 & #1002 are ready",
		"ETA 3:30 PM — dock 4",
		"Total: $1,234.56 (50% deposit)",
		"Line one\nLine two",
		"Café Ünïcode ✓",
		"a+b=c",
	}

	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			rt := &roundTripper{status: http.StatusCreated}
			svc := newTwilioWithTransport(TwilioConfig{AccountSID: "AC1", AuthToken: "t", FromNumber: "+1"}, rt)

			if err := svc.SendSMS(context.Background(), "+1555", body); err != nil {
				t.Fatalf("SendSMS: %v", err)
			}
			form, err := url.ParseQuery(rt.requests[0].body)
			if err != nil {
				t.Fatalf("parse body: %v", err)
			}
			if got := form.Get("Body"); got != body {
				t.Errorf("Body round-tripped as %q, want %q", got, body)
			}
		})
	}
}

// CORRECTNESS: a non-2xx response is a failure. Silently succeeding would mean
// the ERP records a notification the customer never received.
func TestTwilioSendSMS_NonSuccessStatusIsAnError(t *testing.T) {
	tests := []struct {
		status  int
		wantErr bool
	}{
		{http.StatusOK, false},
		{http.StatusCreated, false},
		{http.StatusNoContent, false},
		{http.StatusBadRequest, true},
		{http.StatusUnauthorized, true},
		{http.StatusForbidden, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusMovedPermanently, true}, // 3xx is not success either
	}

	for _, tc := range tests {
		rt := &roundTripper{status: tc.status, body: `{"message":"upstream detail"}`}
		svc := newTwilioWithTransport(TwilioConfig{AccountSID: "AC1", AuthToken: "t", FromNumber: "+1"}, rt)

		err := svc.SendSMS(context.Background(), "+1555", "hi")
		if tc.wantErr && err == nil {
			t.Errorf("status %d was treated as success", tc.status)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("status %d returned an error: %v", tc.status, err)
		}
		if tc.wantErr && err != nil && !strings.Contains(err.Error(), "upstream detail") {
			t.Errorf("status %d: error %q does not include the upstream body, which is the only diagnostic available", tc.status, err)
		}
	}
}

// CORRECTNESS: a transport failure must be reported, not swallowed.
func TestTwilioSendSMS_TransportFailure(t *testing.T) {
	rt := &roundTripper{err: errors.New("dial tcp: connection refused")}
	svc := newTwilioWithTransport(TwilioConfig{AccountSID: "AC1", AuthToken: "t", FromNumber: "+1"}, rt)

	err := svc.SendSMS(context.Background(), "+1555", "hi")
	if err == nil {
		t.Fatal("want an error when the request cannot be made")
	}
	if !strings.Contains(err.Error(), "twilio API call failed") {
		t.Errorf("error = %q, want it wrapped with context", err)
	}
}

// CORRECTNESS: the caller's context must govern the request, so a cancelled
// request does not keep an outbound HTTP call alive.
func TestTwilioSendSMS_HonoursContextCancellation(t *testing.T) {
	rt := &roundTripper{status: http.StatusCreated}
	svc := newTwilioWithTransport(TwilioConfig{AccountSID: "AC1", AuthToken: "t", FromNumber: "+1"}, rt)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := svc.SendSMS(ctx, "+1555", "hi"); err == nil {
		t.Fatal("want an error for an already-cancelled context")
	}
	if len(rt.requests) != 0 {
		t.Errorf("the request was still sent after cancellation")
	}
}

// CORRECTNESS: the auth token must never be logged or echoed into the error
// text — errors end up in logs and, via the generic error envelope, sometimes
// in support tickets.
func TestTwilioSendSMS_DoesNotLeakTheAuthTokenIntoErrors(t *testing.T) {
	const token = "super-secret-auth-token"
	rt := &roundTripper{status: http.StatusUnauthorized, body: `{"message":"Authenticate"}`}
	svc := newTwilioWithTransport(TwilioConfig{AccountSID: "AC1", AuthToken: token, FromNumber: "+1"}, rt)

	err := svc.SendSMS(context.Background(), "+1555", "hi")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("the auth token leaked into the error message: %v", err)
	}
}

// CHARACTERIZATION: the service does no validation of its own. An empty
// recipient, an empty body or an unconfigured sender are all sent to Twilio,
// which rejects them — so the failure is remote rather than local. Pinned so a
// change to local validation is a deliberate one.
func TestTwilioSendSMS_NoLocalValidation(t *testing.T) {
	rt := &roundTripper{status: http.StatusCreated}
	svc := newTwilioWithTransport(TwilioConfig{}, rt)

	if err := svc.SendSMS(context.Background(), "", ""); err != nil {
		t.Fatalf("SendSMS with empty fields: %v", err)
	}
	if len(rt.requests) != 1 {
		t.Fatalf("made %d requests, want 1", len(rt.requests))
	}
	if !strings.Contains(rt.requests[0].url, "/Accounts//Messages.json") {
		t.Errorf("url = %s, want the empty account SID reflected verbatim", rt.requests[0].url)
	}
}

// --- log-only fallbacks --------------------------------------------------

// CORRECTNESS: the log-only services are the demo/dev fallback. They must
// never fail, because a missing Twilio key must not break order fulfilment.
func TestLogServices_NeverFail(t *testing.T) {
	sms := NewLogSMSService(testLogger())
	if err := sms.SendSMS(context.Background(), "+1555", "hi"); err != nil {
		t.Errorf("LogSMSService.SendSMS: %v", err)
	}

	email := NewLogEmailService(testLogger())
	if err := email.SendInvoice(context.Background(), "a@b.c", "INV-1", []byte("%PDF-1.4")); err != nil {
		t.Errorf("LogEmailService.SendInvoice: %v", err)
	}
	if err := email.SendDeliveryNotification(context.Background(), "a@b.c", "Subject", "Body"); err != nil {
		t.Errorf("LogEmailService.SendDeliveryNotification: %v", err)
	}
	// The scheduled-report path: several recipients and a generated file.
	if err := email.SendEmailWithAttachment(context.Background(),
		[]string{"controller@example.com", "gm@example.com"},
		"Scheduled Report: AR aging", "Attached.", "ar_aging_2026-08-20.csv",
		[]byte("Customer,Total\nKelbrook Homes,1200\n")); err != nil {
		t.Errorf("LogEmailService.SendEmailWithAttachment: %v", err)
	}
	// Zero recipients and an empty attachment are the caller's mistake to
	// catch, not a reason for the log-only fallback to start returning errors.
	if err := email.SendEmailWithAttachment(context.Background(), nil, "", "", "", nil); err != nil {
		t.Errorf("LogEmailService.SendEmailWithAttachment with empty arguments: %v", err)
	}

	// They satisfy the interfaces the rest of the ERP wires them into.
	var _ SMSService = sms
	var _ EmailService = email
}

// CORRECTNESS: the report scheduler advertises to its API clients how a
// generated report actually reaches its recipients, and derives that sentence
// from the email service that is really wired. If LogEmailService stopped
// describing itself honestly — or stopped describing itself at all — the
// reporting API would go back to claiming reports "are emailed" when they are
// only logged.
func TestLogEmailService_DescribesItselfAsLogOnly(t *testing.T) {
	desc := NewLogEmailService(testLogger()).DeliveryDescription()

	if !strings.Contains(desc, "log-only") {
		t.Errorf("DeliveryDescription() = %q, want it to say delivery is log-only", desc)
	}
	if !strings.Contains(desc, "main.go") {
		t.Errorf("DeliveryDescription() = %q, want it to name where a real sender is swapped in", desc)
	}
}

// --- delivery notifications ---------------------------------------------

// CORRECTNESS: each event type produces its own message, every message names
// the order, and only known event types produce anything at all — an unknown
// status must not send a blank SMS to a customer.
func TestDeliveryNotifier_MessagePerEventType(t *testing.T) {
	tests := []struct {
		name           string
		event          DeliveryEvent
		wantSent       bool
		smsMustContain []string
		smsMustNot     []string
		subject        string
	}{
		{
			name: "staged",
			event: DeliveryEvent{
				EventType: DeliveryEventStaged, OrderNumber: "1001",
				CustomerName: "Acme", CustomerPhone: "+1555", CustomerEmail: "a@b.c",
			},
			wantSent:       true,
			smsMustContain: []string{"1001", "prepared"},
			subject:        "Order #1001 - Being Prepared",
		},
		{
			name: "out for delivery with an ETA",
			event: DeliveryEvent{
				EventType: DeliveryEventOutForDelivery, OrderNumber: "1002", ETA: "2026-03-04T15:30:00Z",
				CustomerName: "Acme", CustomerPhone: "+1555", CustomerEmail: "a@b.c",
			},
			wantSent:       true,
			smsMustContain: []string{"1002", "2026-03-04T15:30:00Z", "on the way"},
			subject:        "Order #1002 - Out for Delivery",
		},
		{
			name: "out for delivery without an ETA omits the ETA text",
			event: DeliveryEvent{
				EventType: DeliveryEventOutForDelivery, OrderNumber: "1003",
				CustomerName: "Acme", CustomerPhone: "+1555", CustomerEmail: "a@b.c",
			},
			wantSent:       true,
			smsMustContain: []string{"1003", "on the way"},
			smsMustNot:     []string{"ETA"},
			subject:        "Order #1003 - Out for Delivery",
		},
		{
			name: "delivered with a receipt link",
			event: DeliveryEvent{
				EventType: DeliveryEventDelivered, OrderNumber: "1004",
				ReceiptURL:   "https://example.com/pod/1004",
				CustomerName: "Acme", CustomerPhone: "+1555", CustomerEmail: "a@b.c",
			},
			wantSent:       true,
			smsMustContain: []string{"1004", "https://example.com/pod/1004"},
			subject:        "Order #1004 - Delivered",
		},
		{
			name: "delivered without a receipt link omits it",
			event: DeliveryEvent{
				EventType: DeliveryEventDelivered, OrderNumber: "1005",
				CustomerName: "Acme", CustomerPhone: "+1555", CustomerEmail: "a@b.c",
			},
			wantSent:       true,
			smsMustContain: []string{"1005", "completed"},
			smsMustNot:     []string{"View receipt"},
			subject:        "Order #1005 - Delivered",
		},
		{
			name: "an unknown event type sends nothing",
			event: DeliveryEvent{
				EventType: DeliveryEventType("CANCELLED"), OrderNumber: "1006",
				CustomerPhone: "+1555", CustomerEmail: "a@b.c",
			},
			wantSent: false,
		},
		{
			name: "an empty event type sends nothing",
			event: DeliveryEvent{
				OrderNumber: "1007", CustomerPhone: "+1555", CustomerEmail: "a@b.c",
			},
			wantSent: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sms := &recordingSMS{}
			email := &recordingEmail{}
			NewDeliveryNotifier(sms, email, testLogger()).Notify(context.Background(), tc.event)

			if !tc.wantSent {
				if len(sms.sent) != 0 {
					t.Errorf("sent an SMS for an unrecognised event: %q", sms.sent[0].body)
				}
				if len(email.deliveries) != 0 {
					t.Errorf("sent an email for an unrecognised event: %q", email.deliveries[0].subject)
				}
				return
			}

			if len(sms.sent) != 1 {
				t.Fatalf("sent %d SMS messages, want 1", len(sms.sent))
			}
			if sms.sent[0].to != tc.event.CustomerPhone {
				t.Errorf("SMS to = %q, want %q", sms.sent[0].to, tc.event.CustomerPhone)
			}
			for _, want := range tc.smsMustContain {
				if !strings.Contains(sms.sent[0].body, want) {
					t.Errorf("SMS body %q does not contain %q", sms.sent[0].body, want)
				}
			}
			for _, bad := range tc.smsMustNot {
				if strings.Contains(sms.sent[0].body, bad) {
					t.Errorf("SMS body %q unexpectedly contains %q", sms.sent[0].body, bad)
				}
			}

			if len(email.deliveries) != 1 {
				t.Fatalf("sent %d emails, want 1", len(email.deliveries))
			}
			if email.deliveries[0].subject != tc.subject {
				t.Errorf("email subject = %q, want %q", email.deliveries[0].subject, tc.subject)
			}
			if !strings.Contains(email.deliveries[0].body, tc.event.CustomerName) {
				t.Errorf("email body %q does not address the customer by name", email.deliveries[0].body)
			}
		})
	}
}

// CORRECTNESS: a channel with no address must be skipped rather than sent to
// an empty destination — Twilio would bill for and reject a message to "".
func TestDeliveryNotifier_SkipsChannelsWithNoAddress(t *testing.T) {
	tests := []struct {
		name          string
		phone, email  string
		wantSMS       int
		wantEmailSent int
	}{
		{"both present", "+1555", "a@b.c", 1, 1},
		{"phone only", "+1555", "", 1, 0},
		{"email only", "", "a@b.c", 0, 1},
		{"neither", "", "", 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sms := &recordingSMS{}
			mail := &recordingEmail{}
			NewDeliveryNotifier(sms, mail, testLogger()).Notify(context.Background(), DeliveryEvent{
				EventType: DeliveryEventDelivered, OrderNumber: "1001",
				CustomerPhone: tc.phone, CustomerEmail: tc.email,
			})
			if len(sms.sent) != tc.wantSMS {
				t.Errorf("sent %d SMS, want %d", len(sms.sent), tc.wantSMS)
			}
			if len(mail.deliveries) != tc.wantEmailSent {
				t.Errorf("sent %d emails, want %d", len(mail.deliveries), tc.wantEmailSent)
			}
		})
	}
}

// CORRECTNESS: the two channels are independent. A failing SMS provider must
// not stop the email, and vice versa — a delivery notification that reaches
// one channel is better than none, and Notify has no error return with which
// to report the failure anyway.
func TestDeliveryNotifier_OneChannelFailingDoesNotBlockTheOther(t *testing.T) {
	t.Run("SMS fails, email still sent", func(t *testing.T) {
		sms := &recordingSMS{err: errors.New("twilio down")}
		mail := &recordingEmail{}
		NewDeliveryNotifier(sms, mail, testLogger()).Notify(context.Background(), DeliveryEvent{
			EventType: DeliveryEventDelivered, OrderNumber: "1001",
			CustomerPhone: "+1555", CustomerEmail: "a@b.c",
		})
		if len(mail.deliveries) != 1 {
			t.Fatalf("sent %d emails, want 1 despite the SMS failure", len(mail.deliveries))
		}
	})

	t.Run("email fails, SMS still sent", func(t *testing.T) {
		sms := &recordingSMS{}
		mail := &recordingEmail{err: errors.New("smtp down")}
		NewDeliveryNotifier(sms, mail, testLogger()).Notify(context.Background(), DeliveryEvent{
			EventType: DeliveryEventDelivered, OrderNumber: "1001",
			CustomerPhone: "+1555", CustomerEmail: "a@b.c",
		})
		if len(sms.sent) != 1 {
			t.Fatalf("sent %d SMS, want 1 despite the email failure", len(sms.sent))
		}
	})

	t.Run("both fail without panicking", func(t *testing.T) {
		NewDeliveryNotifier(
			&recordingSMS{err: errors.New("x")},
			&recordingEmail{err: errors.New("y")},
			testLogger(),
		).Notify(context.Background(), DeliveryEvent{
			EventType: DeliveryEventStaged, OrderNumber: "1001",
			CustomerPhone: "+1555", CustomerEmail: "a@b.c",
		})
	})
}

// CORRECTNESS: the SMS and the email must describe the same event. A customer
// who gets "out for delivery" by text and "delivered" by email has no idea
// which to believe.
func TestDeliveryNotifier_SMSAndEmailAgree(t *testing.T) {
	for _, ev := range []DeliveryEventType{DeliveryEventStaged, DeliveryEventOutForDelivery, DeliveryEventDelivered} {
		sms := &recordingSMS{}
		mail := &recordingEmail{}
		NewDeliveryNotifier(sms, mail, testLogger()).Notify(context.Background(), DeliveryEvent{
			EventType: ev, OrderNumber: "1001", CustomerName: "Acme",
			CustomerPhone: "+1555", CustomerEmail: "a@b.c",
		})
		if len(sms.sent) != 1 || len(mail.deliveries) != 1 {
			t.Fatalf("%s: sent %d SMS and %d emails, want 1 each", ev, len(sms.sent), len(mail.deliveries))
		}
		if !strings.Contains(sms.sent[0].body, "1001") || !strings.Contains(mail.deliveries[0].subject, "1001") {
			t.Errorf("%s: the order number is missing from one of the channels (sms=%q subject=%q)",
				ev, sms.sent[0].body, mail.deliveries[0].subject)
		}
	}
}
