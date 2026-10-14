// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/platform/httpx"
)

// The one place the setting keys are spelled.
const (
	keyPrefix         = "market_check."
	keyConnectURL     = keyPrefix + "connect_url"
	keyWebhookID      = keyPrefix + "webhook_id"
	keyFlowName       = keyPrefix + "flow_name"
	keyStoreLabel     = keyPrefix + "store_label"
	keyStoreID        = keyPrefix + "store_id"
	keyStoreZip       = keyPrefix + "store_zip"
	keyBandPct        = keyPrefix + "band_pct"
	keyMarginFloorPct = keyPrefix + "margin_floor_pct"

	defaultFlowName   = "FB Connect pricing flow"
	defaultBandPct    = "5"
	defaultMarginPct  = "15"
	maxLabelLength    = 120
	webhookIDPatternS = `^[A-Za-z0-9_-]{8,64}$`
)

var (
	webhookIDPattern = regexp.MustCompile(webhookIDPatternS)
	storeIDPattern   = regexp.MustCompile(`^[0-9]{1,8}$`)
	storeZipPattern  = regexp.MustCompile(`^[0-9]{5}$`)
	percentPattern   = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,2})?$`)
)

// Settings is the answer of the settings routes. The webhook id is never in
// it: only whether one is saved.
type Settings struct {
	ConnectURL     string `json:"connect_url"`
	WebhookIDSaved bool   `json:"webhook_id_saved"`
	FlowName       string `json:"flow_name"`
	StoreLabel     string `json:"store_label"`
	StoreID        string `json:"store_id"`
	StoreZip       string `json:"store_zip"`
	BandPct        string `json:"band_pct"`
	MarginFloorPct string `json:"margin_floor_pct"`
	Configured     bool   `json:"configured"`
	// Notice is a plain sentence about the saved state when it needs the admin's
	// attention (an id to enter again); empty otherwise.
	Notice string `json:"notice"`
}

// SettingsInput is the body of the settings write. A nil field keeps what is
// saved. WebhookID: nil keeps, "" clears.
type SettingsInput struct {
	ConnectURL     *string `json:"connect_url"`
	WebhookID      *string `json:"webhook_id"`
	FlowName       *string `json:"flow_name"`
	StoreLabel     *string `json:"store_label"`
	StoreID        *string `json:"store_id"`
	StoreZip       *string `json:"store_zip"`
	BandPct        *string `json:"band_pct"`
	MarginFloorPct *string `json:"margin_floor_pct"`
}

// ErrNotConfigured is the run's refusal when the address or the id is unset.
var ErrNotConfigured = &httpx.Error{Status: 409, Code: "not_configured",
	Message: "Market check is off: an admin enters the FB Connect address and the flow's webhook id in Tech Admin"}

func invalidField(field, message string) *httpx.Error {
	return &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed, Message: "the request is not valid",
		Details: []httpx.FieldError{{Field: field, Message: message}}}
}

// FieldOf names the first field of a validation refusal ("" for any other error).
func FieldOf(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) && len(he.Details) > 0 {
		return he.Details[0].Field
	}
	return ""
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func cleanLabel(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > maxLabelLength {
		return "", invalidField(field, "must be at most 120 characters")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", invalidField(field, "must not hold control characters")
		}
	}
	return s, nil
}

// parseConnectURL accepts https://host[:port] only (http for a loopback host)
// and returns it in canonical form with no trailing slash.
func parseConnectURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", invalidField("connect_url", "must be an address like https://connect.example.com")
	}
	if u.User != nil {
		return "", invalidField("connect_url", "must not hold a user name or password")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") {
		return "", invalidField("connect_url", "must be the address only: no path, query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return "", invalidField("connect_url", "must start with https:// (http:// is for a local host only)")
		}
	default:
		return "", invalidField("connect_url", "must start with https://")
	}
	return u.Scheme + "://" + u.Host, nil
}

// parsePercent reads a decimal percent string into hundredths of a percent.
func parsePercent(field, s string, max int64) (bp int64, canon string, err error) {
	s = strings.TrimSpace(s)
	if !percentPattern.MatchString(s) {
		return 0, "", invalidField(field, fmt.Sprintf("must be a percent from 0 to %d, such as 5 or 12.5", max))
	}
	f, _ := strconv.ParseFloat(s, 64)
	bp = int64(f*100 + 0.5)
	if bp > max*100 {
		return 0, "", invalidField(field, fmt.Sprintf("must be a percent from 0 to %d", max))
	}
	return bp, formatBP(bp), nil
}

// formatBP writes hundredths of a percent as a minimal decimal string.
func formatBP(bp int64) string {
	whole, frac := bp/100, bp%100
	switch {
	case frac == 0:
		return strconv.FormatInt(whole, 10)
	case frac%10 == 0:
		return fmt.Sprintf("%d.%d", whole, frac/10)
	}
	return fmt.Sprintf("%d.%02d", whole, frac)
}

// load reads the saved settings (the id stays sealed; the caller opens it).
type loaded struct {
	Settings
	sealedID string
}

func (s *Service) load(ctx context.Context) (loaded, error) {
	get := func(k, def string) (string, error) {
		v, ok, err := s.store.Read(ctx, k)
		if err != nil {
			return "", err
		}
		if !ok || v == "" {
			return def, nil
		}
		return v, nil
	}
	var l loaded
	var err error
	if l.ConnectURL, err = get(keyConnectURL, ""); err != nil {
		return l, err
	}
	if l.sealedID, err = get(keyWebhookID, ""); err != nil {
		return l, err
	}
	if l.FlowName, err = get(keyFlowName, defaultFlowName); err != nil {
		return l, err
	}
	if l.StoreLabel, err = get(keyStoreLabel, ""); err != nil {
		return l, err
	}
	if l.StoreID, err = get(keyStoreID, ""); err != nil {
		return l, err
	}
	if l.StoreZip, err = get(keyStoreZip, ""); err != nil {
		return l, err
	}
	if l.BandPct, err = get(keyBandPct, defaultBandPct); err != nil {
		return l, err
	}
	if l.MarginFloorPct, err = get(keyMarginFloorPct, defaultMarginPct); err != nil {
		return l, err
	}
	l.WebhookIDSaved = l.sealedID != ""
	l.Configured = l.WebhookIDSaved && l.ConnectURL != ""
	return l, nil
}

const (
	noticeNewAddress = "Enter the webhook id again for the new address"
	noticeUnreadable = "The saved webhook id cannot be read; enter it again"
	noticeBadAddress = "The saved FB Connect address is not valid; enter it again"
)

// usable re-validates what is stored, whatever path wrote it: the address
// with the save time parser, the id sealed (never a legacy plain value), opened
// and in the id's pattern. It answers the opened id and the canonical address,
// or a notice when the saved state cannot be used.
func (s *Service) usable(l loaded) (hook, base, notice string) {
	if l.ConnectURL != "" {
		canon, err := parseConnectURL(l.ConnectURL)
		if err != nil || canon == "" || canon != l.ConnectURL {
			return "", "", noticeBadAddress
		}
		base = canon
	}
	if l.sealedID == "" {
		if base != "" {
			return "", base, noticeNewAddress
		}
		return "", base, ""
	}
	if !ai.IsSealedValue(l.sealedID) {
		return "", base, noticeUnreadable
	}
	id, err := ai.OpenValue(s.vault, l.sealedID)
	if err != nil || !webhookIDPattern.MatchString(id) {
		return "", base, noticeUnreadable
	}
	return id, base, ""
}

// Settings answers the saved settings, never the webhook id. Configured is
// true only when the address and the id are both saved and usable.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	l, err := s.load(ctx)
	if err != nil {
		return Settings{}, err
	}
	hook, base, notice := s.usable(l)
	l.Configured = hook != "" && base != ""
	l.Notice = notice
	return l.Settings, nil
}

// SaveSettings validates the whole input, then writes it in one transaction.
func (s *Service) SaveSettings(ctx context.Context, in SettingsInput) (Settings, error) {
	writes := map[string]string{}
	deletes := []string{}
	var changed []string
	setStr := func(key, field string, v *string, clean func(string) (string, error)) error {
		if v == nil {
			return nil
		}
		c, err := clean(*v)
		if err != nil {
			return err
		}
		changed = append(changed, field)
		if c == "" {
			deletes = append(deletes, key)
		} else {
			writes[key] = c
		}
		return nil
	}
	pattern := func(field string, re *regexp.Regexp, words string) func(string) (string, error) {
		return func(s string) (string, error) {
			s = strings.TrimSpace(s)
			if s != "" && !re.MatchString(s) {
				return "", invalidField(field, words)
			}
			return s, nil
		}
	}
	label := func(field string) func(string) (string, error) {
		return func(s string) (string, error) { return cleanLabel(field, s) }
	}
	if err := setStr(keyConnectURL, "connect_url", in.ConnectURL, parseConnectURL); err != nil {
		return Settings{}, err
	}
	if err := setStr(keyFlowName, "flow_name", in.FlowName, label("flow_name")); err != nil {
		return Settings{}, err
	}
	if err := setStr(keyStoreLabel, "store_label", in.StoreLabel, label("store_label")); err != nil {
		return Settings{}, err
	}
	if err := setStr(keyStoreID, "store_id", in.StoreID, pattern("store_id", storeIDPattern, "must be up to 8 digits")); err != nil {
		return Settings{}, err
	}
	if err := setStr(keyStoreZip, "store_zip", in.StoreZip, pattern("store_zip", storeZipPattern, "must be five digits")); err != nil {
		return Settings{}, err
	}
	if err := setStr(keyBandPct, "band_pct", in.BandPct, func(v string) (string, error) {
		_, c, err := parsePercent("band_pct", v, 50)
		return c, err
	}); err != nil {
		return Settings{}, err
	}
	if err := setStr(keyMarginFloorPct, "margin_floor_pct", in.MarginFloorPct, func(v string) (string, error) {
		_, c, err := parsePercent("margin_floor_pct", v, 90)
		return c, err
	}); err != nil {
		return Settings{}, err
	}
	if in.WebhookID != nil {
		id := strings.TrimSpace(*in.WebhookID)
		changed = append(changed, "webhook_id")
		if id == "" {
			deletes = append(deletes, keyWebhookID)
		} else {
			if !webhookIDPattern.MatchString(id) {
				return Settings{}, invalidField("webhook_id", "must be 8 to 64 letters, digits, dashes or underscores")
			}
			sealed, err := ai.SealValue(s.vault, id)
			if err != nil {
				if errors.Is(err, ai.ErrNoVault) {
					return Settings{}, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
						Message: "the webhook id cannot be stored: no vault key is configured (set PAYMENT_VAULT_KEY)",
						Details: []httpx.FieldError{{Field: "webhook_id", Message: "no vault key is configured, so the id is not stored"}}}
				}
				return Settings{}, errors.New("the webhook id could not be sealed")
			}
			writes[keyWebhookID] = sealed
		}
	}
	apply := func(ctx context.Context) error {
		// A changed address does not carry the saved id to the new host: it is
		// cleared unless this same request brings a new one.
		if newURL, ok := writes[keyConnectURL]; ok && in.WebhookID == nil {
			old, _, err := s.store.Read(ctx, keyConnectURL)
			if err != nil {
				return err
			}
			if old != newURL {
				deletes = append(deletes, keyWebhookID)
			}
		}
		// One fixed key order, so two saves at once lock rows in the same
		// order and never deadlock.
		keys := make([]string, 0, len(writes)+len(deletes))
		for k := range writes {
			keys = append(keys, k)
		}
		keys = append(keys, deletes...)
		sort.Strings(keys)
		for _, k := range keys {
			var err error
			if v, ok := writes[k]; ok {
				err = s.store.Write(ctx, k, v)
			} else {
				err = s.store.Delete(ctx, k)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	var err error
	if s.tx != nil {
		err = s.tx.RunInTx(ctx, apply)
	} else {
		err = apply(ctx)
	}
	if err != nil {
		return Settings{}, err
	}
	if s.audit != nil && len(changed) > 0 {
		// field names only: never a value, never the id
		_ = s.audit.Record(ctx, "market_check.settings_saved", changed)
	}
	return s.Settings(ctx)
}
