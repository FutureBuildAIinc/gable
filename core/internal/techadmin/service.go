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
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/ai"
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

	vault    ai.Sealer // seals the AI keys at rest; nil or absent means no key can be stored sealed
	aiTester AITester  // the test connection action
}

// AITester makes the cheapest call the active AI provider allows.
// *ai.Client satisfies it.
type AITester interface {
	TestConnection(ctx context.Context) error
}

// WithVault wires the vault that seals the AI keys at rest.
func (s *Service) WithVault(v ai.Sealer) *Service {
	s.vault = v
	return s
}

// WithAITester wires the client the test connection action calls.
func (s *Service) WithAITester(t AITester) *Service {
	s.aiTester = t
	return s
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

// KeyActive answers whether a key id is still valid: not revoked. The draft
// change feed rechecks its keyed streams at every heartbeat by id (one row
// read, no hash work), so a revoked key's stream closes within one
// heartbeat of the revocation (ADR 0007 section 3.3).
func (s *Service) KeyActive(ctx context.Context, id uuid.UUID) (bool, error) {
	k, err := s.repo.GetKey(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return k.Revoked == nil, nil
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

// GetAISettings reads the AI settings document: the active provider's key
// status (a stored row is the admin override; an OpenRouter key that comes
// only from the environment is env), the base URL when an override is
// stored, the Anthropic provider's status, and the resource revision. No key
// is ever returned, only masked hints.
func (s *Service) GetAISettings(ctx context.Context) (*AISettings, error) {
	key, hasKey, err := s.repo.ReadSetting(ctx, settingAIKey)
	if err != nil {
		return nil, err
	}
	baseURL, hasBase, err := s.repo.ReadSetting(ctx, settingAIBaseURL)
	if err != nil {
		return nil, err
	}
	provider, _, err := s.repo.ReadSetting(ctx, ai.SettingProvider)
	if err != nil {
		return nil, err
	}
	model, _, err := s.repo.ReadSetting(ctx, ai.SettingAnthropicModel)
	if err != nil {
		return nil, err
	}
	akey, hasAKey, err := s.repo.ReadSetting(ctx, ai.SettingAnthropicKey)
	if err != nil {
		return nil, err
	}
	rev, err := s.repo.ReadRevision(ctx, ResourceAISettings)
	if err != nil {
		return nil, err
	}
	if hasKey {
		// A sealed key the vault cannot open (the vault key changed) reads
		// as not stored: AI is off for it, as the client treats it.
		if opened, oerr := ai.OpenValue(s.vault, key); oerr == nil {
			key = opened
		} else {
			key, hasKey = s.envAIKey, false
		}
	} else {
		key = s.envAIKey
	}
	if provider != ai.ProviderAnthropic {
		provider = ai.ProviderOpenRouter
	}
	if model == "" {
		model = ai.DefaultAnthropicModel
	}
	out := &AISettings{
		Revision:     rev,
		Source:       "none",
		Provider:     provider,
		Model:        model,
		VaultPresent: s.vault != nil && s.vault.Present(),
	}
	if hasAKey && akey != "" {
		if opened, oerr := ai.OpenValue(s.vault, akey); oerr == nil && opened != "" {
			out.AnthropicConfigured = true
			out.AnthropicKeyHint = keyHint(opened)
		}
	}
	if key != "" {
		out.KeyHint = keyHint(key)
		if hasKey {
			out.Source = "admin"
		} else {
			out.Source = "env"
		}
	}
	out.Configured = key != ""
	if provider == ai.ProviderAnthropic {
		out.Configured = out.AnthropicConfigured
		if out.AnthropicConfigured {
			out.Source = "admin"
		} else {
			out.Source = "none"
		}
	}
	// Only an override is surfaced, so the UI can tell "override set" from
	// "using the default" and never re-persists the default back.
	if hasBase {
		out.BaseURL = &baseURL
	}
	if err := s.readAITest(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// aiTestRow is the stored last test of the saved key.
type aiTestRow struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	// Revision is the AI settings revision the test ran on. A result from an
	// older revision belongs to a key that may have changed since, so it
	// reads as untested.
	Revision int64 `json:"revision"`
}

// saveTestTimeout bounds the test a save runs, so a provider that does not
// answer cannot hold the save's response for the client's whole timeout.
const saveTestTimeout = 20 * time.Second

// readAITest fills the document's test status from the stored last test:
// "none" when no key is saved for the chosen provider, "untested" when one is
// saved and was never tested, else the stored result.
func (s *Service) readAITest(ctx context.Context, out *AISettings) error {
	out.TestStatus, out.TestMessage = AITestNone, ""
	if !out.Configured {
		return nil
	}
	out.TestStatus = AITestUntested
	raw, ok, err := s.repo.ReadSetting(ctx, settingAITest)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	var row aiTestRow
	if json.Unmarshal([]byte(raw), &row) != nil {
		return nil
	}
	if row.Revision != out.Revision {
		return nil
	}
	switch row.Status {
	case AITestPassed, AITestRefused, AITestFailed:
		out.TestStatus, out.TestMessage = row.Status, row.Message
	}
	return nil
}

// runAITest calls the provider once and stores the result for the settings
// document. A provider failure is an answer, not an error; only a storage
// failure is returned. The message is the provider's refusal in words, never
// the key.
func (s *Service) runAITest(ctx context.Context, fromSave bool) (TestResult, error) {
	// The revision the test runs on is read BEFORE the provider is asked: the
	// result belongs to that revision, whenever the provider answers.
	var rev int64
	if s.aiTester != nil && s.repo != nil {
		r, err := s.repo.ReadRevision(ctx, ResourceAISettings)
		if err != nil {
			return TestResult{}, err
		}
		rev = r
	}
	res := TestResult{OK: true, Status: AITestPassed, Message: "The provider answered."}
	switch {
	case s.aiTester == nil:
		res = TestResult{Status: AITestFailed, Message: "AI is off: the AI client is not available on this server"}
	default:
		if err := s.aiTester.TestConnection(ctx); err != nil {
			if words, refused := ai.RefusedMessage(err); refused {
				res = TestResult{Status: AITestRefused, Message: words}
			} else if unreachable := unreachableProvider(err); unreachable != "" {
				res = TestResult{Status: AITestFailed, Message: unreachableWords(unreachable, fromSave)}
			} else if errors.Is(err, context.DeadlineExceeded) {
				res = TestResult{Status: AITestFailed, Message: unreachableWords(s.aiProviderLabel(ctx), fromSave)}
			} else {
				res = TestResult{Status: AITestFailed, Message: err.Error()}
			}
		}
	}
	if s.aiTester == nil || s.repo == nil {
		return res, nil
	}
	stored := aiTestRow{Status: res.Status, Message: res.Message, Revision: rev}
	if res.Status == AITestPassed {
		stored.Message = ""
	}
	// A result of an older revision never replaces one of a later revision:
	// two tests that finish out of order leave the later key's result.
	if raw, ok, rerr := s.repo.ReadSetting(ctx, settingAITest); rerr == nil && ok {
		var prev aiTestRow
		if json.Unmarshal([]byte(raw), &prev) == nil && prev.Revision > rev {
			return res, nil
		}
	}
	row, err := json.Marshal(stored)
	if err != nil {
		return res, err
	}
	return res, s.repo.WriteSetting(ctx, settingAITest, string(row))
}

// SaveAISettings writes the OpenRouter key and, when the body names it, the
// base URL (the original save, kept for its callers).
func (s *Service) SaveAISettings(ctx context.Context, apiKey string, baseURL *string, pre Precondition) (*AISettings, error) {
	return s.SaveAISettingsFull(ctx, AISave{APIKey: apiKey, BaseURL: baseURL}, pre)
}

// SaveAISettingsFull writes the AI settings in one transaction on the
// client's revision: the anchor row is locked, the revision checked, the rows
// written (keys sealed with the vault), the anchor bumped, the audit row
// written and the event written last. The audit row and the event name who
// and which provider, never a key. An Anthropic key with no vault key is
// refused before anything is written (ErrNoVault).
func (s *Service) SaveAISettingsFull(ctx context.Context, in AISave, pre Precondition) (*AISettings, error) {
	if in.Model != "" {
		if err := ai.ValidateAnthropicModel(in.Model); err != nil {
			return nil, err
		}
	}
	if in.AnthropicAPIKey != "" && (s.vault == nil || !s.vault.Present()) {
		return nil, ai.ErrNoVault
	}
	var sealedOR, sealedAnthropic string
	var err error
	if in.APIKey != "" {
		sealedOR = in.APIKey
		if s.vault != nil && s.vault.Present() {
			if sealedOR, err = ai.SealValue(s.vault, in.APIKey); err != nil {
				return nil, err
			}
		}
	}
	if in.AnthropicAPIKey != "" {
		if sealedAnthropic, err = ai.SealValue(s.vault, in.AnthropicAPIKey); err != nil {
			return nil, err
		}
	}
	var out *AISettings
	err = s.inTx(ctx, func(ctx context.Context) error {
		rev, err := s.repo.LockRevision(ctx, ResourceAISettings)
		if err != nil {
			return err
		}
		if err := pre.check(rev); err != nil {
			return err
		}
		if sealedOR != "" {
			if err := s.repo.WriteSetting(ctx, settingAIKey, sealedOR); err != nil {
				return err
			}
		}
		if sealedAnthropic != "" {
			if err := s.repo.WriteSetting(ctx, ai.SettingAnthropicKey, sealedAnthropic); err != nil {
				return err
			}
		}
		if in.Provider != "" {
			if err := s.repo.WriteSetting(ctx, ai.SettingProvider, in.Provider); err != nil {
				return err
			}
		}
		if in.Model != "" {
			if err := s.repo.WriteSetting(ctx, ai.SettingAnthropicModel, in.Model); err != nil {
				return err
			}
		}
		if in.BaseURL != nil {
			if *in.BaseURL == "" {
				if err := s.repo.DeleteSetting(ctx, settingAIBaseURL); err != nil {
					return err
				}
			} else if err := s.repo.WriteSetting(ctx, settingAIBaseURL, *in.BaseURL); err != nil {
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
		changes := map[string]any{"resource": ResourceAISettings, "revision": next, "provider": out.Provider}
		if in.BaseURL != nil {
			changes["base_url_cleared"] = *in.BaseURL == ""
		}
		if in.Model != "" {
			changes["model"] = in.Model
		}
		if in.AnthropicAPIKey != "" {
			changes["anthropic_key_changed"] = true
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
	s.invalidateAI()
	// Saving a key tests it with the provider (N7), so the document the
	// admin reads says whether the key works, not only that one is saved. The
	// test is the provider's answer, never a reason to refuse the save.
	if out.Configured && s.aiTester != nil {
		tctx, cancel := context.WithTimeout(ctx, saveTestTimeout)
		_, terr := s.runAITest(tctx, true)
		cancel()
		if terr != nil {
			// The save is committed: a result that could not be stored is
			// logged, never answered as a failed save.
			slog.Error("failed to store the AI test result after a save", "error", terr)
		}
		if fresh, gerr := s.GetAISettings(ctx); gerr == nil {
			return fresh, nil
		}
	}
	return out, nil
}

// TestAIConnection runs the test connection action and stores its result as
// the last test of the saved key. A provider failure is an answer (ok false
// and its words), not an error.
func (s *Service) TestAIConnection(ctx context.Context) TestResult {
	res, err := s.runAITest(ctx, false)
	if err != nil {
		slog.Error("failed to store the AI test result", "error", err)
	}
	return res
}

// SealLegacySettings seals any AI key row still stored in plain text, the
// read time upgrade run at startup. A row that is already sealed, or any row
// when no vault key exists, is left alone. It returns how many it sealed and
// never loses a key: a value is replaced only by its own sealed form. The
// market index source's feed URL and feed key get the same pass: both are
// secrets and both are sealed the way the AI keys are.
func (s *Service) SealLegacySettings(ctx context.Context) (int, error) {
	if s.vault == nil || !s.vault.Present() {
		return 0, nil
	}
	n := 0
	for _, key := range []string{settingAIKey, ai.SettingAnthropicKey} {
		v, ok, err := s.repo.ReadSetting(ctx, key)
		if err != nil {
			return n, err
		}
		if !ok || v == "" || ai.IsSealedValue(v) {
			continue
		}
		sealed, err := ai.SealValue(s.vault, v)
		if err != nil {
			return n, err
		}
		if err := s.repo.WriteSetting(ctx, key, sealed); err != nil {
			return n, err
		}
		n++
	}
	row, err := s.repo.ReadMarketIndexSource(ctx)
	if err != nil {
		return n, err
	}
	changed := false
	if row.FeedURL != "" && !ai.IsSealedValue(row.FeedURL) {
		sealed, err := ai.SealValue(s.vault, row.FeedURL)
		if err != nil {
			return n, err
		}
		row.FeedURL = sealed
		changed = true
		n++
	}
	if row.FeedKey != "" && !ai.IsSealedValue(row.FeedKey) {
		sealed, err := ai.SealValue(s.vault, row.FeedKey)
		if err != nil {
			return n, err
		}
		row.FeedKey = sealed
		changed = true
		n++
	}
	if changed {
		if err := s.repo.UpdateMarketIndexSource(ctx, row, row.Revision); err != nil {
			return n, err
		}
	}
	return n, nil
}

// DeleteAISettings removes the admin override (key and base URL together, so
// both revert to their environment defaults) on the client's revision.
func (s *Service) DeleteAISettings(ctx context.Context, pre Precondition) error {
	err := s.inTx(ctx, func(ctx context.Context) error {
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
		for _, k := range []string{ai.SettingAnthropicKey, ai.SettingProvider, ai.SettingAnthropicModel, settingAITest} {
			if err := s.repo.DeleteSetting(ctx, k); err != nil {
				return err
			}
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
	if err == nil {
		s.invalidateAI()
	}
	return err
}

// unreachableProvider is the provider's name when err is an unreachable
// provider, else "".
func unreachableProvider(err error) string {
	var ue *ai.UnreachableError
	if errors.As(err, &ue) {
		return ue.ProviderName()
	}
	return ""
}

// unreachableWords is the sentence for a test that could not reach provider: a
// save says the key is saved, a pressed test does not.
func unreachableWords(provider string, fromSave bool) string {
	if fromSave {
		return ai.UnreachableWords(provider)
	}
	return ai.PressedUnreachableWords(provider)
}

// aiProviderLabel is the display name of the chosen provider.
func (s *Service) aiProviderLabel(ctx context.Context) string {
	if p, _, err := s.repo.ReadSetting(ctx, ai.SettingProvider); err == nil && p == ai.ProviderAnthropic {
		return "Anthropic"
	}
	return "OpenRouter"
}

// invalidateAI drops the AI client's cached settings after a committed save
// or delete, so the very next call (the test connection included) uses what
// was saved. The tester is the AI client in serve; a tester with no cache has
// nothing to drop.
func (s *Service) invalidateAI() {
	if r, ok := s.aiTester.(interface{ Invalidate() }); ok {
		r.Invalidate()
	}
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

// --- the market index source settings ---

// marketIndexSourceDocument reads the settings row into the wire document.
// The feed URL and the feed key are secrets and never leave this function:
// the document says only whether one is stored, and the header the key rides
// in, which is configuration and not a credential. A sealed value the vault
// cannot open (the vault key changed) reads as not stored.
func (s *Service) marketIndexSourceDocument(row *MarketIndexSourceRow) *MarketIndexSourceSettings {
	out := &MarketIndexSourceSettings{
		Source:        row.Source,
		FeedURLSet:    row.FeedURL != "",
		FeedKeyHeader: row.FeedKeyHeader,
		StalenessDays: row.StalenessDays,
		LastPullAt:    httpx.PtrTimestamp(row.LastPullAt),
		LastPullOK:    row.LastPullOK,
		LastError:     row.LastError,
		Revision:      row.Revision,
	}
	if row.FeedKey != "" {
		if opened, err := ai.OpenValue(s.vault, row.FeedKey); err == nil && opened != "" {
			out.FeedKeySet = true
		}
	}
	return out
}

// GetMarketIndexSourceSettings reads the market index source document. The
// feed URL and the feed key are never returned; the document says only
// whether one is stored.
func (s *Service) GetMarketIndexSourceSettings(ctx context.Context) (*MarketIndexSourceSettings, error) {
	row, err := s.repo.ReadMarketIndexSource(ctx)
	if err != nil {
		return nil, err
	}
	return s.marketIndexSourceDocument(row), nil
}

// SaveMarketIndexSourceSettings writes the market index source in one
// transaction on the client's revision: the settings row is locked, the
// revision checked, the row written (the feed URL and the feed key sealed
// with the vault when one is present), the revision moved, the audit row
// written and the event written last. The audit row and the event never
// carry the URL or the key; they record only that they changed.
func (s *Service) SaveMarketIndexSourceSettings(ctx context.Context, in MarketIndexSourceSave, pre Precondition) (*MarketIndexSourceSettings, error) {
	var out *MarketIndexSourceSettings
	err := s.inTx(ctx, func(ctx context.Context) error {
		row, err := s.repo.LockMarketIndexSource(ctx)
		if err != nil {
			return err
		}
		if err := pre.check(row.Revision); err != nil {
			return err
		}
		next := row
		if in.Source != nil {
			next.Source = *in.Source
		}
		if in.FeedKeyHeader != nil {
			next.FeedKeyHeader = *in.FeedKeyHeader
		}
		if in.FeedURL != nil {
			// The URL is itself a secret: it carries the feed's identity in
			// its path. It is stored the way the AI keys are, sealed when a
			// vault is present, so the value at rest is never the address.
			next.FeedURL = *in.FeedURL
			if *in.FeedURL != "" && s.vault != nil && s.vault.Present() {
				sealed, serr := ai.SealValue(s.vault, *in.FeedURL)
				if serr != nil {
					return serr
				}
				next.FeedURL = sealed
			}
		}
		if in.FeedKey != nil {
			// The way the AI settings store their key: sealed when a vault is
			// present, so the value at rest is never the key itself.
			next.FeedKey = *in.FeedKey
			if *in.FeedKey != "" && s.vault != nil && s.vault.Present() {
				sealed, serr := ai.SealValue(s.vault, *in.FeedKey)
				if serr != nil {
					return serr
				}
				next.FeedKey = sealed
			}
		}
		if in.StalenessDays != nil {
			next.StalenessDays = *in.StalenessDays
		}
		rev := row.Revision + 1
		if err := s.repo.UpdateMarketIndexSource(ctx, next, rev); err != nil {
			return err
		}
		fresh, err := s.repo.ReadMarketIndexSource(ctx)
		if err != nil {
			return err
		}
		out = s.marketIndexSourceDocument(fresh)
		changes := map[string]any{
			"resource": ResourceMarketIndexSourceSettings,
			"revision": rev,
		}
		if in.Source != nil {
			changes["source"] = *in.Source
		}
		if in.FeedKeyHeader != nil {
			changes["feed_key_header"] = *in.FeedKeyHeader
		}
		if in.StalenessDays != nil {
			changes["staleness_days"] = *in.StalenessDays
		}
		if in.FeedURL != nil {
			// Only that it changed, never the address.
			changes["feed_url_changed"] = true
		}
		if in.FeedKey != nil {
			// Only that it changed, never the key.
			changes["feed_key_changed"] = true
		}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				Action:     "settings.saved",
				EntityType: "admin_settings",
				EntityID:   settingsEntityID(ResourceMarketIndexSourceSettings),
				Changes:    changes,
			}); err != nil {
				return err
			}
		}
		return s.record(ctx, "admin_settings.saved", "admin_settings", settingsEntityID(ResourceMarketIndexSourceSettings), changes, rev)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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
