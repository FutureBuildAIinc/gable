// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package notification

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal SMTP server bound to port 0. It speaks EHLO,
// STARTTLS (when tlsCfg is set and implicit is false), AUTH PLAIN, MAIL, RCPT,
// DATA, RSET and QUIT, and records what it was given.
type fakeSMTP struct {
	ln       net.Listener
	tlsCfg   *tls.Config
	implicit bool
	failData bool

	mu       sync.Mutex
	starttls bool
	authed   string
	from     string
	rcpts    []string
	data     string
	wg       sync.WaitGroup
}

func newFakeSMTP(t *testing.T, tlsCfg *tls.Config, implicit bool) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, tlsCfg: tlsCfg, implicit: implicit}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.handle(c)
		}
	}()
	t.Cleanup(func() { ln.Close(); f.wg.Wait() })
	return f
}

func (f *fakeSMTP) hostPort() (string, string) {
	h, p, _ := net.SplitHostPort(f.ln.Addr().String())
	return h, p
}

func (f *fakeSMTP) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if f.implicit {
		c = tls.Server(c, f.tlsCfg)
	}
	r := bufio.NewReader(c)
	w := func(s string) { fmt.Fprintf(c, "%s\r\n", s) }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			fmt.Fprintf(c, "250-fake\r\n")
			if f.tlsCfg != nil && !f.implicit && !f.tlsOn() {
				fmt.Fprintf(c, "250-STARTTLS\r\n")
			}
			fmt.Fprintf(c, "250 AUTH PLAIN\r\n")
		case up == "STARTTLS":
			w("220 go ahead")
			c = tls.Server(c, f.tlsCfg)
			r = bufio.NewReader(c)
			f.mu.Lock()
			f.starttls = true
			f.mu.Unlock()
			w = func(s string) { fmt.Fprintf(c, "%s\r\n", s) }
		case strings.HasPrefix(up, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			f.mu.Lock()
			f.authed = strings.ReplaceAll(string(raw), "\x00", "|")
			f.mu.Unlock()
			w("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			f.mu.Lock()
			f.from = strings.Trim(line[len("MAIL FROM:"):], "<> ")
			f.rcpts = nil
			f.mu.Unlock()
			w("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			f.mu.Lock()
			f.rcpts = append(f.rcpts, strings.Trim(line[len("RCPT TO:"):], "<> "))
			f.mu.Unlock()
			w("250 ok")
		case up == "DATA":
			w("354 go")
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				sb.WriteString(l)
			}
			if f.failData {
				w("554 refused by test")
				continue
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			w("250 queued as test1")
		case up == "RSET":
			w("250 ok")
		case up == "QUIT":
			w("221 bye")
			return
		default:
			w("500 what")
		}
	}
}

func (f *fakeSMTP) tlsOn() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starttls
}

func selfSignedConfig(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, pool
}

func senderFor(t *testing.T, f *fakeSMTP, mode string, pool *x509.CertPool, user, pass string) *SMTPEmailService {
	t.Helper()
	h, p := f.hostPort()
	port, _ := strconv.Atoi(p)
	s, err := NewSMTPEmailService(SMTPConfig{Host: h, Port: port, Username: user, Password: pass, From: "Yard Desk <desk@example.test>", TLS: mode})
	if err != nil {
		t.Fatal(err)
	}
	if pool != nil {
		s.tlsConfig = &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}
	}
	return s
}

