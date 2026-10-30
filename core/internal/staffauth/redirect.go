// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/appwrite"
	"github.com/google/uuid"
)

// CallbackPath is Gable's callback, the one URL Appwrite is ever given.
const CallbackPath = "/api/v1/auth/callback"

// pendingTTL is how long a redirect sign in may take, from the start to the
// callback.
const pendingTTL = 10 * time.Minute

// RedirectDirectory is the slice of the Appwrite client the redirect methods
// add to Directory. *appwrite.Client satisfies it.
type RedirectDirectory interface {
	Directory
	OAuth2TokenURL(provider, success, failure string) string
	CreateSessionFromToken(ctx context.Context, userID, secret string) (appwrite.Session, error)
	FindUserByEmail(ctx context.Context, email string) (appwrite.User, bool, error)
	CreateMagicURLToken(ctx context.Context, userID, email, redirectURL string) error
}

// ErrMethod is a method this layer does not start by redirect.
var ErrMethod = errors.New("staffauth: not a redirect sign in method")

// WithRedirects turns the redirect methods on. origin is Gable's own public
// origin, from which the callback URL is built.
func (s *Service) WithRedirects(rd RedirectDirectory, origin string) *Service {
	s.rdir = rd
	s.origin = strings.TrimRight(origin, "/")
	return s
}

// providerOf maps a Gable method code to the Appwrite provider name.
func providerOf(method string) (string, bool) {
	switch method {
	case "google", "microsoft", "oidc":
		return method, true
	}
	return "", false
}

// redirectsOpen reports whether the provider redirect methods may run: the
// Appwrite version must have been read at or above the floor (ADR 0012
// section 8), so a deployment whose version cannot be read stays closed. With
// no floor watch (a test seam) the methods are open.
func (s *Service) redirectsOpen(ctx context.Context) bool {
	if s.rdir == nil {
		return false
	}
	if s.floor == nil {
		return true
	}
	if s.floorClosed(ctx) {
		return false
	}
	s.floor.mu.Lock()
	defer s.floor.mu.Unlock()
	return s.floor.verified
}

func (s *Service) callbackURL(state string, failure bool) string {
	q := url.Values{}
	q.Set("state", state)
	if failure {
		q.Set("failure", "1")
	}
	return s.origin + CallbackPath + "?" + q.Encode()
}

// filePending files a new redirect sign in and returns its raw state.
func (s *Service) filePending(ctx context.Context, method string) (string, error) {
	state, err := newSessionID()
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	err = s.store.CreatePending(ctx, Pending{
		StateHash: hashID(state), Method: method, ClientKind: "web", ExpiresAt: now.Add(pendingTTL),
	}, now)
	return state, err
}

// StartRedirect begins a provider sign in (google, microsoft, oidc): it files
// the pending row and returns the raw state, for the cookie, and the Appwrite
// address the browser navigates to. Both Appwrite return URLs are Gable's
// callback and nothing else.
func (s *Service) StartRedirect(ctx context.Context, method string) (state, location string, err error) {
	provider, ok := providerOf(method)
	if !ok {
		return "", "", ErrMethod
	}
	if !s.redirectsOpen(ctx) {
		return "", "", ErrUnavailable
	}
	if state, err = s.filePending(ctx, method); err != nil {
		return "", "", err
	}
	return state, s.rdir.OAuth2TokenURL(provider, s.callbackURL(state, false), s.callbackURL(state, true)), nil
}

// StartMagicLink emails a sign in link to an existing, admissible member and
// to nobody else. It always returns a raw state for the cookie and the same
// nil error whether or not a link went out, so the reply tells a stranger
// nothing; it creates no Appwrite user, because it only ever passes the id of
// one Appwrite already holds. Only a closed redirect floor is reported.
func (s *Service) StartMagicLink(ctx context.Context, email string) (string, error) {
	if !s.redirectsOpen(ctx) {
		return "", ErrUnavailable
	}
	email = strings.TrimSpace(email)
	sent, reason := false, "unknown_email"
	state, err := newSessionID()
	if err != nil {
		return "", err
	}
	if email != "" {
		sent, reason = s.sendMagicLink(ctx, email, state)
	} else {
		reason = "missing_email"
	}
	attempt := uuid.NewSHA1(subjectNamespace, []byte("email:"+strings.ToLower(email)))
	s.auditEvent(ctx, "staff.magic_link_requested", attempt, "", map[string]any{
		"email": email, "sent": sent, "reason": reason,
	})
	return state, nil
}

