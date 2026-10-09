// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

// ErrInvalidKey is the credential verdict from ValidateKey: the presented
// key is unknown, revoked, or malformed. Callers that map errors onto HTTP
// verdicts (the machine-key auth core) distinguish it from infrastructure
// faults, which carry different status codes.
var ErrInvalidKey = errors.New("invalid api key")

// EventRecorder writes a domain event into the transactional outbox, as the
// LAST statement of the mutation's transaction (ADR 0003 section 2).
// *outbox.Writer satisfies it.
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// AuditSink writes an audit row through the caller's executor, so inside a
// transaction the row commits or rolls back with the act it describes.
// *audit.Logger satisfies it.
type AuditSink interface {
	Log(ctx context.Context, entry audit.Entry) error
}

// Precondition is the client's revision for a settings write: the If-Match
// header and/or the body's revision (ADR 0001 section 11).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

func (p Precondition) check(current int64) error {
	return httpx.CheckRevision(current, p.IfMatch, p.Revision)
}

// settingsEntityID is the stable id of a settings resource on the events
// feed and the audit trail. The outbox names UUID entities and a settings
// row has none, so the resource's name is hashed into the URL namespace:
// deterministic (a consumer can group by it), stable, and derived from
// nothing a caller controls.
func settingsEntityID(resource string) uuid.UUID {
	return uuid.NewMD5(uuid.NameSpaceURL, []byte("admin-settings/"+resource))
}

type Service struct {
	repo   Repository
	events EventRecorder
	tx     TxRunner
	audit  AuditSink
	now    func() time.Time

	// envAIKey, envAIBaseURL and envORSKey are the deployment's environment
	// fallbacks for the three settings rows (the values internal/ai's stores
	// fall back to). A key that comes only from the environment reads as
	// configured with source "env", the same answer the settings screen has
	// always shown; only an admin override reads as "admin".
	envAIKey     string
	envAIBaseURL string
	envORSKey    string
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo, now: time.Now}
}

// WithSettingsDefaults wires the environment fallbacks of the settings rows
// (the same values serve passes the AI stack's key stores).
func (s *Service) WithSettingsDefaults(aiKey, aiBaseURL, orsKey string) *Service {
	s.envAIKey, s.envAIBaseURL, s.envORSKey = aiKey, aiBaseURL, orsKey
	return s
}

// WithOutbox wires the recorder of key and settings events.
func (s *Service) WithOutbox(events EventRecorder) *Service {
	s.events = events
	return s
}

// WithTxRunner wires the transaction wrapper every write uses, so the
// mutation, its revision move, its audit row and its event are one
// transactional fact.
func (s *Service) WithTxRunner(tx TxRunner) *Service {
	s.tx = tx
	return s
}

// WithAuditLog wires the audit sink. A nil sink is ignored rather than
// stored (a typed nil boxed in the interface would panic on first use).
func (s *Service) WithAuditLog(a AuditSink) *Service {
	if a == nil {
		return s
	}
	s.audit = a
	return s
}

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

// ValidateGrantScopes checks the scopes of a mint against the grant grammar
// (ADR 0007 section 5.3): every scope must be one the vocabulary can grant
// (middleware.ValidScopeGrammar, the one source, so the mint and the auth
// core cannot disagree), and the propose and commit verbs are grantable only
// on a confirm gated module. A scope outside the grammar is a 400
// validation_failed naming scopes[i]: a key could already be minted with a
// typo that silently granted nothing, and with four verbs the typo space is
// wider; the mint is where the operator can still fix it. No stored key
// changes (migration 103 reports the strays, by id and prefix).
func ValidateGrantScopes(scopes []string) error {
	grammar := make(map[string]bool)
	for _, scope := range middleware.ValidScopeGrammar() {
		grammar[scope] = true
	}
	v := &httpx.Validator{}
	for i, scope := range scopes {
		path := "scopes[" + strconv.Itoa(i) + "]"
		if !grammar[scope] {
			if _, verb, found := strings.Cut(scope, ":"); found && (verb == "propose" || verb == "commit") {
				v.Check(false, path, "propose and commit are grantable only on a module with a registered draft kind")
			} else {
				v.Check(false, path, "is not a scope the grant grammar knows: <module>:<verb>, the verb read, write, propose or commit")
			}
		}
	}
	return v.Err()
}

