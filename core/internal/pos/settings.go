// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The counter's dealer settings (CP-04): the database wide check limit and
// the check ID rule, one resource on GET/PUT /pos/settings behind the admin
// and owner roles (the read stays open to the counter, which asks for the ID
// before Pay). Both rules live in system_settings rows anchored to one
// revision (admin_revisions, the anchor the tech admin settings use), so a
// write is one act: the anchor locked, the revision checked, the rows
// written, the anchor bumped, the audit row beside them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// settingCheckIDRequired is the check ID rule in cents: absent or empty is
// never, 0 is every check, a figure is checks over it. The ID's number is
// never taken, on the screen or on the wire.
const settingCheckIDRequired = "pos.check_id_required_over_cents"

// posSettingsResource anchors the counter settings' revision.
const posSettingsResource = "pos.settings"

// settingsRoles may write the counter settings.
var settingsRoles = []string{"admin", "owner"}

// The ID rule's three shapes, as the wire spells them.
const (
	IDRuleNever      = "never"
	IDRuleEveryCheck = "every_check"
	IDRuleOverAmount = "over_amount"
)

// CheckIDRule is the dealer's ID rule: never (the default), every check, or
// checks over a figure.
type CheckIDRule struct {
	Mode string `json:"mode"`
	// OverCents is the figure an over_amount rule names; null otherwise.
	OverCents *httpx.Cents `json:"over_cents"`
}

// PosRules is the object of named counter rules, shaped so a later rule (the
// restocking fees) sits beside these, not in a key value list.
type PosRules struct {
	// CheckLimitCents is the database wide check limit; null is no limit.
	CheckLimitCents *httpx.Cents `json:"check_limit_cents"`
	CheckIDRequired CheckIDRule  `json:"check_id_required"`
}

// PosSettings is the counter settings resource: its rules and the revision a
// write rides (ADR 0001 section 11).
type PosSettings struct {
	Revision int64    `json:"revision"`
	Rules    PosRules `json:"rules"`
}

// CheckIDRequiredOverCents reads the ID rule; nil is never (absent or empty).
// A failed read is an error, never "never": the rule a dealer asked for is
// not switched off by a broken query.
func (r *PostgresRepository) CheckIDRequiredOverCents(ctx context.Context) (*int64, error) {
	var raw *string
	err := r.ex(ctx).QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, settingCheckIDRequired).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err != nil || raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(*raw), 10, 64)
	if perr != nil || n < 0 {
		return nil, errSettingShape(settingCheckIDRequired, *raw)
	}
	return &n, nil
}

