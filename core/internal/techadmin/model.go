// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin

import (
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// APIKey is a machine key row. The Argon2 hash never leaves the package
// (json:"-"); the wire shape is the summary a list or a mint answers with,
// the one identifying detail being the 12 character prefix, since the raw
// key is shown exactly once, at mint.
type APIKey struct {
	ID        uuid.UUID       `json:"id"`
	Name      string          `json:"name"`
	KeyHash   string          `json:"-"`
	KeyPrefix string          `json:"prefix"`
	Scopes    []string        `json:"scopes"`
	CreatedAt httpx.Timestamp `json:"created_at"`
	LastUsed  *httpx.Timestamp `json:"last_used_at"`
	Revoked   *httpx.Timestamp `json:"revoked_at"`
}

// CreatedKey is the mint answer: the raw key (the only time it is visible)
// beside its row.
type CreatedKey struct {
	APIKey string  `json:"api_key"`
	Key    APIKey  `json:"key"`
}

// The settings resources the module owns, each a named set of system_settings
// rows with one revision anchored in admin_revisions (migration 095): the
// resource is a singleton document, so the anchor row (not any value row)
// carries its revision, and a delete cannot move the revision backwards.
const (
	// ResourceAISettings spans the openrouter_api_key and openrouter_base_url
	// rows.
	ResourceAISettings = "admin.settings.ai"
	// ResourceRoutingSettings is the openrouteservice_api_key row.
	ResourceRoutingSettings = "admin.settings.routing"
)

// The system_settings keys each resource reads and writes. The AI stack
// (internal/ai) reads the same rows through its own store; this module is the
// write side.
const (
	settingAIKey     = "openrouter_api_key"
	settingAIBaseURL = "openrouter_base_url"
	settingORSKey    = "openrouteservice_api_key"
)

// AISettings is the AI settings document: the key's status (never the key
// itself, only its hint), the base URL when an admin override is stored, and
// the resource revision every write must precondition on.
type AISettings struct {
	Configured bool    `json:"configured"`
	Source     string  `json:"source"` // "admin", "env" or "none"
	KeyHint    *string `json:"key_hint"`
	BaseURL    *string `json:"base_url"`
	Revision   int64   `json:"revision"`
}

// RoutingSettings is the routing (OpenRouteService) settings document, the
// AI settings' shape without the base URL.
type RoutingSettings struct {
	Configured bool    `json:"configured"`
	Source     string  `json:"source"`
	KeyHint    *string `json:"key_hint"`
	Revision   int64   `json:"revision"`
}

// keyHint is the masked hint a configured key shows: its first 10 and last 4
// characters, or four asterisks when the key is too short to hint safely.
func keyHint(key string) *string {
	if key == "" {
		return nil
	}
	hint := "****"
	if len(key) > 12 {
		hint = key[:10] + "..." + key[len(key)-4:]
	}
	return &hint
}
