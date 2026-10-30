// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// CreateKeyRequest is the body of POST /api/v1/admin/keys. The scopes are
// minted verbatim (ADR 0002: a grant reads back as written) after the
// grammar check ADR 0007 section 5.3 adds at the mint; branch_id, when
// present, is the branch bound key's pin (ADR 0007 section 5.5), set at
// mint and never edited.
type CreateKeyRequest struct {
	Name     *string  `json:"name"`
	Scopes   []string `json:"scopes"`
	BranchID *string  `json:"branch_id"`
}

// Parse validates the mint request, collecting every problem into one 400.
func (r *CreateKeyRequest) Parse() (name string, scopes []string, branch *uuid.UUID, err error) {
	v := &httpx.Validator{}
	name = ""
	if r.Name != nil {
		name = strings.TrimSpace(*r.Name)
	}
	if name == "" {
		v.Check(false, "name", "is required")
	} else if len(name) > 255 {
		v.Check(false, "name", "must be 255 characters or fewer")
	}
	if id, ok := v.UUID("branch_id", r.BranchID, false); ok {
		branch = &id
	}
	scopes = []string{}
	seen := map[string]bool{}
	for i, s := range r.Scopes {
		path := "scopes[" + strconv.Itoa(i) + "]"
		if s == "" {
			v.Check(false, path, "must not be empty")
			continue
		}
		if len(s) > 128 || !printableASCII(s) {
			v.Check(false, path, "must be 128 printable ASCII characters or fewer")
			continue
		}
		if !seen[s] {
			seen[s] = true
			scopes = append(scopes, s)
		}
	}
	if len(r.Scopes) > 64 {
		v.Check(false, "scopes", "must carry at most 64 scopes")
	}
	if err := v.Err(); err != nil {
		return "", nil, nil, err
	}
	return name, scopes, branch, nil
}

// SaveAISettingsRequest is the body of PUT /api/v1/admin/settings/ai. BaseURL
// is a pointer so omitted (leave the override as it is) and present but empty
// (clear the override, reverting to the environment default) stay distinct.
// Revision is the body's precondition; If-Match serves the same role.
type SaveAISettingsRequest struct {
	APIKey          *string         `json:"api_key"`
	BaseURL         *string         `json:"base_url"`
	Provider        *string         `json:"provider"`
	Model           *string         `json:"model"`
	AnthropicAPIKey *string         `json:"anthropic_api_key"`
	Revision        json.RawMessage `json:"revision"`
}

// Parse validates the save, collecting every problem into one 400. A bad
// base URL or model is reported here, before anything is written, so a
// refused save never leaves half the settings behind. An OpenRouter key
// is required only when the save names nothing else.
func (r *SaveAISettingsRequest) Parse() (save AISave, revision *int64, err error) {
	v := &httpx.Validator{}
	var apiKey, provider, model, akey string
	if r.APIKey != nil {
		apiKey = *r.APIKey
	}
	if r.Provider != nil {
		provider = strings.TrimSpace(*r.Provider)
	}
	if r.Model != nil {
		model = strings.TrimSpace(*r.Model)
	}
	if r.AnthropicAPIKey != nil {
		akey = strings.TrimSpace(*r.AnthropicAPIKey)
	}
	if provider != "" && provider != ai.ProviderOpenRouter && provider != ai.ProviderAnthropic {
		v.Check(false, "provider", "must be openrouter or anthropic")
	}
	if verr := ai.ValidateAnthropicModel(model); verr != nil {
		v.Check(false, "model", verr.Error())
	}
	// A save must change something: an OpenRouter key is required only when
	// the body names nothing else, so a provider or model alone (switching
	// back to OpenRouter) is a valid save.
	if strings.TrimSpace(apiKey) == "" && akey == "" && provider == "" && model == "" && r.BaseURL == nil {
		v.Check(false, "api_key", "is required")
	}
	var baseURL *string
	if r.BaseURL != nil {
		u := strings.TrimSpace(*r.BaseURL)
		if u != "" {
			if verr := ai.ValidateBaseURL(u); verr != nil {
				v.Check(false, "base_url", verr.Error())
			}
		}
		baseURL = &u
	}
	if n, ok := v.Int("revision", r.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		revision = &n
	}
	if err := v.Err(); err != nil {
		return AISave{}, nil, err
	}
	return AISave{
		APIKey:          strings.TrimSpace(apiKey),
		BaseURL:         baseURL,
		Provider:        provider,
		Model:           model,
		AnthropicAPIKey: akey,
	}, revision, nil
}

// SaveRoutingSettingsRequest is the body of PUT /api/v1/admin/settings/routing.
type SaveRoutingSettingsRequest struct {
	APIKey   *string         `json:"api_key"`
	Revision json.RawMessage `json:"revision"`
}

