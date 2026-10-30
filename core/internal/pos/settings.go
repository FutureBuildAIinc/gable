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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/tint"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// settingCheckIDRequired is the check ID rule in cents: absent or empty is
// never, 0 is every check, a figure is checks over it. The ID's number is
// never taken, on the screen or on the wire.
const settingCheckIDRequired = "pos.check_id_required_over_cents"

// settingTintedNoReturn is the dealer's tinted no return policy: absent or
// false is off, because the rule is a dealer policy and not a law.
const settingTintedNoReturn = "pos.tinted_no_return"

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
	// RestockingFees is the fee each return reason withholds, in basis
	// points: the whole closed list, a reason never set carrying none.
	RestockingFees map[string]int `json:"restocking_fees"`
	// RestockingFeeDefaults is the suggested figure for each reason, read
	// only: what the settings screen offers a dealer who has set nothing.
	RestockingFeeDefaults map[string]int `json:"restocking_fee_defaults"`
	// TintCharge is the dealer's tint charge rule (row CP-16): the flat
	// figure per container and the tiers that may cover it. The rule
	// follows the tint, not only the container.
	TintCharge tint.ChargeRule `json:"tint_charge"`
	// TintedNoReturn is the dealer's policy: a tinted can is not taken
	// back. Off unless the dealer says so, because the rule is a dealer
	// policy and not a law.
	TintedNoReturn bool `json:"tinted_no_return"`
	// CardSurcharge is the active branch's card fee switch (CP-15). On a
	// read it is the branch the caller works in (null with no branch in
	// context); on a write, omitted leaves it as it is.
	CardSurcharge *CardSurcharge `json:"card_surcharge"`
}

// MaxCardSurchargeBps is the card brands' cap on a surcharge, 3 percent, in
// basis points. The server refuses a rate above it.
const MaxCardSurchargeBps = 300

// CardSurcharge is one branch's card fee rule (CP-15): the switch (off by
// default) and the rate, in basis points, the clerk and customer are told. It
// records the dealer's intent and the disclosure; the processor's enrolment
// decides the charge, and the answer's figure is what books.
type CardSurcharge struct {
	BranchID uuid.UUID `json:"branch_id"`
	Enabled  bool      `json:"enabled"`
	RateBps  int       `json:"rate_bps"`
}

// BranchCardSurcharge reads the branch's card fee rule: off at zero for a
// branch that never set it.
func (r *PostgresRepository) BranchCardSurcharge(ctx context.Context, branchID uuid.UUID) (CardSurcharge, error) {
	cs := CardSurcharge{BranchID: branchID}
	err := r.ex(ctx).QueryRow(ctx,
		`SELECT card_surcharge_enabled, card_surcharge_bps FROM locations WHERE id = $1`, branchID).Scan(&cs.Enabled, &cs.RateBps)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return cs, err
	}
	return cs, nil
}

