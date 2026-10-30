// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"fmt"
	"net/url"
	"strings"
)

// Config is what the start checks read (ADR 0012 section 8).
type Config struct {
	Endpoint  string
	ProjectID string
	TeamID    string
	APIKey    string
	// Origin is Gable's own public origin, from which the callback and the
	// invite proof URLs are built.
	Origin   string
	AuthMode string
}

// Enabled reports whether any Appwrite setting is present. Settings are all
// or nothing: a half configured core must not start as if it had none.
func (c Config) Enabled() bool {
	return c.Endpoint != "" || c.ProjectID != "" || c.TeamID != "" || c.APIKey != ""
}

// Check refuses a configuration the core must not start with: dev mode beside
// Appwrite settings, a missing setting, or an endpoint or origin that is not
// an absolute http(s) URL. It never echoes a value, the key least of all.
func (c Config) Check() error {
	if !c.Enabled() {
		return nil
	}
	if strings.EqualFold(c.AuthMode, "dev") {
		return fmt.Errorf("AUTH_MODE=dev cannot run with Appwrite settings; unset one of them")
	}
	for _, f := range []struct{ name, value string }{
		{"APPWRITE_ENDPOINT", c.Endpoint},
		{"APPWRITE_PROJECT_ID", c.ProjectID},
		{"APPWRITE_TEAM_ID", c.TeamID},
		{"APPWRITE_API_KEY", c.APIKey},
		{"GABLE_PUBLIC_URL", c.Origin},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("%s is required when any Appwrite setting is set", f.name)
		}
	}
	for _, f := range []struct{ name, value string }{{"APPWRITE_ENDPOINT", c.Endpoint}, {"GABLE_PUBLIC_URL", c.Origin}} {
		u, err := url.Parse(f.value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s must be an absolute http or https URL", f.name)
		}
	}
	return nil
}
