// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OfflinePolicy is a branch's resolved offline selling policy (ADR 0011
// section 4): the branch's own row in pos_offline_policy when set (source
// "branch"), else the database wide defaults in system_settings (source
// "default"), else off (source "none"). EnrolledRegisterCount is the number
// of active (unrevoked) enrolled registers on the branch, and
// MaxAccountExposureCents is AccountCapCents times EnrolledRegisterCount,
// the worst case exposure for one account across the branch's enrolled
// registers during a connection cut.
type OfflinePolicy struct {
	BranchID                uuid.UUID        `json:"branch_id"`
	Enabled                 bool             `json:"enabled"`
	Tenders                 []string         `json:"tenders"`
	SaleCapCents            int64            `json:"sale_cap_cents"`
	CheckCapCents           int64            `json:"check_cap_cents"`
	AccountCapCents         int64            `json:"account_cap_cents"`
	QueueTotalCapCents      int64            `json:"queue_total_cap_cents"`
	MaxQueued               int              `json:"max_queued"`
	EnrolledRegisterCount   int              `json:"enrolled_register_count"`
	MaxAccountExposureCents int64            `json:"max_account_exposure_cents"`
	Source                  string           `json:"source"`
	UpdatedBy               *string          `json:"updated_by"`
	UpdatedAt               *httpx.Timestamp `json:"updated_at"`
	Revision                *int64           `json:"revision"`
}

// OfflinePolicyInput is a parsed PUT /pos/offline/policy write. When Clear is
// true, the branch override row is deleted so the branch falls back to the
// database wide default or none.
type OfflinePolicyInput struct {
	BranchID           uuid.UUID
	Clear              bool
	Enabled            bool
	Tenders            []string
	SaleCapCents       int64
	CheckCapCents      int64
	AccountCapCents    int64
	QueueTotalCapCents int64
	MaxQueued          int
}

type offlinePolicyRequest struct {
	BranchID           *string         `json:"branch_id"`
	Clear              *bool           `json:"clear"`
	Enabled            *bool           `json:"enabled"`
	Tenders            *[]string       `json:"tenders"`
	SaleCapCents       json.RawMessage `json:"sale_cap_cents"`
	CheckCapCents      json.RawMessage `json:"check_cap_cents"`
	AccountCapCents    json.RawMessage `json:"account_cap_cents"`
	QueueTotalCapCents json.RawMessage `json:"queue_total_cap_cents"`
	MaxQueued          json.RawMessage `json:"max_queued"`
}

