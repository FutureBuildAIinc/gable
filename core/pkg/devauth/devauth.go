// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package devauth gates AUTH_MODE=dev on a build tag (ADR 0012 section 7).
//
// AUTH_MODE=dev switches staff authentication off. It exists only in a binary
// built with -tags devauth; a binary built without the tag refuses to start
// with it, so the image that is published can never run in dev mode. A dev
// build in turn refuses to start when dev mode and the Appwrite settings are
// both set, so a stack never carries two answers to "who is signed in".
package devauth

import (
	"errors"
	"strings"
)

// Enabled reports whether this binary was built with the devauth tag.
var Enabled = tagged

// Check returns the start refusal for authMode and appwriteEndpoint under this
// build, or nil when the combination is allowed.
func Check(authMode, appwriteEndpoint string) error {
	return check(Enabled, authMode, appwriteEndpoint)
}

func check(enabled bool, authMode, appwriteEndpoint string) error {
	dev := strings.EqualFold(authMode, "dev")
	switch {
	case dev && !enabled:
		return errors.New("AUTH_MODE=dev is set but this binary was built without the devauth tag; build with -tags devauth for a local stack, or unset AUTH_MODE")
	case dev && appwriteEndpoint != "":
		return errors.New("both AUTH_MODE=dev and APPWRITE_ENDPOINT are set; a stack signs in through Appwrite or runs dev mode, not both")
	}
	return nil
}
