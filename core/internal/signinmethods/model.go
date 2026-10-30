// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package signinmethods holds the dealer's allowed sign in methods (S9): a
// closed vocabulary of six method codes stored one row per method in
// dealer_signin_methods (migration 129), discovered at runtime by the front
// door and the shells through the public GET /api/v1/auth/config. Gable is a
// single tenant per install (like the single row dealer_profile), so the
// table has no tenant column and the module resolves every caller to the one
// dealer. Of the six, only email_password is implemented as a server sign in
// path today (the portal login); the rest may sit in the allowlist, the door
// renders them, and their start route answers a plain refusal.
package signinmethods

import (
	"github.com/gablelbm/gable/internal/platform/httpx"
)

// The six method codes of the closed vocabulary, in canonical order.
const (
	MethodEmailPassword = "email_password"
	MethodPasskey       = "passkey"
	MethodMagicLink     = "magic_link"
	MethodGoogle        = "google"
	MethodMicrosoft     = "microsoft"
	MethodOIDC          = "oidc"
)

// Codes is the closed vocabulary, in canonical order. Each call returns a
// fresh slice.
func Codes() []string {
	return []string{MethodEmailPassword, MethodPasskey, MethodMagicLink, MethodGoogle, MethodMicrosoft, MethodOIDC}
}

// IsCode reports whether code is one of the six.
func IsCode(code string) bool {
	for _, c := range Codes() {
		if c == code {
			return true
		}
	}
	return false
}

// kindOf names how the door renders a method: a form it fills in, a device
// ceremony, a link sent to an inbox, or a redirect to a provider.
func kindOf(code string) string {
	switch code {
	case MethodEmailPassword:
		return "form"
	case MethodPasskey:
		return "passkey"
	case MethodMagicLink:
		return "link"
	default:
		return "redirect"
	}
}

// labelOf is the door's default label; an oidc row may carry its own.
func labelOf(code string) string {
	switch code {
	case MethodEmailPassword:
		return "Email and password"
	case MethodPasskey:
		return "Passkey"
	case MethodMagicLink:
		return "Email a sign in link"
	case MethodGoogle:
		return "Google"
	case MethodMicrosoft:
		return "Microsoft"
	case MethodOIDC:
		return "Single sign on"
	}
	return code
}

// StartPath is the route a non form method starts from.
const StartPath = "/api/v1/auth/start/"

// formStartAction is where the door's email and password form posts: the
// staff sign in the core makes through Appwrite (ADR 0012). The portal's own
// login page posts to the portal login directly and does not read this.
const formStartAction = "/api/v1/auth/signin"

// Row is one stored allowed method.
type Row struct {
	Method        string
	SortOrder     int
	OIDCIssuerURL string
	OIDCClientID  string
	OIDCLabel     string
}

// Input is one method of a replacement allowlist, in the caller's order.
type Input struct {
	Method        string
	OIDCIssuerURL string
	OIDCClientID  string
	OIDCLabel     string
}

// MethodView is the wire shape of one allowed method on both the public
// config and the admin allowlist read. The nullable fields are always
// present, null when the method does not use them: the response type is
// fixed, so a caller never has to guess whether a field is absent.
type MethodView struct {
	Method    string  `json:"method"`
	Kind      string  `json:"kind"`
	Label     string  `json:"label"`
	StartURL  *string `json:"start_url"`
	IssuerURL *string `json:"issuer_url"`
	ClientID  *string `json:"client_id"`
}

// viewOf builds the wire view of a stored row.
func viewOf(r Row) MethodView {
	v := MethodView{Method: r.Method, Kind: kindOf(r.Method), Label: labelOf(r.Method)}
	if r.Method == MethodEmailPassword {
		action := formStartAction
		v.StartURL = &action
	} else {
		start := StartPath + r.Method
		v.StartURL = &start
	}
	if r.Method == MethodOIDC {
		if r.OIDCLabel != "" {
			v.Label = r.OIDCLabel
		}
	}
	return v
}

// ConfigResponse is the public GET /api/v1/auth/config body: the auth mode
// the server runs in, the dealer's public display name and optional mark URL
// for the white labelled door, and the allowed methods in the dealer's stored
// order. It is a strict allowlist type: no secret, no internal host, no other
// tenant, nothing else.
type ConfigResponse struct {
	AuthMode      string       `json:"auth_mode"`
	DealerName    string       `json:"dealer_name"`
	DealerMarkURL *string      `json:"dealer_mark_url"`
	Methods       []MethodView `json:"methods"`
}

// AllowlistResponse is the admin read of the same allowlist.
type AllowlistResponse struct {
	Methods   []MethodView    `json:"methods"`
	UpdatedAt httpx.Timestamp `json:"updated_at"`
}