// writeSetting writes one system_settings row inside the caller's act.
func (r *PostgresRepository) writeSetting(ctx context.Context, key, value string) error {
	_, err := r.ex(ctx).Exec(ctx, `INSERT INTO system_settings (key, value, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, key, value)
	if err != nil {
		return errSettingWrite(err)
	}
	return nil
}

// deleteSetting clears one system_settings row inside the caller's act.
func (r *PostgresRepository) deleteSetting(ctx context.Context, key string) error {
	if _, err := r.ex(ctx).Exec(ctx, `DELETE FROM system_settings WHERE key = $1`, key); err != nil {
		return errSettingWrite(err)
	}
	return nil
}

// readPosSettingsRevision reads the counter settings' revision without
// locking (the GET path); a missing anchor is revision 1.
func (r *PostgresRepository) readPosSettingsRevision(ctx context.Context) (int64, error) {
	var rev int64
	err := r.ex(ctx).QueryRow(ctx, `SELECT revision FROM admin_revisions WHERE resource = $1`, posSettingsResource).Scan(&rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, errSettingWrite(err)
	}
	return rev, nil
}

// lockPosSettingsRevision takes the settings' revision anchor FOR UPDATE,
// creating the anchor on the first write; the lock holds for the rest of the
// transaction, so the revision check and the bump are one act.
func (r *PostgresRepository) lockPosSettingsRevision(ctx context.Context) (int64, error) {
	if _, err := r.ex(ctx).Exec(ctx,
		`INSERT INTO admin_revisions (resource, revision) VALUES ($1, 1) ON CONFLICT (resource) DO NOTHING`,
		posSettingsResource); err != nil {
		return 0, errSettingWrite(err)
	}
	var rev int64
	if err := r.ex(ctx).QueryRow(ctx,
		`SELECT revision FROM admin_revisions WHERE resource = $1 FOR UPDATE`, posSettingsResource).Scan(&rev); err != nil {
		return 0, errSettingWrite(err)
	}
	return rev, nil
}

// bumpPosSettingsRevision moves the anchor to the revision the write made.
func (r *PostgresRepository) bumpPosSettingsRevision(ctx context.Context, to int64) error {
	if _, err := r.ex(ctx).Exec(ctx,
		`UPDATE admin_revisions SET revision = $2, updated_at = NOW() WHERE resource = $1`, posSettingsResource, to); err != nil {
		return errSettingWrite(err)
	}
	return nil
}

func errSettingShape(key, value string) error {
	return &httpx.Error{Status: http.StatusInternalServerError, Code: "internal_error",
		Message: "the " + key + " setting is not a whole number of cents: " + strconv.Quote(value)}
}

func errSettingWrite(err error) error {
	return &httpx.Error{Status: http.StatusInternalServerError, Code: "internal_error",
		Message: "failed to write the counter settings: " + err.Error()}
}

// checkTenderIDRule holds the sale's check tenders to the dealer's ID rule
// (pos.check_id_required_over_cents): absent or empty is never, 0 is every
// check, a figure is checks over it read on the sale's checks in total, so
// splitting the money into checks under the figure cannot avoid it. When the
// rule bites, every check tender of the sale must carry id_checked (one
// unchecked check is refused 409 id_required; a checked one frees nothing
// but itself). The rule never asks for the ID's number, which no body
// carries. Cash is never held.
func (s *Service) checkTenderIDRule(ctx context.Context, customerID uuid.UUID, tenders []TenderIn) error {
	var checks int
	var sum int64
	for i := range tenders {
		if tenders[i].Method != TenderCheck {
			continue
		}
		checks++
		sum += int64(tenders[i].AmountCents)
	}
	if checks == 0 {
		return nil
	}
	over, err := s.repo.CheckIDRequiredOverCents(ctx)
	if err != nil {
		return err
	}
	if over == nil {
		return nil
	}
	// every check asks on each tender alone; a figure asks once the sale's
	// checks together pass it
	bites := *over == 0 || sum > *over
	if !bites {
		return nil
	}
	var facts *CustomerFacts
	for i := range tenders {
		if tenders[i].Method != TenderCheck || tenders[i].IDChecked {
			continue
		}
		if facts == nil {
			f, ferr := s.repo.CustomerFacts(ctx, customerID)
			if ferr != nil {
				return ferr
			}
			facts = &f
		}
		if *over == 0 {
			return conflict("id_required",
				"This check needs the customer's ID: check it and pick the ID type")
		}
		return conflict("id_required", fmt.Sprintf(
			"A check over %s needs the customer's ID: check it and pick the ID type",
			moneyWords(facts.Currency, *over)))
	}
	return nil
}

// idRuleOf shapes the raw setting as the wire's rule.
func idRuleOf(over *int64) CheckIDRule {
	switch {
	case over == nil:
		return CheckIDRule{Mode: IDRuleNever}
	case *over == 0:
		return CheckIDRule{Mode: IDRuleEveryCheck}
	default:
		c := httpx.Cents(*over)
		return CheckIDRule{Mode: IDRuleOverAmount, OverCents: &c}
	}
}

// PosSettings reads the counter settings: the two rules and their revision.
func (s *Service) PosSettings(ctx context.Context) (*PosSettings, error) {
	limit, err := s.repo.DefaultCheckLimitCents(ctx)
	if err != nil {
		return nil, err
	}
	idRule, err := s.repo.CheckIDRequiredOverCents(ctx)
	if err != nil {
		return nil, err
	}
	rev, err := s.repo.readPosSettingsRevision(ctx)
	if err != nil {
		return nil, err
	}
	out := &PosSettings{Revision: rev, Rules: PosRules{CheckIDRequired: idRuleOf(idRule)}}
	if limit != nil {
		c := httpx.Cents(*limit)
		out.Rules.CheckLimitCents = &c
	}
	return out, nil
}

// SavePosSettingsInput is the parsed body of PUT /pos/settings.
type SavePosSettingsInput struct {
	Rules      PosRules
	Revision   *int64
	IfMatch    string
	Actor      string
	Authorized bool
}

// SavePosSettings writes both rules as one act on the caller's revision
// (If-Match and/or the body's, ADR 0001 section 11): the anchor locked, the
// revision checked, the rows written, the anchor bumped, the audit row
// beside them in the same transaction (ADR 0003).
func (s *Service) SavePosSettings(ctx context.Context, in *SavePosSettingsInput) (*PosSettings, error) {
	if !in.Authorized {
		return nil, httpx.Forbidden("changing the counter rules needs the admin or owner role")
	}
	var out *PosSettings
	err := s.inTx(ctx, func(ctx context.Context) error {
		rev, err := s.repo.lockPosSettingsRevision(ctx)
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(rev, in.IfMatch, in.Revision); err != nil {
			return err
		}
		// the check limit: null clears the row (no limit)
		if in.Rules.CheckLimitCents == nil {
			if err := s.repo.deleteSetting(ctx, settingCheckLimit); err != nil {
				return err
			}
		} else if err := s.repo.writeSetting(ctx, settingCheckLimit, strconv.FormatInt(int64(*in.Rules.CheckLimitCents), 10)); err != nil {
			return err
		}
		// the ID rule: never clears the row, every check is 0, a figure is it
		switch in.Rules.CheckIDRequired.Mode {
		case IDRuleNever:
			if err := s.repo.deleteSetting(ctx, settingCheckIDRequired); err != nil {
				return err
			}
		case IDRuleEveryCheck:
			if err := s.repo.writeSetting(ctx, settingCheckIDRequired, "0"); err != nil {
				return err
			}
		case IDRuleOverAmount:
			if in.Rules.CheckIDRequired.OverCents == nil || int64(*in.Rules.CheckIDRequired.OverCents) <= 0 {
				return invalid("rules.check_id_required.over_cents", "name the figure a check over needs the ID at")
			}
			if err := s.repo.writeSetting(ctx, settingCheckIDRequired,
				strconv.FormatInt(int64(*in.Rules.CheckIDRequired.OverCents), 10)); err != nil {
				return err
			}
		}
		next := rev + 1
		if err := s.repo.bumpPosSettingsRevision(ctx, next); err != nil {
			return err
		}
		if s.auditLog != nil {
			changes := map[string]any{"resource": posSettingsResource, "revision": next,
				"check_limit_cents": in.Rules.CheckLimitCents, "check_id_required": in.Rules.CheckIDRequired.Mode}
			if in.Rules.CheckIDRequired.OverCents != nil {
				changes["check_id_over_cents"] = int64(*in.Rules.CheckIDRequired.OverCents)
			}
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "pos.settings.saved", EntityType: "pos_settings",
				EntityID: uuid.Nil, UserID: in.Actor, Changes: changes}); err != nil {
				return err
			}
		}
		out, err = s.PosSettings(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// posSettingsRequest is the body of PUT /pos/settings.
type posSettingsRequest struct {
	Rules struct {
		CheckLimitCents json.RawMessage `json:"check_limit_cents"`
		CheckIDRequired struct {
			Mode      string          `json:"mode"`
			OverCents json.RawMessage `json:"over_cents"`
		} `json:"check_id_required"`
	} `json:"rules"`
	Revision json.RawMessage `json:"revision"`
}

func (r *posSettingsRequest) parse() (*SavePosSettingsInput, error) {
	v := &httpx.Validator{}
	out := &SavePosSettingsInput{}
	if len(strings.TrimSpace(string(r.Rules.CheckLimitCents))) > 0 && string(r.Rules.CheckLimitCents) != "null" {
		if n, ok := v.Int("rules.check_limit_cents", r.Rules.CheckLimitCents, true); ok {
			v.Check(n >= 0, "rules.check_limit_cents", "must not be negative; send null for no limit")
			c := httpx.Cents(n)
			out.Rules.CheckLimitCents = &c
		}
	}
	v.Enum("rules.check_id_required.mode", r.Rules.CheckIDRequired.Mode,
		IDRuleNever, IDRuleEveryCheck, IDRuleOverAmount)
	out.Rules.CheckIDRequired.Mode = r.Rules.CheckIDRequired.Mode
	if len(strings.TrimSpace(string(r.Rules.CheckIDRequired.OverCents))) > 0 && string(r.Rules.CheckIDRequired.OverCents) != "null" {
		if n, ok := v.Int("rules.check_id_required.over_cents", r.Rules.CheckIDRequired.OverCents, true); ok {
			v.Check(n > 0, "rules.check_id_required.over_cents",
				"must be greater than zero; 0 or less is the every check rule")
			c := httpx.Cents(n)
			out.Rules.CheckIDRequired.OverCents = &c
		}
	}
	if out.Rules.CheckIDRequired.Mode != IDRuleOverAmount {
		out.Rules.CheckIDRequired.OverCents = nil
	}
	if len(strings.TrimSpace(string(r.Revision))) > 0 && string(r.Revision) != "null" {
		if n, ok := v.Int("revision", r.Revision, true); ok {
			v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
			out.Revision = &n
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetPosSettingsHandler is GET /pos/settings: the counter rules and their
// revision, readable by the counter (it asks for the ID before Pay).
func (h *Handler) GetPosSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.PosSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PutPosSettingsHandler is PUT /pos/settings: an owner or admin writes both
// rules on the revision (ADR 0001); audited. A machine key is refused (a
// dealer wide configuration write is a person's act); a request that carries
// no claims at all is the dev mode bypass every role guard lets through.
func (h *Handler) PutPosSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if refuseMachineKey(w, r, "changing the counter rules needs a signed in manager") {
		return
	}
	in, err := decodePosSettings(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	actor := devApprover
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil {
		if claims.Subject == "" || !middleware.ClaimsHaveAnyRole(claims, settingsRoles...) {
			httpx.WriteError(w, r, httpx.Forbidden("changing the counter rules needs the admin or owner role"))
			return
		}
		actor = claims.Subject
	}
	in.Actor = actor
	in.IfMatch = r.Header.Get("If-Match")
	in.Authorized = true
	out, err := h.service.SavePosSettings(r.Context(), in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, out.Revision)
	writeJSON(w, http.StatusOK, out)
}

// decodePosSettings decodes and parses the PUT body.
func decodePosSettings(r *http.Request) (*SavePosSettingsInput, error) {
	var req posSettingsRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return nil, err
	}
	return req.parse()
}