// GenerateKey mints a machine key, hashes it, stores the hash, and answers
// with the raw key (the only time it is visible) beside its row. The scopes
// are validated against the grant grammar before anything is written
// (ValidateGrantScopes); a granted scope still reads back as written (ADR
// 0002). Branch, when not nil, pins the key to one branch (ADR 0007 section
// 5.5): it is stored at mint and never edited. The mint, its audit row and
// key.created are one transaction.
func (s *Service) GenerateKey(ctx context.Context, name string, scopes []string, branch *uuid.UUID) (string, *APIKey, error) {
	if err := ValidateGrantScopes(scopes); err != nil {
		return "", nil, err
	}
	if branch != nil {
		exists, err := s.repo.BranchExists(ctx, *branch)
		if err != nil {
			return "", nil, err
		}
		if !exists {
			return "", nil, &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed,
				Message: "branch_id names no branch",
				Details: []httpx.FieldError{{Field: "branch_id", Message: "must be a branch location"}}}
		}
	}
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", nil, err
	}
	rawKey := "sk_live_" + base64.RawURLEncoding.EncodeToString(keyBytes)

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", nil, err
	}
	hash := argon2.IDKey([]byte(rawKey), salt, 1, 64*1024, 4, 32)
	storedHash := base64.RawURLEncoding.EncodeToString(salt) + "$" + base64.RawURLEncoding.EncodeToString(hash)

	if scopes == nil {
		scopes = []string{}
	}
	apiKey := &APIKey{
		ID:        uuid.New(),
		Name:      name,
		KeyHash:   storedHash,
		KeyPrefix: rawKey[:12],
		Scopes:    scopes,
		BranchID:  branch,
		CreatedAt: httpx.TimestampOf(s.now().UTC()),
	}

	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.CreateKey(ctx, apiKey); err != nil {
			return err
		}
		if s.audit != nil {
			changes := map[string]any{"name": name, "scopes": scopes}
			if branch != nil {
				changes["branch_id"] = branch.String()
			}
			if err := s.audit.Log(ctx, audit.Entry{
				Action:     "key.created",
				EntityType: "api_key",
				EntityID:   apiKey.ID,
				Changes:    changes,
			}); err != nil {
				return err
			}
		}
		data := map[string]any{"name": name, "scopes": scopes, "prefix": apiKey.KeyPrefix}
		if branch != nil {
			data["branch_id"] = branch.String()
		}
		return s.record(ctx, "key.created", "api_key", apiKey.ID, data, 0)
	})
	if err != nil {
		return "", nil, err
	}
	return rawKey, apiKey, nil
}

// ValidateKey checks a raw key against the stored hashes, constant time per
// candidate, and touches last_used_at on the match. A key that is malformed,
// unknown or revoked fails with ErrInvalidKey; anything else is an
// infrastructure fault. It runs on the auth path, never inside a
// transaction, so the last used touch goes to the pool.
func (s *Service) ValidateKey(ctx context.Context, rawKey string) (*APIKey, error) {
	if len(rawKey) < 12 {
		return nil, fmt.Errorf("%w: key shorter than the stored prefix length", ErrInvalidKey)
	}
	prefix := rawKey[:12]
	candidates, err := s.repo.GetKeysByPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	for _, k := range candidates {
		salt, expected, ok := splitHash(k.KeyHash)
		if !ok {
			continue
		}
		computed := argon2.IDKey([]byte(rawKey), salt, 1, 64*1024, 4, 32)
		if subtle.ConstantTimeCompare(expected, computed) == 1 {
			_ = s.repo.UpdateLastUsed(ctx, k.ID)
			return k, nil
		}
	}
	return nil, ErrInvalidKey
}

// splitHash decodes the "salt$hash" storage form.
func splitHash(h string) (salt, hash []byte, ok bool) {
	for i := 0; i < len(h); i++ {
		if h[i] != '$' {
			continue
		}
		var err error
		if salt, err = base64.RawURLEncoding.DecodeString(h[:i]); err != nil {
			return nil, nil, false
		}
		if hash, err = base64.RawURLEncoding.DecodeString(h[i+1:]); err != nil {
			return nil, nil, false
		}
		return salt, hash, true
	}
	return nil, nil, false
}

