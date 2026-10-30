// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/appwrite"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Directory is the slice of the Appwrite client the session layer uses.
// *appwrite.Client satisfies it.
type Directory interface {
	CreateEmailPasswordSession(ctx context.Context, email, password string) (appwrite.Session, error)
	GetUser(ctx context.Context, id string) (appwrite.User, error)
	ListMemberships(ctx context.Context, teamID, userID string) (appwrite.Membership, bool, error)
	DeleteSessionAsUser(ctx context.Context, sessionID, secret string) error
}

// AuditSink is the audit logger seam; *audit.Logger satisfies it.
type AuditSink interface {
	Log(ctx context.Context, e audit.Entry) error
}

// Settings are the session limits and the Appwrite coordinates.
type Settings struct {
	ProjectID      string
	TeamID         string
	IdleLimit      time.Duration // a session unused this long ends
	AbsoluteLimit  time.Duration // a session ends this long after sign in, however used
	RefreshAfter   time.Duration // the membership is re-read when the last read is older
	SnapshotMaxAge time.Duration // an unconfirmed snapshot older than this is refused
	TouchGap       time.Duration // last use is written at most this often
}

// DefaultSettings fills any zero limit.
func (s Settings) withDefaults() Settings {
	if s.IdleLimit <= 0 {
		s.IdleLimit = 4 * time.Hour
	}
	if s.AbsoluteLimit <= 0 {
		s.AbsoluteLimit = 12 * time.Hour
	}
	if s.RefreshAfter <= 0 {
		s.RefreshAfter = 5 * time.Minute
	}
	if s.SnapshotMaxAge <= 0 {
		s.SnapshotMaxAge = time.Hour
	}
	if s.TouchGap <= 0 {
		s.TouchGap = time.Minute
	}
	return s
}

// Refusals the handler maps to the one fixed answer, and the reason each
// writes to the audit row.
var (
	// ErrRefused is any sign in refusal: wrong credentials, or a person the
	// admission decision refuses. The wire answer is the same for all.
	ErrRefused = errors.New("staffauth: sign in refused")
	// ErrUnavailable is an Appwrite fault that decided nothing.
	ErrUnavailable = errors.New("staffauth: identity provider unavailable")
	// ErrNoSession is a session id that is unknown, revoked or expired.
	ErrNoSession = errors.New("staffauth: no valid session")
)

// Service signs people in and resolves their sessions.
type Service struct {
	dir   Directory
	store *Store
	audit AuditSink
	set   Settings
	now   func() time.Time
	log   *slog.Logger
}

// NewService returns the session service.
func NewService(dir Directory, store *Store, auditSink AuditSink, set Settings, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{dir: dir, store: store, audit: auditSink, set: set.withDefaults(), now: time.Now, log: log}
}

// Identity is what a sign in yields for the response body.
type Identity struct {
	Sub   uuid.UUID
	Roles []string
}

// refusalReason names an admission refusal for the audit row.
func refusalReason(err error) string {
	switch {
	case errors.Is(err, appwrite.ErrNoMembership):
		return "not_a_member"
	case errors.Is(err, appwrite.ErrMembershipUnconfirmed):
		return "membership_unconfirmed"
	case errors.Is(err, appwrite.ErrUserBlocked):
		return "user_blocked"
	case errors.Is(err, appwrite.ErrEmailUnverified):
		return "email_unverified"
	case errors.Is(err, appwrite.ErrMFAEnabled):
		return "mfa_enabled"
	case errors.Is(err, appwrite.ErrOwnerRole):
		return "appwrite_owner_role"
	case errors.Is(err, appwrite.ErrNoGableRoles):
		return "no_gable_role"
	default:
		return "refused"
	}
}

func (s *Service) auditEvent(ctx context.Context, action string, entity uuid.UUID, sub string, changes map[string]any) {
	if s.audit == nil {
		return
	}
	if err := s.audit.Log(ctx, audit.Entry{Action: action, EntityType: "staff_session", EntityID: entity, UserID: sub, Changes: changes}); err != nil {
		s.log.Error("staff sign in audit row failed", "action", action, "error", err)
	}
}