func TestSMTPSendEmailWithAttachment_PlainDelivery(t *testing.T) {
	f := newFakeSMTP(t, nil, false)
	s := senderFor(t, f, SMTPTLSNone, nil, "", "")
	if err := s.SendEmailWithAttachment(context.Background(), []string{"a@example.test", "b@example.test"}, "Weekly report", "Body text", "report.csv", []byte("x,y\n1,2\n")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if f.from != "desk@example.test" {
		t.Errorf("envelope from = %q", f.from)
	}
	if len(f.rcpts) != 2 || f.rcpts[0] != "a@example.test" || f.rcpts[1] != "b@example.test" {
		t.Errorf("rcpts = %v", f.rcpts)
	}
	m, err := mail.ReadMessage(strings.NewReader(f.data))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := m.Header.Get("Subject"); got != "Weekly report" {
		t.Errorf("subject = %q", got)
	}
	if got := m.Header.Get("From"); !strings.Contains(got, "desk@example.test") {
		t.Errorf("from header = %q", got)
	}
	if !strings.HasPrefix(m.Header.Get("Content-Type"), "multipart/mixed") {
		t.Errorf("content type = %q", m.Header.Get("Content-Type"))
	}
	if !strings.Contains(f.data, `filename="report.csv"`) || !strings.Contains(f.data, base64.StdEncoding.EncodeToString([]byte("x,y\n1,2\n"))[:8]) {
		t.Errorf("attachment missing from message: %q", f.data)
	}
}

func TestSMTPStartTLSAndAuth(t *testing.T) {
	cfg, pool := selfSignedConfig(t)
	f := newFakeSMTP(t, cfg, false)
	s := senderFor(t, f, SMTPTLSStartTLS, pool, "ops", "placeholder-pass")
	if err := s.SendDeliveryNotification(context.Background(), "c@example.test", "Out for delivery", "Your order is on the truck."); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !f.tlsOn() {
		t.Error("the session never upgraded with STARTTLS")
	}
	if f.authed != "|ops|placeholder-pass" {
		t.Errorf("auth = %q", f.authed)
	}
	if !strings.Contains(f.data, base64.StdEncoding.EncodeToString([]byte("Your order is on the truck."))) {
		t.Errorf("body missing: %q", f.data)
	}
}

func TestSMTPImplicitTLS(t *testing.T) {
	cfg, pool := selfSignedConfig(t)
	f := newFakeSMTP(t, cfg, true)
	s := senderFor(t, f, SMTPTLSImplicit, pool, "", "")
	if err := s.SendInvoice(context.Background(), "c@example.test", "INV-1", []byte("%PDF-1.4 stub")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.Contains(f.data, "INV-1") || !strings.Contains(f.data, `filename="invoice-INV-1.pdf"`) {
		t.Errorf("invoice message wrong: %q", f.data)
	}
}

func TestSMTPStartTLSRequiredWhenServerLacksIt(t *testing.T) {
	f := newFakeSMTP(t, nil, false)
	s := senderFor(t, f, SMTPTLSStartTLS, nil, "", "")
	err := s.SendDeliveryNotification(context.Background(), "c@example.test", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v, want a STARTTLS refusal", err)
	}
	if f.data != "" {
		t.Error("a message was sent in clear text")
	}
}

func TestSMTPServerRefusalIsAnError(t *testing.T) {
	f := newFakeSMTP(t, nil, false)
	f.failData = true
	s := senderFor(t, f, SMTPTLSNone, nil, "", "")
	err := s.SendDeliveryNotification(context.Background(), "c@example.test", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "554") {
		t.Fatalf("err = %v, want the server's 554", err)
	}
}

func TestSMTPRefusesHeaderInjection(t *testing.T) {
	f := newFakeSMTP(t, nil, false)
	s := senderFor(t, f, SMTPTLSNone, nil, "", "")
	if err := s.SendDeliveryNotification(context.Background(), "c@example.test", "hi\r\nBcc: x@example.test", "b"); err == nil {
		t.Error("a subject with a line break was accepted")
	}
	if err := s.SendDeliveryNotification(context.Background(), "c@example.test\r\nRCPT TO:<x@example.test>", "s", "b"); err == nil {
		t.Error("a recipient with a line break was accepted")
	}
	if err := s.SendDeliveryNotification(context.Background(), "not an address", "s", "b"); err == nil {
		t.Error("a malformed recipient was accepted")
	}
	if f.data != "" {
		t.Error("something reached the server")
	}
}

func TestSMTPUnicodeSubjectIsEncoded(t *testing.T) {
	f := newFakeSMTP(t, nil, false)
	s := senderFor(t, f, SMTPTLSNone, nil, "", "")
	if err := s.SendDeliveryNotification(context.Background(), "c@example.test", "Facture déjà prête", "b"); err != nil {
		t.Fatal(err)
	}
	m, _ := mail.ReadMessage(strings.NewReader(f.data))
	dec := new(mime.WordDecoder)
	if got, _ := dec.DecodeHeader(m.Header.Get("Subject")); got != "Facture déjà prête" {
		t.Errorf("subject = %q", got)
	}
}

func TestSMTPDoesNotImplementDeliveryDescription(t *testing.T) {
	var s EmailService = &SMTPEmailService{}
	if _, ok := s.(interface{ DeliveryDescription() string }); ok {
		t.Fatal("a real sender must not disclose itself as log-only")
	}
}

func TestLoadSMTPConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	cfg, err := LoadSMTPConfig(env(map[string]string{}))
	if err != nil || cfg != nil {
		t.Fatalf("no host: cfg=%v err=%v, want nil, nil", cfg, err)
	}
	if _, err := LoadSMTPConfig(env(map[string]string{"SMTP_HOST": "mail.example.test"})); err == nil || !strings.Contains(err.Error(), "SMTP_FROM") {
		t.Fatalf("host without from: err=%v, want a refusal naming SMTP_FROM", err)
	}
	if _, err := LoadSMTPConfig(env(map[string]string{"SMTP_HOST": "h", "SMTP_FROM": "not an address"})); err == nil {
		t.Fatal("a malformed From address was accepted")
	}
	if _, err := LoadSMTPConfig(env(map[string]string{"SMTP_HOST": "h", "SMTP_FROM": "a@example.test", "SMTP_TLS": "maybe"})); err == nil {
		t.Fatal("an unknown SMTP_TLS was accepted")
	}
	if _, err := LoadSMTPConfig(env(map[string]string{"SMTP_HOST": "h", "SMTP_FROM": "a@example.test", "SMTP_PORT": "99999"})); err == nil {
		t.Fatal("an out of range port was accepted")
	}
	if _, err := LoadSMTPConfig(env(map[string]string{"SMTP_HOST": "h", "SMTP_FROM": "a@example.test", "SMTP_USERNAME": "u"})); err == nil {
		t.Fatal("a username without a password was accepted")
	}
	cfg, err = LoadSMTPConfig(env(map[string]string{"SMTP_HOST": "mail.example.test", "SMTP_FROM": "a@example.test"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 587 || cfg.TLS != SMTPTLSStartTLS {
		t.Errorf("defaults = port %d tls %q, want 587 starttls", cfg.Port, cfg.TLS)
	}
	cfg, _ = LoadSMTPConfig(env(map[string]string{"SMTP_HOST": "h", "SMTP_FROM": "a@example.test", "SMTP_TLS": "tls"}))
	if cfg.Port != 465 {
		t.Errorf("implicit tls default port = %d, want 465", cfg.Port)
	}
}

func TestNewEmailServiceFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	svc, err := NewEmailServiceFromEnv(env(nil), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.(*LogEmailService); !ok {
		t.Errorf("no SMTP_HOST gave %T, want the log sender", svc)
	}
	svc, err = NewEmailServiceFromEnv(env(map[string]string{"SMTP_HOST": "mail.example.test", "SMTP_FROM": "a@example.test"}), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.(*SMTPEmailService); !ok {
		t.Errorf("SMTP_HOST gave %T, want the SMTP sender", svc)
	}
	if _, err := NewEmailServiceFromEnv(env(map[string]string{"SMTP_HOST": "mail.example.test"}), testLogger()); err == nil {
		t.Error("a host without a From address must refuse, so serve refuses to start")
	}
}

func TestSMTPAnswerIsTheServersReply(t *testing.T) {
	f := newFakeSMTP(t, nil, false)
	s := senderFor(t, f, SMTPTLSNone, nil, "", "")
	ans, err := SendWithAnswer(context.Background(), s, []string{"c@example.test"}, "Statement", "Body", "statement.pdf", []byte("%PDF-1.4 stub"))
	if err != nil {
		t.Fatal(err)
	}
	if ans != "250 queued as test1" {
		t.Errorf("answer = %q", ans)
	}
	f.failData = true
	ans, err = SendWithAnswer(context.Background(), s, []string{"c@example.test"}, "Statement", "Body", "statement.pdf", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "554") {
		t.Errorf("refusal: ans=%q err=%v", ans, err)
	}
}

func TestLogSenderAnswerSaysNothingWasSent(t *testing.T) {
	ans, err := SendWithAnswer(context.Background(), NewLogEmailService(testLogger()), []string{"c@example.test"}, "s", "b", "f.pdf", []byte("x"))
	if err != nil || !strings.Contains(ans, "not sent") {
		t.Errorf("ans=%q err=%v", ans, err)
	}
}

// The password reaches the wire only as the AUTH PLAIN line: not an error, not
// a log line, so not the delivery row built from the error.
func TestSMTPPasswordNeverReachesAnErrorOrALog(t *testing.T) {
	const secret = "teststub-password-teststub"
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := newFakeSMTP(t, nil, false)
	f.failData = true
	s := senderFor(t, f, SMTPTLSNone, nil, "ops", secret)
	_, err := s.SendWithAnswer(context.Background(), []string{"c@example.test"}, "s", "b", "r.csv", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "554") {
		t.Fatalf("err = %v, want the server's 554", err)
	}
	if f.authed != "|ops|"+secret {
		t.Fatalf("the send never authenticated, so the test proved nothing: %q", f.authed)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Errorf("the password leaked: error %q, log %q", err, logs.String())
	}
}

// PLAIN credentials over SMTP_TLS=none are refused to a host that is not
// loopback, before AUTH goes out.
func TestSMTPRefusesPlainAuthInClearTextToANonLoopbackHost(t *testing.T) {
	addrs, _ := net.InterfaceAddrs()
	var host string
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
			host = ipn.IP.String()
			break
		}
	}
	if host == "" {
		t.Skip("no non loopback IPv4 address on this machine")
	}
	ln, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Skipf("cannot listen on %s: %v", host, err)
	}
	f := &fakeSMTP{ln: ln}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.handle(c)
		}
	}()
	t.Cleanup(func() { ln.Close(); f.wg.Wait() })

	_, p := f.hostPort()
	port, _ := strconv.Atoi(p)
	s, err := NewSMTPEmailService(SMTPConfig{Host: host, Port: port, Username: "ops", Password: "teststub", From: "d@example.test", TLS: SMTPTLSNone})
	if err != nil {
		t.Fatal(err)
	}
	err = s.SendDeliveryNotification(context.Background(), "c@example.test", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "clear text") {
		t.Fatalf("err = %v, want the clear text refusal", err)
	}
	if f.authed != "" || f.data != "" {
		t.Errorf("credentials or a message went out: auth %q data %q", f.authed, f.data)
	}
}
