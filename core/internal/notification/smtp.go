// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package notification

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// The three transport modes SMTP_TLS names.
const (
	// SMTPTLSStartTLS upgrades a plain connection with STARTTLS and refuses to
	// send when the server does not offer it. It is the default.
	SMTPTLSStartTLS = "starttls"
	// SMTPTLSImplicit wraps the connection in TLS from the first byte.
	SMTPTLSImplicit = "tls"
	// SMTPTLSNone sends in clear text; it is for a local mail catcher.
	SMTPTLSNone = "none"
)

// smtpTimeout bounds one whole send, dial to quit, when the caller's context
// carries no earlier deadline.
const smtpTimeout = 30 * time.Second

// SMTPConfig is the sender's configuration. It comes only from the
// environment (LoadSMTPConfig); nothing here has a default host or address.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string // an RFC 5322 address, with or without a display name
	TLS      string // SMTPTLSStartTLS, SMTPTLSImplicit or SMTPTLSNone
}

// LoadSMTPConfig reads SMTP_HOST, SMTP_PORT, SMTP_USERNAME, SMTP_PASSWORD,
// SMTP_FROM and SMTP_TLS through getenv. It returns nil, nil when SMTP_HOST is
// unset (the caller keeps the log sender). A set host with no usable From
// address, an unknown SMTP_TLS, a bad port, or a username without a password is
// an error: the server must refuse to start rather than run half configured.
func LoadSMTPConfig(getenv func(string) string) (*SMTPConfig, error) {
	host := strings.TrimSpace(getenv("SMTP_HOST"))
	if host == "" {
		return nil, nil
	}
	cfg := &SMTPConfig{
		Host:     host,
		Username: getenv("SMTP_USERNAME"),
		Password: getenv("SMTP_PASSWORD"),
		From:     strings.TrimSpace(getenv("SMTP_FROM")),
		TLS:      strings.ToLower(strings.TrimSpace(getenv("SMTP_TLS"))),
	}
	if cfg.From == "" {
		return nil, errors.New("SMTP_HOST is set but SMTP_FROM is not: a sender needs a From address")
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		return nil, fmt.Errorf("SMTP_FROM is not an email address: %w", err)
	}
	switch cfg.TLS {
	case "":
		cfg.TLS = SMTPTLSStartTLS
	case SMTPTLSStartTLS, SMTPTLSImplicit, SMTPTLSNone:
	default:
		return nil, fmt.Errorf("SMTP_TLS %q is not one of starttls, tls, none", cfg.TLS)
	}
	if raw := strings.TrimSpace(getenv("SMTP_PORT")); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("SMTP_PORT %q is not a port number", raw)
		}
		cfg.Port = p
	} else if cfg.TLS == SMTPTLSImplicit {
		cfg.Port = 465
	} else {
		cfg.Port = 587
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return nil, errors.New("SMTP_USERNAME and SMTP_PASSWORD must be set together")
	}
	return cfg, nil
}

// SMTPEmailService is the real EmailService: it hands each message to an SMTP
// server. It deliberately does NOT implement DeliveryDescription, the method
// LogEmailService uses to disclose that nothing leaves the process.
type SMTPEmailService struct {
	cfg  SMTPConfig
	from *mail.Address

	// tlsConfig is set by tests to trust their throwaway certificate; nil
	// means the system roots and the configured host name.
	tlsConfig *tls.Config
}

// NewSMTPEmailService builds the sender from a validated configuration.
func NewSMTPEmailService(cfg SMTPConfig) (*SMTPEmailService, error) {
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("smtp from address: %w", err)
	}
	if cfg.Host == "" || cfg.Port == 0 {
		return nil, errors.New("smtp host and port are required")
	}
	return &SMTPEmailService{cfg: cfg, from: from}, nil
}

func (s *SMTPEmailService) SendInvoice(ctx context.Context, to string, invoiceID string, pdfBytes []byte) error {
	return s.send(ctx, []string{to}, fmt.Sprintf("Invoice %s", invoiceID), "Please find your invoice attached.",
		"invoice-"+safeFilename(invoiceID)+".pdf", pdfBytes)
}

