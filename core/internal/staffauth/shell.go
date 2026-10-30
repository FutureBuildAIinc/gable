// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/appwrite"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// The shells' return targets (ADR 0012 section 3): the registered gable
// scheme with its fixed callback, or a loopback address with any port and the
// fixed callback path. Nothing else is ever a return target.
const (
	DeepLinkTarget       = "gable://auth/callback"
	LoopbackCallbackPath = "/auth/callback"
)

// codeTTL is how long the one time code a callback hands a shell may wait to
// be redeemed.
const codeTTL = 2 * time.Minute

// ShellStart is what a shell adds to a start: the PKCE challenge and the
// validated return target.
type ShellStart struct {
	CodeChallenge string
	ReturnTarget  string
}

// ErrBadShellStart is a shell start whose challenge or return target is not
// acceptable.
var ErrBadShellStart = errors.New("staffauth: shell start parameters refused")

// ValidateReturnTarget accepts only the registered scheme target or a
// loopback target (127.0.0.1 or [::1], a port, the fixed path), compared
// exactly: no userinfo, query, fragment or other host rides through.
func ValidateReturnTarget(raw string) bool {
	if raw == DeepLinkTarget {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "::1" {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != u.Port() {
		return false
	}
	want := "http://" + u.Host + LoopbackCallbackPath
	return raw == want && u.Path == LoopbackCallbackPath
}

// NewShellStart validates a shell's start parameters. The challenge is an
// S256 challenge: 43 base64url characters, no padding.
func NewShellStart(challenge, method, returnTo string) (*ShellStart, error) {
	if method != "S256" || len(challenge) != 43 || !isB64URL(challenge) || !ValidateReturnTarget(returnTo) {
		return nil, ErrBadShellStart
	}
	return &ShellStart{CodeChallenge: challenge, ReturnTarget: returnTo}, nil
}

func isB64URL(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// challengeOf is the S256 transform of a PKCE verifier.
func challengeOf(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// validVerifier is RFC 7636's verifier shape: 43 to 128 unreserved characters.
func validVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~') {
			return false
		}
	}
	return true
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ShellTokens are what a redeem or a refresh answers.
type ShellTokens struct {
	Access    string
	Refresh   string
	ExpiresIn int // seconds until the session's absolute limit
	Identity  Identity
}

// newShellPair makes a fresh access token and a refresh token of the family.
func newShellPair(family uuid.UUID) (access, refresh string, err error) {
	a, err := randomToken()
	if err != nil {
		return "", "", err
	}
	r, err := randomToken()
	if err != nil {
		return "", "", err
	}
	return middleware.ShellTokenPrefix + a, family.String() + "." + r, nil
}

// familyOf reads the family a refresh token names. The family is public (it
// is the token's prefix); the secret half is what the hash covers.
func familyOf(refresh string) (uuid.UUID, bool) {
	i := strings.IndexByte(refresh, '.')
	if i < 0 || i == len(refresh)-1 {
		return uuid.Nil, false
	}
	f, err := uuid.Parse(refresh[:i])
	return f, err == nil
}

// shellCallbackDone finishes a shell's callback: the person is admitted, so
// the core files the hash of a fresh one time code on the pending row and
// names the person there, and the browser goes back to the shell's own return
// target with the code.
func (s *Service) shellCallbackDone(ctx context.Context, hash []byte, p Pending, who Identity, awUserID string) (string, error) {
	code, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := s.store.AttachPerson(ctx, hash, who.Sub, awUserID); err != nil {
		return "", err
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	ok, err := s.store.SetCode(ctx, hash, hashID(code), now.Add(codeTTL))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrRefused
	}
	return p.ReturnTarget + "?code=" + url.QueryEscape(code), nil
}

// Redeem spends a shell's one time code with its PKCE verifier for an access
// token and a refresh token. The code is claimed with one conditional update;
// a wrong verifier changes nothing. The membership is read afresh before the
// session is issued, so a person removed since the callback gets nothing.
func (s *Service) Redeem(ctx context.Context, code, verifier string) (ShellTokens, error) {
	attempt := uuid.NewSHA1(subjectNamespace, []byte("code:"+hex8(hashID(code))))
	refuse := func(reason string) (ShellTokens, error) {
		s.auditEvent(ctx, "staff.sign_in_refused", attempt, "", map[string]any{
			"reason": reason, "method": "shell_redeem", "client_kind": "shell",
		})
		return ShellTokens{}, ErrRefused
	}
	if code == "" || !validVerifier(verifier) {
		return refuse("bad_request")
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	sub, awUserID, ok, err := s.store.ClaimCode(ctx, hashID(code), challengeOf(verifier), now)
	if err != nil {
		return ShellTokens{}, err
	}
	if !ok {
		return refuse("code_unusable")
	}
	roles, err := s.readAdmission(ctx, awUserID)
	switch {
	case errors.Is(err, ErrUnavailable):
		return ShellTokens{}, err
	case err != nil:
		return refuse(refusalReason(err))
	}
	family := uuid.New()
	access, refresh, err := newShellPair(family)
	if err != nil {
		return ShellTokens{}, err
	}
	row := Session{
		IDHash: hashID(access), Sub: sub, AppwriteUserID: awUserID, ClientKind: "shell", Roles: roles,
		RolesReadAt: now, CreatedAt: now, IdleSeconds: int(s.set.IdleLimit.Seconds()),
		AbsoluteExpiresAt: now.Add(s.set.AbsoluteLimit), RefreshHash: hashID(refresh), TokenFamily: &family,
	}
	if err := s.store.Create(ctx, row); err != nil {
		return ShellTokens{}, err
	}
	s.auditEvent(ctx, "staff.sign_in", sub, sub.String(), map[string]any{"client_kind": "shell", "roles": roles})
	return ShellTokens{Access: access, Refresh: refresh, ExpiresIn: int(s.set.AbsoluteLimit.Seconds()), Identity: Identity{Sub: sub, Roles: roles}}, nil
}

// readAdmission reads the account and the membership with the key and
// applies the admission decision. No Appwrite session is involved.
func (s *Service) readAdmission(ctx context.Context, awUserID string) ([]string, error) {
	if s.floorClosed(ctx) {
		return nil, ErrUnavailable
	}
	cctx, cancel := context.WithTimeout(ctx, s.set.CallTimeout)
	defer cancel()
	user, err := s.dir.GetUser(cctx, awUserID)
	if err != nil {
		var ae *appwrite.Error
		if errors.As(err, &ae) && ae.StatusCode == http.StatusNotFound {
			return nil, appwrite.ErrUserBlocked
		}
		s.log.Warn("appwrite read failed at redemption", "error", err)
		return nil, ErrUnavailable
	}
	m, found, err := s.dir.ListMemberships(cctx, s.set.TeamID, awUserID)
	if err != nil {
		s.log.Warn("appwrite membership read failed at redemption", "error", err)
		return nil, ErrUnavailable
	}
	if err := appwrite.Admit(user, m, found); err != nil {
		return nil, err
	}
	return sortRoles(appwrite.MapRoles(m.Roles)), nil
}

// ShellRefresh rotates a shell's tokens. The presented refresh token must be
// the family's current one: one conditional update swaps both tokens, so of
// any number of simultaneous refreshes on one token at most one rotates. A
// rotated token presented again is a reuse and revokes the whole family.
func (s *Service) ShellRefresh(ctx context.Context, refresh string) (ShellTokens, error) {
	family, ok := familyOf(refresh)
	if !ok {
		return ShellTokens{}, ErrRefused
	}
	access, newRefresh, err := newShellPair(family)
	if err != nil {
		return ShellTokens{}, err
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	row, rotated, err := s.store.RotateShell(ctx, family, hashID(refresh), hashID(access), hashID(newRefresh), now)
	if err != nil {
		return ShellTokens{}, err
	}
	if !rotated {
		n, rerr := s.store.RevokeFamily(ctx, family, "refresh_reuse", now)
		if rerr != nil {
			return ShellTokens{}, rerr
		}
		if n > 0 {
			s.auditEvent(ctx, "staff.refresh_reuse_revoked", family, "", map[string]any{"client_kind": "shell", "sessions": n})
		}
		return ShellTokens{}, ErrRefused
	}
	s.auditEvent(ctx, "staff.refresh_rotated", row.Sub, row.Sub.String(), map[string]any{"client_kind": "shell"})
	return ShellTokens{Access: access, Refresh: newRefresh, ExpiresIn: int(row.AbsoluteExpiresAt.Sub(now).Seconds()), Identity: Identity{Sub: row.Sub, Roles: row.Roles}}, nil
}

// ShellSignOut revokes the shell session behind an access token.
func (s *Service) ShellSignOut(ctx context.Context, access string) {
	row, err := s.store.Get(ctx, hashID(access))
	if err != nil || row.ClientKind != "shell" {
		return
	}
	if ok, _ := s.store.Revoke(ctx, row.IDHash, "sign_out", s.now().UTC()); ok {
		s.auditEvent(ctx, "staff.sign_out", row.Sub, row.Sub.String(), map[string]any{"client_kind": "shell"})
	}
}

func hex8(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 16)
	for _, c := range b[:8] {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}