// SignIn opens an Appwrite session server side, reads the account and the
// membership, ends the Appwrite session at once and, if the person is
// admitted, issues a fresh core session. It returns the raw session id for
// the cookie; only its hash is stored.
func (s *Service) SignIn(ctx context.Context, email, password, clientKind string) (string, Identity, error) {
	email = strings.TrimSpace(email)
	attempt := uuid.NewSHA1(subjectNamespace, []byte("email:"+strings.ToLower(email)))
	refuse := func(reason string, err error) (string, Identity, error) {
		s.auditEvent(ctx, "staff.sign_in_refused", attempt, "", map[string]any{
			"reason": reason, "email": email, "client_kind": clientKind,
		})
		return "", Identity{}, err
	}
	if email == "" || password == "" {
		return refuse("missing_credentials", ErrRefused)
	}
	aws, err := s.dir.CreateEmailPasswordSession(ctx, email, password)
	if err != nil {
		var ae *appwrite.Error
		if errors.As(err, &ae) && ae.StatusCode >= 400 && ae.StatusCode < 500 && ae.StatusCode != http.StatusTooManyRequests {
			return refuse("bad_credentials", ErrRefused)
		}
		s.log.Warn("appwrite sign in unavailable", "error", err)
		return "", Identity{}, ErrUnavailable
	}
	// Nothing Appwrite issued outlives this call.
	defer func() {
		if err := s.dir.DeleteSessionAsUser(context.WithoutCancel(ctx), aws.ID, aws.Secret); err != nil {
			s.log.Warn("ending the appwrite session failed", "error", err)
		}
	}()
	user, err := s.dir.GetUser(ctx, aws.UserID)
	if err != nil {
		return s.readFailure(ctx, err, refuse)
	}
	m, found, err := s.dir.ListMemberships(ctx, s.set.TeamID, aws.UserID)
	if err != nil {
		return s.readFailure(ctx, err, refuse)
	}
	if err := appwrite.Admit(user, m, found); err != nil {
		return refuse(refusalReason(err), ErrRefused)
	}
	id, err := newSessionID()
	if err != nil {
		return "", Identity{}, err
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	roles := sortRoles(appwrite.MapRoles(m.Roles))
	sub := Subject(s.set.ProjectID, aws.UserID)
	row := Session{
		IDHash: hashID(id), Sub: sub, AppwriteUserID: aws.UserID, ClientKind: clientKind, Roles: roles,
		RolesReadAt: now, CreatedAt: now, IdleSeconds: int(s.set.IdleLimit.Seconds()),
		AbsoluteExpiresAt: now.Add(s.set.AbsoluteLimit),
	}
	if err := s.store.Create(ctx, row); err != nil {
		return "", Identity{}, err
	}
	s.auditEvent(ctx, "staff.sign_in", sub, sub.String(), map[string]any{"client_kind": clientKind, "roles": roles})
	return id, Identity{Sub: sub, Roles: roles}, nil
}

// readFailure maps a failed account or membership read during sign in.
func (s *Service) readFailure(ctx context.Context, err error, refuse func(string, error) (string, Identity, error)) (string, Identity, error) {
	var ae *appwrite.Error
	if errors.As(err, &ae) && ae.StatusCode == http.StatusNotFound {
		return refuse("user_deleted", ErrRefused)
	}
	s.log.Warn("appwrite read failed during sign in", "error", err)
	return "", Identity{}, ErrUnavailable
}

// SignOut revokes the session behind id. There is no Appwrite session to end.
func (s *Service) SignOut(ctx context.Context, id string) {
	row, err := s.store.Get(ctx, hashID(id))
	if err != nil {
		return
	}
	if ok, _ := s.store.Revoke(ctx, row.IDHash, "sign_out", s.now().UTC()); ok {
		s.auditEvent(ctx, "staff.sign_out", row.Sub, row.Sub.String(), map[string]any{"client_kind": row.ClientKind})
	}
}

// Resolve turns a presented session id into claims. Every failure is
// ErrNoSession: an unknown, revoked, idle expired or absolutely expired
// session, a person removed at refresh, or a snapshot too old to trust.
func (s *Service) Resolve(ctx context.Context, id string) (*middleware.UserClaims, error) {
	if id == "" {
		return nil, ErrNoSession
	}
	row, err := s.store.Get(ctx, hashID(id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNoSession
		}
		return nil, err
	}
	now := s.now().UTC()
	if row.RevokedAt != nil || !now.Before(row.AbsoluteExpiresAt) ||
		!now.Before(row.LastUsedAt.Add(time.Duration(row.IdleSeconds)*time.Second)) {
		return nil, ErrNoSession
	}
	if now.Sub(row.RolesReadAt) > s.set.RefreshAfter {
		if row, err = s.refresh(ctx, row, now); err != nil {
			return nil, err
		}
	}
	if err := s.store.Touch(ctx, row.IDHash, now, s.set.TouchGap); err != nil {
		s.log.Warn("recording session use failed", "error", err)
	}
	return &middleware.UserClaims{
		RegisteredClaims: jwtSubject(row.Sub.String()),
		Roles:            row.Roles,
	}, nil
}

// Refresh forces the membership re-read for the session behind id and
// reports the roles it now holds.
func (s *Service) Refresh(ctx context.Context, id string) (Identity, error) {
	row, err := s.store.Get(ctx, hashID(id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Identity{}, ErrNoSession
		}
		return Identity{}, err
	}
	now := s.now().UTC()
	if row.RevokedAt != nil || !now.Before(row.AbsoluteExpiresAt) {
		return Identity{}, ErrNoSession
	}
	if row, err = s.refresh(ctx, row, now); err != nil {
		return Identity{}, err
	}
	return Identity{Sub: row.Sub, Roles: row.Roles}, nil
}

// refresh re-reads the account and the membership with the key. A definite
// answer that the person may no longer enter revokes the session; changed
// roles replace the snapshot. An answer that is not definite keeps the
// session on its snapshot until the snapshot reaches its maximum age.
func (s *Service) refresh(ctx context.Context, row Session, now time.Time) (Session, error) {
	user, err := s.dir.GetUser(ctx, row.AppwriteUserID)
	var m appwrite.Membership
	found := false
	definite := true
	if err != nil {
		var ae *appwrite.Error
		if errors.As(err, &ae) && ae.StatusCode == http.StatusNotFound {
			return s.revokeAtRefresh(ctx, row, now, "user_deleted")
		}
		definite = false
	} else if m, found, err = s.dir.ListMemberships(ctx, s.set.TeamID, row.AppwriteUserID); err != nil {
		definite = false
	}
	if !definite {
		if now.Sub(row.RolesReadAt) > s.set.SnapshotMaxAge {
			return row, ErrNoSession
		}
		return row, nil
	}
	if err := appwrite.Admit(user, m, found); err != nil {
		return s.revokeAtRefresh(ctx, row, now, refusalReason(err))
	}
	roles := sortRoles(appwrite.MapRoles(m.Roles))
	changed, err := s.store.ReplaceRoles(ctx, row.IDHash, roles, now.Truncate(time.Microsecond))
	if err != nil {
		return row, err
	}
	if !changed {
		// The row was revoked, or refreshed by another request, in between.
		cur, gerr := s.store.Get(ctx, row.IDHash)
		if gerr != nil || cur.RevokedAt != nil {
			return row, ErrNoSession
		}
		return cur, nil
	}
	if !equalRoles(roles, row.Roles) {
		s.auditEvent(ctx, "staff.roles_changed", row.Sub, row.Sub.String(), map[string]any{"roles": roles})
	}
	row.Roles, row.RolesReadAt = roles, now
	return row, nil
}

func (s *Service) revokeAtRefresh(ctx context.Context, row Session, now time.Time, reason string) (Session, error) {
	if ok, _ := s.store.Revoke(ctx, row.IDHash, reason, now); ok {
		s.auditEvent(ctx, "staff.session_revoked", row.Sub, row.Sub.String(), map[string]any{"reason": reason, "client_kind": row.ClientKind})
	}
	return row, ErrNoSession
}