func (s *SMTPEmailService) SendDeliveryNotification(ctx context.Context, to string, subject string, body string) error {
	return s.send(ctx, []string{to}, subject, body, "", nil)
}

func (s *SMTPEmailService) SendEmailWithAttachment(ctx context.Context, to []string, subject string, body string, filename string, content []byte) error {
	return s.send(ctx, to, subject, body, safeFilename(filename), content)
}

func safeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, name)
}

func (s *SMTPEmailService) send(ctx context.Context, to []string, subject, body, filename string, content []byte) error {
	if len(to) == 0 {
		return errors.New("no recipient")
	}
	rcpts := make([]string, 0, len(to))
	for _, t := range to {
		if strings.ContainsAny(t, "\r\n") {
			return fmt.Errorf("recipient %q holds a line break", t)
		}
		a, err := mail.ParseAddress(t)
		if err != nil {
			return fmt.Errorf("recipient %q: %w", t, err)
		}
		rcpts = append(rcpts, a.Address)
	}
	if strings.ContainsAny(subject, "\r\n") {
		return errors.New("subject holds a line break")
	}
	msg, err := s.buildMessage(rcpts, subject, body, filename, content)
	if err != nil {
		return err
	}

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, smtpTimeout)
		defer cancel()
	}
	c, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	if s.cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("smtp server does not offer AUTH but credentials are configured")
		}
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("smtp RCPT TO %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp end of data: %w", err)
	}
	return c.Quit()
}

// dial opens the connection in the configured mode and returns a client that
// has finished its greeting and, where asked, its TLS upgrade.
func (s *SMTPEmailService) dial(ctx context.Context) (*smtp.Client, error) {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("smtp dial: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := s.tlsConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
	}
	if s.cfg.TLS == SMTPTLSImplicit {
		conn = tls.Client(conn, tlsCfg)
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smtp greeting: %w", err)
	}
	if s.cfg.TLS == SMTPTLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			c.Close()
			return nil, errors.New("smtp server does not offer STARTTLS and SMTP_TLS is starttls: refusing to send in clear text")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			c.Close()
			return nil, fmt.Errorf("smtp STARTTLS: %w", err)
		}
	}
	return c, nil
}

func (s *SMTPEmailService) buildMessage(rcpts []string, subject, body, filename string, content []byte) ([]byte, error) {
	var idb [12]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(idb[:])
	domain := "localhost"
	if i := strings.LastIndex(s.from.Address, "@"); i >= 0 {
		domain = s.from.Address[i+1:]
	}

	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", s.from.String())
	h("To", strings.Join(rcpts, ", "))
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", time.Now().UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+id+"@"+domain+">")
	h("MIME-Version", "1.0")

	textPart := func() {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
		writeBase64(&b, []byte(body))
	}
	if filename == "" {
		textPart()
		return b.Bytes(), nil
	}
	boundary := "gable-" + id
	h("Content-Type", `multipart/mixed; boundary="`+boundary+`"`)
	b.WriteString("\r\n--" + boundary + "\r\n")
	textPart()
	b.WriteString("\r\n--" + boundary + "\r\n")
	ctype := mime.TypeByExtension(strings.ToLower(filename[strings.LastIndex(filename, "."):]))
	if ctype == "" || !strings.Contains(filename, ".") {
		ctype = "application/octet-stream"
	}
	fmt.Fprintf(&b, "Content-Type: %s\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"%s\"\r\n\r\n", ctype, filename)
	writeBase64(&b, content)
	b.WriteString("\r\n--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

func writeBase64(b *bytes.Buffer, data []byte) {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
}

// NewEmailServiceFromEnv picks the email sender for a process: the SMTP
// sender when SMTP_HOST is set, the log sender otherwise. A set host with a
// missing or malformed setting is an error the caller treats as fatal.
func NewEmailServiceFromEnv(getenv func(string) string, logger *slog.Logger) (EmailService, error) {
	cfg, err := LoadSMTPConfig(getenv)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return NewLogEmailService(logger), nil
	}
	s, err := NewSMTPEmailService(*cfg)
	if err != nil {
		return nil, err
	}
	logger.Info("Email sender: SMTP", "host", cfg.Host, "port", cfg.Port, "tls", cfg.TLS)
	return s, nil
}