// ListKeys returns one page of keys, newest first, and whether more follow.
func (s *Service) ListKeys(ctx context.Context, f ListFilter, wantTotal bool) (items []APIKey, hasMore bool, total *int64, err error) {
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.ListKeys(ctx, f)
	if err != nil {
		return nil, false, nil, err
	}
	if len(rows) > limit {
		rows, hasMore = rows[:limit], true
	}
	if wantTotal {
		n, err := s.repo.CountKeys(ctx)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return rows, hasMore, total, nil
}

// RevokeKey revokes a key: the row is locked and read first, so an unknown
// id is a 404 and an already revoked key is an idempotent no-op that writes
// nothing. A revoke that flips the row writes its audit row and key.revoked
// in the same transaction.
func (s *Service) RevokeKey(ctx context.Context, id uuid.UUID) (revoked bool, err error) {
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockKey(ctx, id); err != nil {
			return err
		}
		k, err := s.repo.GetKey(ctx, id)
		if err != nil {
			return err
		}
		if k.Revoked != nil {
			revoked = false
			return nil
		}
		if err := s.repo.RevokeKey(ctx, id); err != nil {
			return err
		}
		revoked = true
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				Action:     "key.revoked",
				EntityType: "api_key",
				EntityID:   id,
				Changes:    map[string]any{"name": k.Name, "prefix": k.KeyPrefix},
			}); err != nil {
				return err
			}
		}
		return s.record(ctx, "key.revoked", "api_key", id, map[string]any{
			"name": k.Name, "prefix": k.KeyPrefix,
		}, 0)
	})
	return revoked, err
}

// --- the settings documents ---

// GetAISettings reads the AI settings document: the key's status (a stored
// row is the admin override; a key that comes only from the environment is
// env), the base URL when an override is stored, and the resource revision.
func (s *Service) GetAISettings(ctx context.Context) (*AISettings, error) {
	key, hasKey, err := s.repo.ReadSetting(ctx, settingAIKey)
	if err != nil {
		return nil, err
	}
	baseURL, hasBase, err := s.repo.ReadSetting(ctx, settingAIBaseURL)
	if err != nil {
		return nil, err
	}
	rev, err := s.repo.ReadRevision(ctx, ResourceAISettings)
	if err != nil {
		return nil, err
	}
	if !hasKey {
		key = s.envAIKey
	}
	out := &AISettings{Revision: rev, Source: "none"}
	if key != "" {
		out.Configured = true
		out.KeyHint = keyHint(key)
		if hasKey {
			out.Source = "admin"
		} else {
			out.Source = "env"
		}
	}
	// Only an override is surfaced, so the UI can tell "override set" from
	// "using the default" and never re-persists the default back.
	if hasBase {
		out.BaseURL = &baseURL
	}
	return out, nil
}

// SaveAISettings writes the key and, when the body names it, the base URL in
// one transaction on the client's revision: the anchor row is locked, the
// revision checked, the rows written, the anchor bumped, the audit row
// written and the event written last. A baseURL of nil leaves the override
// alone; an empty one clears it, reverting to the environment default.
func (s *Service) SaveAISettings(ctx context.Context, apiKey string, baseURL *string, pre Precondition) (*AISettings, error) {
	var out *AISettings
	err := s.inTx(ctx, func(ctx context.Context) error {
		rev, err := s.repo.LockRevision(ctx, ResourceAISettings)
		if err != nil {
			return err
		}
		if err := pre.check(rev); err != nil {
			return err
		}
		if err := s.repo.WriteSetting(ctx, settingAIKey, apiKey); err != nil {
			return err
		}
		if baseURL != nil {
			if *baseURL == "" {
				if err := s.repo.DeleteSetting(ctx, settingAIBaseURL); err != nil {
					return err
				}
			} else if err := s.repo.WriteSetting(ctx, settingAIBaseURL, *baseURL); err != nil {
				return err
			}
		}
		next := rev + 1
		if err := s.repo.BumpRevision(ctx, ResourceAISettings, next); err != nil {
			return err
		}
		// Re-read for the response before the sinks, so the event stays the
		// transaction's LAST statement (the recipe's service order).
		out, err = s.GetAISettings(ctx)
		if err != nil {
			return err
		}
		changes := map[string]any{"resource": ResourceAISettings, "revision": next}
		if baseURL != nil {
			changes["base_url_cleared"] = *baseURL == ""
		}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				Action:     "settings.saved",
				EntityType: "admin_settings",
				EntityID:   settingsEntityID(ResourceAISettings),
				Changes:    changes,
			}); err != nil {
				return err
			}
		}
		return s.record(ctx, "admin_settings.saved", "admin_settings", settingsEntityID(ResourceAISettings), changes, next)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAISettings removes the admin override (key and base URL together, so
// both revert to their environment defaults) on the client's revision.
func (s *Service) DeleteAISettings(ctx context.Context, pre Precondition) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		rev, err := s.repo.LockRevision(ctx, ResourceAISettings)
		if err != nil {
			return err
		}
		if err := pre.check(rev); err != nil {
			return err
		}
		if err := s.repo.DeleteSetting(ctx, settingAIKey); err != nil {
			return err
		}
		if err := s.repo.DeleteSetting(ctx, settingAIBaseURL); err != nil {
			return err
		}
		next := rev + 1
		if err := s.repo.BumpRevision(ctx, ResourceAISettings, next); err != nil {
			return err
		}
		changes := map[string]any{"resource": ResourceAISettings, "revision": next}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				Action:     "settings.deleted",
				EntityType: "admin_settings",
				EntityID:   settingsEntityID(ResourceAISettings),
				Changes:    changes,
			}); err != nil {
				return err
			}
		}
		return s.record(ctx, "admin_settings.deleted", "admin_settings", settingsEntityID(ResourceAISettings), changes, next)
	})
}

