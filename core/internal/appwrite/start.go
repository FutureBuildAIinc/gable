// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package appwrite

import (
	"context"
	"errors"
	"time"
)

// StartState is what the core may serve once it has asked Appwrite for its
// version at start (ADR 0012 section 8).
type StartState int

const (
	// StartReady: a version at or above the floor was read; every method is open.
	StartReady StartState = iota
	// StartRedirectsClosed: no version could be read; the core starts, but the
	// provider redirect methods stay closed until a version at or above the
	// floor is read. Email and password sign in does not depend on the floor.
	StartRedirectsClosed
)

// startReadTimeout bounds the version read at start.
const startReadTimeout = 10 * time.Second

// CheckStart reads the server version once. A readable version below the
// floor is an error wrapping ErrUnsupportedVersion and the caller must refuse
// to start. An unreadable version (network error, non 2xx, body that is not a
// version) is not an error: it yields StartRedirectsClosed.
func CheckStart(ctx context.Context, c *Client) (StartState, error) {
	ctx, cancel := context.WithTimeout(ctx, startReadTimeout)
	defer cancel()
	version, err := c.Version(ctx)
	if err != nil || version == "" {
		return StartRedirectsClosed, nil
	}
	if err := CheckFloor(version); err != nil {
		if errors.Is(err, errUnparseable) {
			return StartRedirectsClosed, nil
		}
		return StartRedirectsClosed, err
	}
	return StartReady, nil
}