// SaveMarketIndexSourceRequest is the body of PUT
// /api/v1/admin/settings/market-index-source. FeedURL and FeedKey are
// pointers so omitted (leave it as it is), present but empty (clear it) and a
// value (replace it) stay distinct; neither is ever echoed back.
type SaveMarketIndexSourceRequest struct {
	Source        *string         `json:"source"`
	FeedURL       *string         `json:"feed_url"`
	FeedKeyHeader *string         `json:"feed_key_header"`
	FeedKey       *string         `json:"feed_key"`
	StalenessDays json.RawMessage `json:"staleness_days"`
	Revision      json.RawMessage `json:"revision"`
}

// Parse validates the market index source save, collecting every problem
// into one 400 before anything is written. A save must name something.
func (r *SaveMarketIndexSourceRequest) Parse() (save MarketIndexSourceSave, revision *int64, err error) {
	v := &httpx.Validator{}
	if r.Source == nil && r.FeedURL == nil && r.FeedKeyHeader == nil && r.FeedKey == nil && r.StalenessDays == nil {
		v.Check(false, "source", "a save must name something to change")
	}
	var source string
	if r.Source != nil {
		source = strings.TrimSpace(*r.Source)
		if source != IndexSourceManual && source != IndexSourceFBConnect {
			v.Check(false, "source", "must be manual or fb_connect_feed")
		}
	}
	var feedURL *string
	if r.FeedURL != nil {
		u := strings.TrimSpace(*r.FeedURL)
		if u != "" {
			if verr := validateFeedURL(u); verr != nil {
				v.Check(false, "feed_url", verr.Error())
			}
		}
		feedURL = &u
	}
	var feedKeyHeader *string
	if r.FeedKeyHeader != nil {
		h := strings.TrimSpace(*r.FeedKeyHeader)
		if h != "" && !validHeaderName(h) {
			v.Check(false, "feed_key_header", "must be an HTTP header name (letters, digits and - _ . only)")
		}
		feedKeyHeader = &h
	}
	var feedKey *string
	if r.FeedKey != nil {
		k := strings.TrimSpace(*r.FeedKey)
		feedKey = &k
	}
	var staleness *int
	if n, ok := v.Int("staleness_days", r.StalenessDays, false); ok {
		v.Check(n >= 1 && n <= 365, "staleness_days", "must be between 1 and 365 days")
		if n >= 1 && n <= 365 {
			d := int(n)
			staleness = &d
		}
	}
	var rev *int64
	if n, ok := v.Int("revision", r.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		rev = &n
	}
	if err := v.Err(); err != nil {
		return MarketIndexSourceSave{}, nil, err
	}
	out := MarketIndexSourceSave{FeedURL: feedURL, FeedKeyHeader: feedKeyHeader, FeedKey: feedKey}
	if r.Source != nil {
		out.Source = &source
	}
	if staleness != nil {
		out.StalenessDays = staleness
	}
	return out, rev, nil
}

// validateFeedURL checks the FB Connect feed address before it is stored. It
// is a secret, so it must be an absolute https URL (http only for loopback)
// and must not carry credentials in its authority: a key has its own header,
// and a userinfo would travel in the request line where it is logged.
func validateFeedURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid feed URL: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("feed URL must be absolute (include scheme and host), e.g. https://connect.example.com/feed")
	}
	if u.User != nil {
		return fmt.Errorf("feed URL must not carry credentials; the key rides in its own header")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackFeedHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("feed URL must use https (plaintext http is only allowed for localhost endpoints)")
	default:
		return fmt.Errorf("feed URL scheme must be http or https, got %q", u.Scheme)
	}
}

// isLoopbackFeedHost reports whether host is a loopback literal or a
// *.localhost name, which RFC 6761 reserves for loopback.
func isLoopbackFeedHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return strings.HasSuffix(strings.ToLower(host), ".localhost")
}

// validHeaderName reports whether name is an HTTP header name (RFC 9110's
// token): letters, digits and the punctuation a header name may carry.
func validHeaderName(name string) bool {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// Parse validates the routing key save.
func (r *SaveRoutingSettingsRequest) Parse() (apiKey string, revision *int64, err error) {
	v := &httpx.Validator{}
	apiKey = ""
	if r.APIKey != nil {
		apiKey = *r.APIKey
	}
	if strings.TrimSpace(apiKey) == "" {
		v.Check(false, "api_key", "is required")
	}
	if n, ok := v.Int("revision", r.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		revision = &n
	}
	if err := v.Err(); err != nil {
		return "", nil, err
	}
	return apiKey, revision, nil
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7E {
			return false
		}
	}
	return true
}