func (r *offlinePolicyRequest) parse() (*OfflinePolicyInput, error) {
	v := &httpx.Validator{}
	branch, _ := v.UUID("branch_id", r.BranchID, true)
	out := &OfflinePolicyInput{
		BranchID: branch,
		Tenders:  []string{},
	}
	if r.Clear != nil && *r.Clear {
		out.Clear = true
		if err := v.Err(); err != nil {
			return nil, err
		}
		return out, nil
	}
	if r.Enabled == nil {
		v.Check(false, "enabled", "is required unless clear is true")
	} else {
		out.Enabled = *r.Enabled
	}
	if r.Tenders != nil {
		seen := make(map[string]bool, len(*r.Tenders))
		for i, raw := range *r.Tenders {
			field := fmt.Sprintf("tenders[%d]", i)
			switch raw {
			case "cash", "check", "account":
				upper := strings.ToUpper(raw)
				if !seen[upper] {
					seen[upper] = true
					out.Tenders = append(out.Tenders, upper)
				}
			default:
				v.Check(false, field, "must be one of: cash, check, account")
			}
		}
	}
	parseCap := func(field string, raw json.RawMessage) int64 {
		if isAbsentRaw(raw) {
			return 0
		}
		n, ok := v.Int(field, raw, false)
		if ok {
			v.Check(n >= 0, field, "must be zero or more")
		}
		return n
	}
	out.SaleCapCents = parseCap("sale_cap_cents", r.SaleCapCents)
	out.CheckCapCents = parseCap("check_cap_cents", r.CheckCapCents)
	out.AccountCapCents = parseCap("account_cap_cents", r.AccountCapCents)
	out.QueueTotalCapCents = parseCap("queue_total_cap_cents", r.QueueTotalCapCents)
	if !isAbsentRaw(r.MaxQueued) {
		n, ok := v.Int("max_queued", r.MaxQueued, false)
		if ok {
			v.Check(n >= 0 && n <= 100000, "max_queued", "must be between 0 and 100000")
			out.MaxQueued = int(n)
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetOfflinePolicy reads the branch's offline selling policy: branch row in
// pos_offline_policy first, then system_settings (pos.offline.*), then none
// (enabled false).
func (r *PostgresRepository) GetOfflinePolicy(ctx context.Context, branch uuid.UUID) (*OfflinePolicy, error) {
	ex := r.ex(ctx)
	var enrolledCount int
	if err := ex.QueryRow(ctx, `
		SELECT count(*)::int
		FROM pos_register_devices
		WHERE branch_id = $1 AND revoked_at IS NULL`, branch).Scan(&enrolledCount); err != nil {
		return nil, err
	}

	p := &OfflinePolicy{
		BranchID:              branch,
		Tenders:               []string{},
		EnrolledRegisterCount: enrolledCount,
		Source:                "none",
	}

	var enabled bool
	var tenders []string
	var saleCap, checkCap, accountCap, queueTotalCap int64
	var maxQueued int
	var by string
	var at time.Time
	var rev int64
	err := ex.QueryRow(ctx, `
		SELECT enabled, tenders, sale_cap_cents, check_cap_cents, account_cap_cents,
		       queue_total_cap_cents, max_queued, updated_by, updated_at, revision
		FROM pos_offline_policy
		WHERE branch_id = $1`, branch).Scan(
		&enabled, &tenders, &saleCap, &checkCap, &accountCap,
		&queueTotalCap, &maxQueued, &by, &at, &rev,
	)
	if err == nil {
		ts := httpx.TimestampOf(at)
		p.Enabled = enabled
		p.Tenders = lowercaseOfflineTenders(tenders)
		p.SaleCapCents = saleCap
		p.CheckCapCents = checkCap
		p.AccountCapCents = accountCap
		p.QueueTotalCapCents = queueTotalCap
		p.MaxQueued = maxQueued
		p.MaxAccountExposureCents = accountCap * int64(enrolledCount)
		p.Source = "branch"
		p.UpdatedBy = &by
		p.UpdatedAt = &ts
		p.Revision = &rev
		return p, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	rows, err := ex.Query(ctx, `
		SELECT key, value
		FROM system_settings
		WHERE key IN (
			'pos.offline.enabled',
			'pos.offline.tenders',
			'pos.offline.sale_cap_cents',
			'pos.offline.check_cap_cents',
			'pos.offline.account_cap_cents',
			'pos.offline.queue_total_cap_cents',
			'pos.offline.max_queued'
		)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var k, val string
		if err := rows.Scan(&k, &val); err != nil {
			return nil, err
		}
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		found = true
		switch k {
		case "pos.offline.enabled":
			p.Enabled = strings.EqualFold(val, "true") || val == "1" || strings.EqualFold(val, "on")
		case "pos.offline.tenders":
			p.Tenders = parseSettingOfflineTenders(val)
		case "pos.offline.sale_cap_cents":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil && n >= 0 {
				p.SaleCapCents = n
			}
		case "pos.offline.check_cap_cents":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil && n >= 0 {
				p.CheckCapCents = n
			}
		case "pos.offline.account_cap_cents":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil && n >= 0 {
				p.AccountCapCents = n
			}
		case "pos.offline.queue_total_cap_cents":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil && n >= 0 {
				p.QueueTotalCapCents = n
			}
		case "pos.offline.max_queued":
			if n, err := strconv.Atoi(val); err == nil && n >= 0 {
				p.MaxQueued = n
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if found {
		p.Source = "default"
		p.MaxAccountExposureCents = p.AccountCapCents * int64(enrolledCount)
	}
	return p, nil
}

func lowercaseOfflineTenders(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		v := strings.ToLower(strings.TrimSpace(t))
		if v == "cash" || v == "check" || v == "account" {
			out = append(out, v)
		}
	}
	return out
}

func parseSettingOfflineTenders(raw string) []string {
	var parts []string
	if strings.HasPrefix(raw, "[") {
		_ = json.Unmarshal([]byte(raw), &parts)
	} else {
		parts = strings.Split(raw, ",")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, p := range parts {
		v := strings.ToLower(strings.TrimSpace(p))
		if (v == "cash" || v == "check" || v == "account") && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// SetOfflinePolicy upserts or clears a branch's pos_offline_policy row.
func (r *PostgresRepository) SetOfflinePolicy(ctx context.Context, branch uuid.UUID, in *OfflinePolicyInput, actor string) error {
	if in.Clear {
		_, err := r.ex(ctx).Exec(ctx, `DELETE FROM pos_offline_policy WHERE branch_id = $1`, branch)
		return err
	}
	if actor == "" {
		actor = "anonymous"
	}
	tenders := in.Tenders
	if tenders == nil {
		tenders = []string{}
	}
	_, err := r.ex(ctx).Exec(ctx, `
		INSERT INTO pos_offline_policy (
			branch_id, enabled, tenders, sale_cap_cents, check_cap_cents,
			account_cap_cents, queue_total_cap_cents, max_queued,
			updated_by, updated_at, revision
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), 1)
		ON CONFLICT (branch_id) DO UPDATE SET
			enabled               = EXCLUDED.enabled,
			tenders               = EXCLUDED.tenders,
			sale_cap_cents        = EXCLUDED.sale_cap_cents,
			check_cap_cents       = EXCLUDED.check_cap_cents,
			account_cap_cents     = EXCLUDED.account_cap_cents,
			queue_total_cap_cents = EXCLUDED.queue_total_cap_cents,
			max_queued            = EXCLUDED.max_queued,
			updated_by            = EXCLUDED.updated_by,
			updated_at            = NOW(),
			revision              = pos_offline_policy.revision + 1`,
		branch, in.Enabled, tenders, in.SaleCapCents, in.CheckCapCents,
		in.AccountCapCents, in.QueueTotalCapCents, in.MaxQueued, actor)
	return err
}

// GetOfflinePolicy reads a branch's offline counter policy with its source
// and worst case per account exposure.
func (s *Service) GetOfflinePolicy(ctx context.Context, branch uuid.UUID) (*OfflinePolicy, error) {
	if err := s.grants.CheckPayloadBranch(ctx, branch); err != nil {
		return nil, branchRefusal(err, "branch_id")
	}
	if _, ok, err := s.repo.BranchZone(ctx, branch); err != nil {
		return nil, err
	} else if !ok {
		return nil, httpx.NotFound("branch not found")
	}
	return s.repo.GetOfflinePolicy(ctx, branch)
}

// SetOfflinePolicy sets or clears a branch's offline counter policy and
// writes a synchronous pos.offline.policy audit row in the same transaction.
func (s *Service) SetOfflinePolicy(ctx context.Context, in *OfflinePolicyInput, actor string) (*OfflinePolicy, error) {
	if err := s.grants.CheckPayloadBranch(ctx, in.BranchID); err != nil {
		return nil, branchRefusal(err, "branch_id")
	}
	var out *OfflinePolicy
	err := s.inTx(ctx, func(ctx context.Context) error {
		if _, ok, err := s.repo.BranchZone(ctx, in.BranchID); err != nil {
			return err
		} else if !ok {
			return httpx.NotFound("branch not found")
		}
		if err := s.repo.LockBranchDay(ctx, in.BranchID); err != nil {
			return err
		}
		if err := s.repo.SetOfflinePolicy(ctx, in.BranchID, in, actor); err != nil {
			return err
		}
		p, err := s.repo.GetOfflinePolicy(ctx, in.BranchID)
		if err != nil {
			return err
		}
		if s.auditLog != nil {
			changes := map[string]any{
				"branch_id": in.BranchID,
				"clear":     in.Clear,
				"source":    p.Source,
			}
			if !in.Clear {
				changes["enabled"] = p.Enabled
				changes["tenders"] = p.Tenders
				changes["sale_cap_cents"] = p.SaleCapCents
				changes["check_cap_cents"] = p.CheckCapCents
				changes["account_cap_cents"] = p.AccountCapCents
				changes["queue_total_cap_cents"] = p.QueueTotalCapCents
				changes["max_queued"] = p.MaxQueued
			}
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action:     "pos.offline.policy",
				EntityType: "location",
				EntityID:   in.BranchID,
				UserID:     actor,
				Changes:    changes,
			}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		out = p
		return nil
	})
	return out, err
}

// GetOfflinePolicy is GET /pos/offline/policy?branch_id=.
func (h *Handler) GetOfflinePolicy(w http.ResponseWriter, r *http.Request) {
	vals, err := httpx.StrictQuery(r, "branch_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	branch, err := uuid.Parse(vals.Get("branch_id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "branch_id", Message: "is required and must be a UUID"}))
		return
	}
	p, err := h.service.GetOfflinePolicy(r.Context(), branch)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// PutOfflinePolicy is PUT /pos/offline/policy: sets or clears a branch's
// offline counter policy.
func (h *Handler) PutOfflinePolicy(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "a machine key cannot set the offline policy") {
		return
	}
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req offlinePolicyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.service.SetOfflinePolicy(r.Context(), in, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