// settingsBranch is the branch a branch rule on the counter settings speaks
// for: the branch the caller works in, or in a single branch deployment (the
// branch switch off, no branch in context) the system's default branch. Nil
// when neither is known.
func (r *PostgresRepository) settingsBranch(ctx context.Context) (*uuid.UUID, error) {
	if b := middleware.BranchIDForQuery(ctx); b != nil {
		return b, nil
	}
	var id uuid.UUID
	err := r.ex(ctx).QueryRow(ctx,
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id' AND value ~* '^[0-9a-f-]{36}$'`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// writeBranchCardSurcharge writes the branch's rule inside the settings act.
func (r *PostgresRepository) writeBranchCardSurcharge(ctx context.Context, cs CardSurcharge) error {
	tag, err := r.ex(ctx).Exec(ctx,
		`UPDATE locations SET card_surcharge_enabled = $2, card_surcharge_bps = $3, updated_at = NOW() WHERE id = $1`,
		cs.BranchID, cs.Enabled, cs.RateBps)
	if err != nil {
		return errSettingWrite(err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("branch not found")
	}
	return nil
}

// effectiveRestockingFees states the closed list's fees in force: a reason's
// row when it has one, zero otherwise.
func effectiveRestockingFees(rows map[string]int) map[string]int {
	out := make(map[string]int, len(returnReasons))
	for _, r := range returnReasons {
		out[r.Code] = rows[r.Code]
	}
	return out
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

// writeRestockingFees writes every reason's fee as one act: the prefix's rows
// go first (a reason now at zero keeps no stale row), then a row for each
// figure over zero, in the closed list's order.
func (r *PostgresRepository) writeRestockingFees(ctx context.Context, fees map[string]int) error {
	if _, err := r.ex(ctx).Exec(ctx, `DELETE FROM system_settings WHERE key LIKE $1`,
		settingRestockingFeePrefix+"%"); err != nil {
		return errSettingWrite(err)
	}
	for _, code := range returnReasonCodes() {
		if fees[code] <= 0 {
			continue
		}
		if err := r.writeSetting(ctx, settingRestockingFeePrefix+code, strconv.Itoa(fees[code])); err != nil {
			return err
		}
	}
	return nil
}

// writeTintCharge writes the dealer's tint charge rule (row CP-16) as one
// act: the flat figure per container and the tier list that may cover it. No
// figure and no tier clears both rows, which is no charge (the tint folded
// into the shelf price).
func (r *PostgresRepository) writeTintCharge(ctx context.Context, rule tint.ChargeRule) error {
	if rule.FlatCents == 0 && len(rule.Tiers) == 0 {
		if err := r.deleteSetting(ctx, tint.SettingTintChargeCents); err != nil {
			return err
		}
		return r.deleteSetting(ctx, tint.SettingTintChargeTiers)
	}
	if err := r.writeSetting(ctx, tint.SettingTintChargeCents, strconv.FormatInt(rule.FlatCents, 10)); err != nil {
		return err
	}
	tiers, err := json.Marshal(rule.Tiers)
	if err != nil {
		return errSettingWrite(err)
	}
	return r.writeSetting(ctx, tint.SettingTintChargeTiers, string(tiers))
}

// writeTintedNoReturn writes the dealer's tinted no return policy: off clears
// the row, because the rule is a dealer policy and not a law.
func (r *PostgresRepository) writeTintedNoReturn(ctx context.Context, on bool) error {
	if !on {
		return r.deleteSetting(ctx, settingTintedNoReturn)
	}
	return r.writeSetting(ctx, settingTintedNoReturn, "true")
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
			"Checks over %s in total need the customer's ID: check it and pick the ID type",
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

// PosSettings reads the counter settings: the rules (check limit, check ID,
// restocking fees) and their revision.
func (s *Service) PosSettings(ctx context.Context) (*PosSettings, error) {
	limit, err := s.repo.DefaultCheckLimitCents(ctx)
	if err != nil {
		return nil, err
	}
	idRule, err := s.repo.CheckIDRequiredOverCents(ctx)
	if err != nil {
		return nil, err
	}
	feeRows, err := s.repo.RestockingFeeBps(ctx)
	if err != nil {
		return nil, err
	}
	chargeRule, err := s.repo.TintChargeRule(ctx)
	if err != nil {
		return nil, err
	}
	noReturn, err := s.repo.TintedNoReturn(ctx)
	if err != nil {
		return nil, err
	}
	rev, err := s.repo.readPosSettingsRevision(ctx)
	if err != nil {
		return nil, err
	}
	var surcharge *CardSurcharge
	branch, err := s.repo.settingsBranch(ctx)
	if err != nil {
		return nil, err
	}
	if branch != nil {
		cs, err := s.repo.BranchCardSurcharge(ctx, *branch)
		if err != nil {
			return nil, err
		}
		surcharge = &cs
	}
	out := &PosSettings{Revision: rev, Rules: PosRules{CheckIDRequired: idRuleOf(idRule), CardSurcharge: surcharge,
		RestockingFees:        effectiveRestockingFees(feeRows),
		RestockingFeeDefaults: maps.Clone(defaultRestockingFeeBps),
		TintCharge:            chargeRule,
		TintedNoReturn:        noReturn}}
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
		// the restocking fees: the whole closed list in one write, a row only
		// for a figure over zero (none set meaning no fee)
		if err := s.repo.writeRestockingFees(ctx, in.Rules.RestockingFees); err != nil {
			return err
		}
		// the tint charge rule (row CP-16): the flat figure per container and
		// the tiers that may cover it. No figure and no tier clears the rows,
		// which is no charge (the tint folded into the shelf price).
		if err := s.repo.writeTintCharge(ctx, in.Rules.TintCharge); err != nil {
			return err
		}
		// the tinted no return policy: off clears the row, because the rule is
		// a dealer policy and not a law.
		if err := s.repo.writeTintedNoReturn(ctx, in.Rules.TintedNoReturn); err != nil {
			return err
		}
		// the card fee switch (CP-15) of the branch the caller works in:
		// omitted leaves it alone; the rate is capped at the brands' 3 percent
		var surchargeWritten *CardSurcharge
		if in.Rules.CardSurcharge != nil {
			branch, err := s.repo.settingsBranch(ctx)
			if err != nil {
				return err
			}
			if branch == nil {
				return conflict("branch_required", "the card fee is a branch's rule: work in one branch to change it")
			}
			if in.Rules.CardSurcharge.RateBps < 0 || in.Rules.CardSurcharge.RateBps > MaxCardSurchargeBps {
				return invalid("rules.card_surcharge.rate_bps", "must be from 0 to 300 basis points: the card brands cap a surcharge at 3 percent")
			}
			cs := CardSurcharge{BranchID: *branch, Enabled: in.Rules.CardSurcharge.Enabled, RateBps: in.Rules.CardSurcharge.RateBps}
			if err := s.repo.writeBranchCardSurcharge(ctx, cs); err != nil {
				return err
			}
			surchargeWritten = &cs
		}
		next := rev + 1
		if err := s.repo.bumpPosSettingsRevision(ctx, next); err != nil {
			return err
		}
		if s.auditLog != nil {
			changes := map[string]any{"resource": posSettingsResource, "revision": next,
				"check_limit_cents": in.Rules.CheckLimitCents, "check_id_required": in.Rules.CheckIDRequired.Mode,
				"restocking_fees":   in.Rules.RestockingFees,
				"tint_charge_cents": in.Rules.TintCharge.FlatCents,
				"tint_charge_tiers": in.Rules.TintCharge.Tiers,
				"tinted_no_return":  in.Rules.TintedNoReturn}
			if surchargeWritten != nil {
				changes["card_surcharge_branch_id"] = surchargeWritten.BranchID
				changes["card_surcharge_enabled"] = surchargeWritten.Enabled
				changes["card_surcharge_bps"] = surchargeWritten.RateBps
			}
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
		RestockingFees json.RawMessage `json:"restocking_fees"`
		TintCharge     json.RawMessage `json:"tint_charge"`
		TintedNoReturn json.RawMessage `json:"tinted_no_return"`
		CardSurcharge  json.RawMessage `json:"card_surcharge"`
	} `json:"rules"`
	Revision json.RawMessage `json:"revision"`
}

// parseRestockingFees reads rules.restocking_fees into the whole closed list:
// every reason named, no other key, each figure a whole number of basis
// points from 0 to 10000 (10000 is the line's whole refund: the fee may never
// pass the line).
func parseRestockingFees(v *httpx.Validator, raw json.RawMessage) map[string]int {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		v.Check(false, "rules.restocking_fees", "name every reason's fee, 0 for none")
		return nil
	}
	var rows map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		v.Check(false, "rules.restocking_fees", "must be an object of reason code to basis points")
		return nil
	}
	fees := map[string]int{}
	for _, code := range returnReasonCodes() {
		val, ok := rows[code]
		if !ok || len(bytes.TrimSpace(val)) == 0 || bytes.Equal(bytes.TrimSpace(val), []byte("null")) {
			v.Check(false, "rules.restocking_fees."+code, "name every reason's fee, 0 for none")
			continue
		}
		if n, ok := v.Int("rules.restocking_fees."+code, val, true); ok {
			v.Check(n >= 0 && n <= 10000, "rules.restocking_fees."+code,
				"must be basis points from 0 to 10000: the fee never takes more than the line's whole refund")
			fees[code] = int(n)
		}
	}
	for code := range rows {
		if !isReturnReason(code) {
			v.Check(false, "rules.restocking_fees."+code, "is not a return reason")
		}
	}
	return fees
}

// parseTintCharge reads rules.tint_charge into the dealer's rule: the flat
// figure per container and the tiers that may cover it. Absent is no figure
// and no tier, which is no charge (the tint folded into the shelf price), so
// a dealer who has set nothing writes nothing.
func parseTintCharge(v *httpx.Validator, raw json.RawMessage) tint.ChargeRule {
	rule := tint.ChargeRule{}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return rule
	}
	var body struct {
		FlatCents json.RawMessage `json:"flat_cents"`
		Tiers     json.RawMessage `json:"tiers"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		v.Check(false, "rules.tint_charge", "must be an object with the flat figure and the tiers")
		return rule
	}
	if n, ok := v.Int("rules.tint_charge.flat_cents", body.FlatCents, false); ok {
		v.Check(n >= 0, "rules.tint_charge.flat_cents", "must not be negative")
		if n >= 0 {
			rule.FlatCents = n
		}
	}
	var tiers []struct {
		BaseTier      string          `json:"base_tier"`
		MinColorantOZ string          `json:"min_colorant_oz"`
		ChargeCents   json.RawMessage `json:"charge_cents"`
	}
	if len(bytes.TrimSpace(body.Tiers)) > 0 && !bytes.Equal(bytes.TrimSpace(body.Tiers), []byte("null")) {
		if err := json.Unmarshal(body.Tiers, &tiers); err != nil {
			v.Check(false, "rules.tint_charge.tiers", "must be a list of tiers")
			return rule
		}
	}
	for i, t := range tiers {
		path := fmt.Sprintf("rules.tint_charge.tiers[%d]", i)
		switch t.BaseTier {
		case "", tint.BaseTierFlat, tint.BaseTierDeep:
		default:
			v.Check(false, path+".base_tier", "must be flat, deep, or empty for any base")
		}
		floor := strings.TrimSpace(t.MinColorantOZ)
		if floor != "" {
			if _, ok := new(big.Rat).SetString(floor); !ok {
				v.Check(false, path+".min_colorant_oz", "must be a decimal number of fluid ounces")
			}
		}
		n, ok := v.Int(path+".charge_cents", t.ChargeCents, true)
		if !ok {
			continue
		}
		v.Check(n >= 0, path+".charge_cents", "must not be negative")
		if n < 0 {
			continue
		}
		rule.Tiers = append(rule.Tiers, tint.ChargeTier{
			BaseTier:      t.BaseTier,
			MinColorantOZ: floor,
			ChargeCents:   n,
		})
	}
	return rule
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
	out.Rules.RestockingFees = parseRestockingFees(v, r.Rules.RestockingFees)
	out.Rules.TintCharge = parseTintCharge(v, r.Rules.TintCharge)
	if len(bytes.TrimSpace(r.Rules.TintedNoReturn)) > 0 && !bytes.Equal(bytes.TrimSpace(r.Rules.TintedNoReturn), []byte("null")) {
		var on bool
		if err := json.Unmarshal(r.Rules.TintedNoReturn, &on); err != nil {
			v.Check(false, "rules.tinted_no_return", "must be true or false")
		} else {
			out.Rules.TintedNoReturn = on
		}
	}
	if raw := bytes.TrimSpace(r.Rules.CardSurcharge); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var body struct {
			Enabled json.RawMessage `json:"enabled"`
			RateBps json.RawMessage `json:"rate_bps"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			v.Check(false, "rules.card_surcharge", "must be an object with enabled and rate_bps")
		} else {
			cs := &CardSurcharge{}
			if err := json.Unmarshal(body.Enabled, &cs.Enabled); err != nil {
				v.Check(false, "rules.card_surcharge.enabled", "must be true or false")
			}
			if n, ok := v.Int("rules.card_surcharge.rate_bps", body.RateBps, true); ok {
				v.Check(n >= 0 && n <= MaxCardSurchargeBps, "rules.card_surcharge.rate_bps",
					"must be from 0 to 300 basis points: the card brands cap a surcharge at 3 percent")
				cs.RateBps = int(n)
			}
			out.Rules.CardSurcharge = cs
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

// flagUnexpectedSurcharge writes the audit row of a fee the processor added
// at a branch whose switch is off (enrolment ahead of the setting, CP-15).
// The money moved, so the fee is stored and booked all the same; the row is
// what a manager finds it by. It runs inside the act that books the fee.
func (s *Service) flagUnexpectedSurcharge(ctx context.Context, branchID, saleID uuid.UUID, feeCents int64, actor string) error {
	if feeCents <= 0 || s.auditLog == nil {
		return nil
	}
	rule, err := s.repo.BranchCardSurcharge(ctx, branchID)
	if err != nil {
		return err
	}
	if rule.Enabled {
		return nil
	}
	if err := s.auditLog.Log(ctx, audit.Entry{
		Action: "pos.surcharge_unexpected", EntityType: "pos_transaction", EntityID: saleID, UserID: actor,
		Changes: map[string]any{"branch_id": branchID, "surcharge_cents": feeCents, "switch": "off"}}); err != nil {
		return fmt.Errorf("failed to write the audit row: %w", err)
	}
	return nil
}
