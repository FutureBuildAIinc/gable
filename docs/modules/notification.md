<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Notification: the email sender

Every email the ERP sends goes through one interface,
`notification.EmailService` (`core/internal/notification/email.go`), and the
process picks its implementation once at start from the environment. The
consumers (the document routes, the delivery notifier, the exposure notifier
and the report scheduler) depend on the interface alone.

## Which sender runs

| `SMTP_HOST` | Sender | What happens to a message |
|---|---|---|
| unset or empty | `LogEmailService` | A structured log line; nothing leaves the process. It discloses this through `DeliveryDescription()`, which the report schedule API shows as `execution.delivery`. |
| set | `SMTPEmailService` | A real SMTP session. It does not implement `DeliveryDescription()`, so the schedule API falls back to its neutral statement. |

`notification.NewEmailServiceFromEnv` makes the choice for both `core serve`
and `core worker`. A set host with a bad setting is an error at start, and the
process exits rather than run half configured.

## Configuration

Environment only. No default host, address or credential exists anywhere in
the repository.

| Variable | Meaning |
|---|---|
| `SMTP_HOST` | The server. Setting it turns the real sender on. |
| `SMTP_PORT` | Default 587, or 465 when `SMTP_TLS=tls`. |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | Optional; set together or not at all. Sent as PLAIN, and refused in clear text to any host that is not loopback. |
| `SMTP_FROM` | Required with a host. An address with or without a display name. |
| `SMTP_TLS` | `starttls` (default; refuses to send if the server does not offer it), `tls` (implicit TLS) or `none` (clear text, for a local catcher). |

## What the sender does

The sender speaks SMTP over a small session of its own so that it can return
the server's reply to the end of the message: `notification.SendWithAnswer`
gives the delivery record the sender's own words (`250 2.0.0 Ok: queued as ...`
from a server, `logged by the log sender, not sent` from the log sender).
Messages are MIME: a base64 text part, and for a document a base64 attachment
named for it. A subject or recipient holding a line break, or a recipient that
is not an address, is refused before anything is sent. A non ASCII subject is
encoded. One send is bounded in time when the caller sets no deadline.

## A local mail catcher

`docker-compose.yml` has an optional `mailpit` service on the `mail` profile,
bound to 127.0.0.1 (SMTP on 1025, the page on 8025). The default and the CI
stack never start it. To send into it:

```
GABLE_SMTP_HOST=mailpit GABLE_SMTP_PORT=1025 GABLE_SMTP_TLS=none \
GABLE_SMTP_FROM=desk@example.test docker compose --profile mail up
```

Then open <http://127.0.0.1:8025> to read what the ERP sent. Nothing the
catcher receives leaves the machine.

## Tests

`core/internal/notification/smtp_test.go` drives a fake SMTP server in Go
bound to port 0: a plain delivery, STARTTLS with authentication, implicit TLS,
a server that does not offer STARTTLS, a refused message, header injection, a
non ASCII subject, the configuration rules (a host without a From address is
refused) and the choice between the two senders.