// sendMagicLink files the pending row and asks Appwrite for the link when the
// email belongs to a confirmed member with a mapped role and a verified
// address. The row is filed before the call so a link can never arrive for a
// state the core does not know.
func (s *Service) sendMagicLink(ctx context.Context, email, state string) (bool, string) {
	cctx, cancel := context.WithTimeout(ctx, s.set.CallTimeout)
	defer cancel()
	user, found, err := s.rdir.FindUserByEmail(cctx, email)
	if err != nil {
		s.log.Warn("magic link user lookup failed", "error", err)
		return false, "lookup_failed"
	}
	if !found {
		return false, "unknown_email"
	}
	m, isMember, err := s.rdir.ListMemberships(cctx, s.set.TeamID, user.ID)
	if err != nil {
		s.log.Warn("magic link membership read failed", "error", err)
		return false, "lookup_failed"
	}
	if err := appwrite.Admit(user, m, isMember); err != nil {
		return false, refusalReason(err)
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	if err := s.store.CreatePending(ctx, Pending{
		StateHash: hashID(state), Method: "magic_link", ClientKind: "web", ExpiresAt: now.Add(pendingTTL),
	}, now); err != nil {
		s.log.Error("filing the magic link sign in failed", "error", err)
		return false, "store_failed"
	}
	if err := s.rdir.CreateMagicURLToken(cctx, user.ID, user.Email, s.callbackURL(state, false)); err != nil {
		s.log.Warn("magic link request failed", "error", err)
		return false, "send_failed"
	}
	return true, "sent"
}

// Callback finishes a redirect sign in. The state in the cookie and the state
// in the query must match; the pending row is claimed with one conditional
// update, committed, before Appwrite is called, so a state is spent once; then
// the user id and one time secret are exchanged for an Appwrite session that
// the shared tail reads, ends and turns into the core's own session.
func (s *Service) Callback(ctx context.Context, cookieState, queryState, userID, secret string, providerFailed bool) (string, Identity, error) {
	if cookieState == "" || queryState == "" || subtle.ConstantTimeCompare([]byte(cookieState), []byte(queryState)) != 1 {
		return s.refuseCallback(ctx, queryState, "", "state_mismatch")
	}
	hash := hashID(queryState)
	if !s.redirectsOpen(ctx) {
		return "", Identity{}, ErrUnavailable
	}
	pending, claimed, err := s.store.ClaimPending(ctx, hash, s.now().UTC())
	if err != nil {
		return "", Identity{}, err
	}
	if !claimed {
		return s.refuseCallback(ctx, queryState, "", "state_unusable")
	}
	method := pending.Method
	refuse := func(reason string, err error) (string, Identity, error) {
		s.auditEvent(ctx, "staff.sign_in_refused", callbackAttempt(hash), "", map[string]any{
			"reason": reason, "method": method, "client_kind": pending.ClientKind,
		})
		return "", Identity{}, err
	}
	if providerFailed || userID == "" || secret == "" {
		return refuse("provider_failed", ErrRefused)
	}
	cctx, cancel := context.WithTimeout(ctx, s.set.CallTimeout)
	aws, err := s.rdir.CreateSessionFromToken(cctx, userID, secret)
	cancel()
	if err != nil {
		var ae *appwrite.Error
		if errors.As(err, &ae) && ae.StatusCode >= 400 && ae.StatusCode < 500 && ae.StatusCode != http.StatusTooManyRequests {
			return refuse("bad_token", ErrRefused)
		}
		s.log.Warn("appwrite token exchange unavailable", "error", err)
		return "", Identity{}, ErrUnavailable
	}
	id, who, err := s.admitSession(ctx, aws, pending.ClientKind, refuse)
	if err != nil {
		return "", Identity{}, err
	}
	if err := s.store.AttachPerson(ctx, hash, who.Sub, aws.UserID); err != nil {
		s.log.Warn("recording the person on the pending sign in failed", "error", err)
	}
	return id, who, nil
}

func callbackAttempt(stateHash []byte) uuid.UUID {
	return uuid.NewSHA1(subjectNamespace, []byte("pending:"+hex.EncodeToString(stateHash)))
}

// refuseCallback audits a refusal that happened before any claim or exchange.
func (s *Service) refuseCallback(ctx context.Context, queryState, method, reason string) (string, Identity, error) {
	s.auditEvent(ctx, "staff.sign_in_refused", callbackAttempt(hashID(queryState)), "", map[string]any{
		"reason": reason, "method": method, "client_kind": "web",
	})
	return "", Identity{}, ErrRefused
}
