// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// The roster link (ADR 0012 section 5): a staff row carries the sub of the
// person it describes, set by an owner or admin by picking a person who has
// signed in, until the invite (slice 6) sets it itself.
var (
	ErrUnknownPerson = errors.New("staffauth: no person with that subject has signed in")
	ErrNoStaffRow    = errors.New("staffauth: no such staff row")
	ErrAlreadyLinked = errors.New("staffauth: that person or that staff row is already linked")
)

// Person is someone the core's sessions have seen.
type Person struct {
	Sub           uuid.UUID  `json:"sub"`
	Email         string     `json:"email"`
	LinkedStaffID *uuid.UUID `json:"linked_staff_id"`
}

// Links reads the people who have signed in and sets or clears roster links.
type Links struct {
	db    *database.DB
	dir   Directory
	audit AuditSink
}

// NewLinks returns the link service. The email of each person is read from
// Appwrite by the stored Appwrite user id, so the session row holds none.
func NewLinks(db *database.DB, dir Directory, a AuditSink) *Links {
	return &Links{db: db, dir: dir, audit: a}
}

// People lists the distinct subjects the sessions have seen, with the staff
// row each is linked to.
func (l *Links) People(ctx context.Context) ([]Person, error) {
	rows, err := l.db.Pool.Query(ctx,
		`SELECT s.sub, s.appwrite_user_id, st.id
		   FROM (SELECT DISTINCT ON (sub) sub, appwrite_user_id FROM staff_sessions ORDER BY sub, created_at DESC) s
		   LEFT JOIN staff st ON st.sub = s.sub
		  ORDER BY s.sub`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type seen struct {
		p   Person
		uid string
	}
	var all []seen
	for rows.Next() {
		var x seen
		if err := rows.Scan(&x.p.Sub, &x.uid, &x.p.LinkedStaffID); err != nil {
			return nil, err
		}
		all = append(all, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Person, 0, len(all))
	for _, x := range all {
		if u, err := l.dir.GetUser(ctx, x.uid); err == nil {
			x.p.Email = u.Email
		}
		out = append(out, x.p)
	}
	return out, nil
}

// Set links a staff row to a person who has signed in, audited.
func (l *Links) Set(ctx context.Context, staffID, sub uuid.UUID, byActor string) error {
	err := l.db.RunInTx(ctx, func(ctx context.Context) error {
		x := l.db.GetExecutor(ctx)
		var known bool
		if err := x.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM staff_sessions WHERE sub = $1)`, sub).Scan(&known); err != nil {
			return err
		}
		if !known {
			return ErrUnknownPerson
		}
		tag, err := x.Exec(ctx, `UPDATE staff SET sub = $2, updated_at = NOW() WHERE id = $1 AND sub IS NULL`, staffID, sub)
		if err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return ErrAlreadyLinked
			}
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := x.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM staff WHERE id = $1)`, staffID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNoStaffRow
			}
			return ErrAlreadyLinked
		}
		return l.audit.Log(ctx, audit.Entry{Action: "staff.sign_in_linked", EntityType: "staff", EntityID: staffID, UserID: byActor,
			Changes: map[string]any{"sub": sub.String()}})
	})
	return err
}

// Clear removes a staff row's link, audited.
func (l *Links) Clear(ctx context.Context, staffID uuid.UUID, byActor string) error {
	return l.db.RunInTx(ctx, func(ctx context.Context) error {
		x := l.db.GetExecutor(ctx)
		var was *uuid.UUID
		err := x.QueryRow(ctx, `SELECT sub FROM staff WHERE id = $1 FOR UPDATE`, staffID).Scan(&was)
		if err != nil {
			return ErrNoStaffRow
		}
		if was == nil {
			return nil
		}
		if _, err := x.Exec(ctx, `UPDATE staff SET sub = NULL, updated_at = NOW() WHERE id = $1`, staffID); err != nil {
			return err
		}
		return l.audit.Log(ctx, audit.Entry{Action: "staff.sign_in_unlinked", EntityType: "staff", EntityID: staffID, UserID: byActor,
			Changes: map[string]any{"sub": was.String()}})
	})
}

// RegisterRoutes mounts the three routes. They sit under /api/v1/admin/staff,
// so the admin:staff key scope reaches them exactly as it reaches the roster;
// the caller wraps them in the admin and owner guard.
func (l *Links) RegisterRoutes(mux *http.ServeMux, guard ...func(http.Handler) http.Handler) {
	wrap := func(h http.HandlerFunc) http.Handler {
		var out http.Handler = h
		for _, g := range guard {
			out = g(out)
		}
		return out
	}
	mux.Handle("GET /api/v1/admin/staff/sign-in-people", wrap(l.handlePeople))
	mux.Handle("PUT /api/v1/admin/staff/{id}/sign-in-link", wrap(l.handleSet))
	mux.Handle("DELETE /api/v1/admin/staff/{id}/sign-in-link", wrap(l.handleClear))
}

func actorOf(r *http.Request) string {
	if c := middleware.ClaimsFromContext(r.Context()); c != nil {
		return c.Subject
	}
	return ""
}

func (l *Links) handlePeople(w http.ResponseWriter, r *http.Request) {
	people, err := l.People(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": people})
}

func (l *Links) linkErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrUnknownPerson):
		httpx.WriteError(w, r, httpx.BadRequest(err.Error()))
	case errors.Is(err, ErrNoStaffRow):
		httpx.WriteError(w, r, httpx.NotFound(err.Error()))
	case errors.Is(err, ErrAlreadyLinked):
		httpx.WriteError(w, r, httpx.Conflict(err.Error()))
	default:
		httpx.WriteError(w, r, err)
	}
}

func (l *Links) handleSet(w http.ResponseWriter, r *http.Request) {
	staffID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}
	var body struct {
		Sub string `json:"sub"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("request body must be {sub}"))
		return
	}
	sub, err := uuid.Parse(body.Sub)
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("sub must be a UUID"))
		return
	}
	if err := l.Set(r.Context(), staffID, sub, actorOf(r)); err != nil {
		l.linkErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Links) handleClear(w http.ResponseWriter, r *http.Request) {
	staffID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}
	if err := l.Clear(r.Context(), staffID, actorOf(r)); err != nil {
		l.linkErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