// GetRoutingSettings reads the routing settings document, the AI settings'
// shape without the base URL.
func (s *Service) GetRoutingSettings(ctx context.Context) (*RoutingSettings, error) {
	key, hasKey, err := s.repo.ReadSetting(ctx, settingORSKey)
	if err != nil {
		return nil, err
	}
	rev, err := s.repo.ReadRevision(ctx, ResourceRoutingSettings)
	if err != nil {
		return nil, err
	}
	if !hasKey {
		key = s.envORSKey
	}
	out := &RoutingSettings{Revision: rev, Source: "none"}
	if key != "" {
		out.Configured = true
		out.KeyHint = keyHint(key)
		if hasKey {
			out.Source = "admin"
		} else {
			out.Source = "env"
		}
	}
	return out, nil
}

// SaveRoutingSettings writes the routing key in one transaction on the
// client's revision, the AI save's shape without the base URL.
func (s *Service) SaveRoutingSettings(ctx context.Context, apiKey string, pre Precondition) (*RoutingSettings, error) {
	var out *RoutingSettings
	err := s.inTx(ctx, func(ctx context.Context) error {
		rev, err := s.repo.LockRevision(ctx, ResourceRoutingSettings)
		if err != nil {
			return err
		}
		if err := pre.check(rev); err != nil {
			return err
		}
		if err := s.repo.WriteSetting(ctx, settingORSKey, apiKey); err != nil {
			return err
		}
		next := rev + 1
		if err := s.repo.BumpRevision(ctx, ResourceRoutingSettings, next); err != nil {
			return err
		}
		// Re-read for the response before the sinks, so the event stays the
		// transaction's LAST statement (the recipe's service order).
		out, err = s.GetRoutingSettings(ctx)
		if err != nil {
			return err
		}
		changes := map[string]any{"resource": ResourceRoutingSettings, "revision": next}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				Action:     "settings.saved",
				EntityType: "admin_settings",
				EntityID:   settingsEntityID(ResourceRoutingSettings),
				Changes:    changes,
			}); err != nil {
				return err
			}
		}
		return s.record(ctx, "admin_settings.saved", "admin_settings", settingsEntityID(ResourceRoutingSettings), changes, next)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteRoutingSettings removes the routing key override on the client's
// revision.
func (s *Service) DeleteRoutingSettings(ctx context.Context, pre Precondition) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		rev, err := s.repo.LockRevision(ctx, ResourceRoutingSettings)
		if err != nil {
			return err
		}
		if err := pre.check(rev); err != nil {
			return err
		}
		if err := s.repo.DeleteSetting(ctx, settingORSKey); err != nil {
			return err
		}
		next := rev + 1
		if err := s.repo.BumpRevision(ctx, ResourceRoutingSettings, next); err != nil {
			return err
		}
		changes := map[string]any{"resource": ResourceRoutingSettings, "revision": next}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				Action:     "settings.deleted",
				EntityType: "admin_settings",
				EntityID:   settingsEntityID(ResourceRoutingSettings),
				Changes:    changes,
			}); err != nil {
				return err
			}
		}
		return s.record(ctx, "admin_settings.deleted", "admin_settings", settingsEntityID(ResourceRoutingSettings), changes, next)
	})
}

// record writes one event into the outbox through the transaction's
// executor. Revision is 0 for entities without one.
func (s *Service) record(ctx context.Context, eventType, entityType string, entityID uuid.UUID, data map[string]any, revision int64) error {
	if s.events == nil {
		return nil
	}
	if revision > 0 {
		data["revision"] = revision
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: entityType, EntityID: entityID, Data: raw,
	})
}
