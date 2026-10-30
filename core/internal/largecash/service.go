// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// The dealer's settings keys. The default country is the single country
// dealer's path: a branch with no country of its own falls back to it, and a
// branch with neither cannot be saved. The Canadian threshold is a dealer
// policy, off by default, never a legal duty.
const (
	KeyCountryDefault       = "country.default"
	KeyCAEnabled            = "largecash.ca.enabled"
	KeyCAThresholdCents     = "largecash.ca.threshold_cents"
	caDefaultThresholdCents = 1_000_000
)

// CanadaNote is the plain sentence the Canadian surfaces carry: the capture
// is for the dealer's own records, and a building materials dealer is not a
// FINTRAC reporting entity, so nothing here is a FINTRAC report.
const CanadaNote = "This is for the dealer's own records. It is not a FINTRAC report."

// The branch's walk-in identity floor (CP-17a, item 296): the amount over
// which the counter asks a walk-in who paid cash to identify themselves, so
// the walk-in's cash sales are counted together. The floor is the branch's
// own setting; a branch with no setting takes its country's default, 3,000
// dollars on a United States branch and off on a Canadian one.
const (
	// USDefaultWalkInFloorCents is the United States default floor, 3,000
	// dollars in cents.
	USDefaultWalkInFloorCents int64 = 300_000
	// WalkInFloorOffCents is the floor that is off: the branch asks no
	// walk-in's identity and their cash is not aggregated.
	WalkInFloorOffCents int64 = 0
)

// WalkInFloorNote is the plain sentence the counter shows when a branch's
// floor is off: the walk-in's cash is not aggregated.
const WalkInFloorNote = "Walk-in cash is not aggregated at this branch: each walk-in's cash is counted on its own."

// StateIdentityDeclined is the worklist's state for the walk-in who declined
// to identify over the branch's floor: the sale went ahead and a manager
// reviews it.
const StateIdentityDeclined = "identity declined"

// The crossing's own states (migration 139 and 148). Reportable is the
// United States duty, recorded is the Canadian dealer policy, and identity
// declined is the walk-in who would not say who they were.
const (
	EventStateReportable      = "reportable"
	EventStateRecorded        = "recorded"
	EventStateIdentityDeclined = StateIdentityDeclined
)

// ResolveWalkInFloor answers the floor a branch runs: the branch's own
// amount, else the branch country's default (3,000 dollars on a United
// States branch, off on a Canadian one, and off in any country with no rule
// family of its own).
func ResolveWalkInFloor(country string, own *int64) int64 {
	if country != "US" && country != "CA" {
		// no rule family of its own: there is no large cash rule to ask about
		return WalkInFloorOffCents
	}
	if own != nil {
		return *own
	}
	if country == "US" {
		return USDefaultWalkInFloorCents
	}
	return WalkInFloorOffCents
}

// PayerIn is the payer identity the counter captured, as the completion
// request carries it. The taxpayer identification number and the ID number
// are sealed before storage and never logged, never printed on a receipt
// and never in an audit row's body.
type PayerIn struct {
	Name            string `json:"name"`
	Address         string `json:"address"`
	Occupation      string `json:"occupation"`
	DateOfBirth     string `json:"date_of_birth"`
	IDType          string `json:"id_type"`
	IDNumber        string `json:"id_number"`
	TaxpayerID      string `json:"taxpayer_id"`
	ThirdPartyPayer bool   `json:"third_party_payer"`
}

// LineFact is one sale line as the classifier needs it: the product (for
// the consumer durable flag) and the line's total in cents (for the priced
// over the threshold test, which is at the sale line, not the product).
type LineFact struct {
	ProductID      uuid.UUID
	LineTotalCents int64
}

// CompletionFacts is one sale as the large cash rules see it.
type CompletionFacts struct {
	BranchID           uuid.UUID
	TransactionID      uuid.UUID
	CustomerID         *uuid.UUID
	Currency           string
	Lines              []LineFact
	Tenders            []TenderFacts
	SuspectedAvoidance bool
	Payer              *PayerIn
	// PayerDeclined is the counter's record that the walk-in declined to
	// identify over the branch's floor. It answers the floor's ask only, never
	// a crossing: there the sale waits for the payer's details. Over the floor
	// the sale goes ahead and the fact is recorded for the manager's worklist.
	PayerDeclined bool
	// Actor is who completes the sale, for the crossing's audit row.
	Actor string
}

// Preview is the counter's answer before Pay: what the rule family is,
// what this sale's counted cash is, what the aggregate comes to, whether
// the payer's details are required, the fields the rule needs and the
// Canadian note when it applies.
type Preview struct {
	Country         string   `json:"country"`
	RuleFamily      string   `json:"rule_family"`
	CountedCents    int64    `json:"counted_cents"`
	AggregateCents  int64    `json:"aggregate_cents"`
	ThresholdCents  int64    `json:"threshold_cents"`
	Comparator      string   `json:"comparator"`
	AsksDetails     bool     `json:"asks_details"`
	Fields          []string `json:"fields"`
	Note            string   `json:"note,omitempty"`
	DealerThreshold bool     `json:"dealer_threshold"`
	// WalkInFloorCents is the branch's floor for asking a walk-in's identity
	// on a cash sale over it, zero when the branch has it off.
	WalkInFloorCents int64 `json:"walk_in_floor_cents"`
	// AsksFloor is true when this walk-in's cash sale passes the floor and no
	// identity is captured yet: the counter asks before Pay.
	AsksFloor bool `json:"asks_floor"`
	// WalkInAggregated is true when the branch counts a walk-in's cash sales
	// together once the walk-in is identified. With the floor off it is
	// false and the counter says so.
	WalkInAggregated bool `json:"walk_in_aggregated"`
	// WalkInNote is the plain sentence the counter shows when the branch's
	// floor is off: the walk-in's cash is not aggregated.
	WalkInNote string `json:"walk_in_note,omitempty"`
	// ThresholdCrossed is true when the sale's counted cash crosses the rule's
	// threshold: the payer's details are required and no decline answers it.
	ThresholdCrossed bool `json:"threshold_crossed"`
}

// USFields and CAFields are the payer fields each rule needs, in the order
// the counter asks them.
var (
	USFields = []string{"name", "address", "taxpayer_id", "date_of_birth", "occupation", "id_type", "id_number", "third_party_payer"}
	CAFields = []string{"name", "address", "date_of_birth", "occupation", "id_type"}
)

// ErrPayerDetailsRequired is the 409 the completion answers when a sale's
// counted cash would take the payer over the threshold and the payer's
// details are missing.
var ErrPayerDetailsRequired = &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
	Message: "this payment takes the payer over the large cash threshold: capture the payer's details before completing",
	Details: []httpx.FieldError{httpx.Blocker("payer_details_required",
		"this payment takes the payer over the large cash threshold: capture the payer's details before completing")}}

// ErrWalkInIdentityRequired is the 409 the completion answers when a
// walk-in's cash sale passes the branch's identity floor and the counter has
// neither captured the payer's details nor recorded that the walk-in declined
// to identify. The sale waits for the answer.
var ErrWalkInIdentityRequired = &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
	Message: "this walk-in's cash sale is over the branch's identity floor: capture the payer's details or record that the walk-in declined to identify",
	Details: []httpx.FieldError{httpx.Blocker("walk_in_identity_required",
		"this walk-in's cash sale is over the branch's identity floor: capture the payer's details or record that the walk-in declined to identify")}}

// ErrVaultUnavailable is the 503 the preview and the completion answer when
// a sale carries the payer's identity and the server has no valid vault key
// (PAYMENT_VAULT_KEY missing or malformed): the taxpayer identification
// number and the ID number are never stored in clear, and the payer's key is
// never an unkeyed digest, so the capture fails closed. Operator: the counter
// shows the message and the blocker, so the clerk can say why.
var ErrVaultUnavailable = &httpx.Error{Status: http.StatusServiceUnavailable, Code: httpx.CodeUnavailable, Operator: true,
	Message: "the payer's details cannot be recorded: this server has no vault key to seal them; set PAYMENT_VAULT_KEY and restart",
	Details: []httpx.FieldError{httpx.Blocker("large_cash_vault_unavailable",
		"the payer's details cannot be recorded: this server has no vault key to seal them; set PAYMENT_VAULT_KEY and restart")}}

// Service is the large cash capture, classification and aggregation.
type Service struct {
	db    *database.DB
	repo  *Repository
	vault *payment.Vault
	// digestKey is the vault key's 32 bytes, the HMAC key of the payer's
	// digests. Nil when the server has no valid vault key: then the service
	// refuses any capture that carries the payer's identity.
	digestKey []byte
	now       func() time.Time
}

// NewService builds the large cash service. vault may be nil, and keyHex
// empty or malformed: the service then answers ErrVaultUnavailable to any
// sale that carries the payer's identity, never storing it in clear.
func NewService(db *database.DB, vault *payment.Vault, keyHex string) *Service {
	if vault == nil {
		vault, _ = payment.NewVault("")
	}
	var digestKey []byte
	if key, err := hex.DecodeString(strings.TrimSpace(keyHex)); err == nil && len(key) == 32 {
		digestKey = key
	}
	return &Service{db: db, repo: NewRepository(db), vault: vault, digestKey: digestKey, now: time.Now}
}

// sealing answers whether the service can seal the payer's sensitive values
// and key their digests: a present vault and a valid digest key.
func (s *Service) sealing() bool { return s.vault.Present() && s.digestKey != nil }

// settingsEntityID is the audit entity of the dealer's large cash settings,
// which have no row of their own.
var settingsEntityID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("largecash.settings"))

// Settings is the dealer's large cash configuration on the wire.
type Settings struct {
	CADefaultThresholdCents int64  `json:"ca_default_threshold_cents"`
	CAEnabled               bool   `json:"ca_enabled"`
	CAThresholdCents        int64  `json:"ca_threshold_cents"`
	CountryDefault          string `json:"country_default"`
}

// ReadSettings answers the dealer's configuration.
func (s *Service) ReadSettings(ctx context.Context) (*Settings, error) {
	country, err := s.repo.Setting(ctx, KeyCountryDefault)
	if err != nil {
		return nil, err
	}
	enabled, err := s.repo.Setting(ctx, KeyCAEnabled)
	if err != nil {
		return nil, err
	}
	threshold := int64(caDefaultThresholdCents)
	if v, err := s.repo.Setting(ctx, KeyCAThresholdCents); err != nil {
		return nil, err
	} else if v != "" {
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err != nil || parsed <= 0 {
			threshold = int64(caDefaultThresholdCents)
		} else {
			threshold = int64(parsed)
		}
	}
	return &Settings{CountryDefault: country, CAEnabled: enabled == "on",
		CAThresholdCents: threshold, CADefaultThresholdCents: int64(caDefaultThresholdCents)}, nil
}

// SaveSettings writes the dealer's configuration: the default country and
// the Canadian threshold (on or off, amount). One transaction, so a reader
// never sees the switch without its amount.
func (s *Service) SaveSettings(ctx context.Context, in *Settings, actor string) error {
	if in.CountryDefault != "" && len(in.CountryDefault) != 2 {
		return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed,
			Message: "one or more fields failed validation",
			Details: []httpx.FieldError{{Field: "country_default", Message: "must be two letters, for example US or CA"}}}
	}
	if in.CAThresholdCents <= 0 {
		return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed,
			Message: "one or more fields failed validation",
			Details: []httpx.FieldError{{Field: "ca_threshold_cents", Message: "must be greater than zero"}}}
	}
	enabled := "off"
	if in.CAEnabled {
		enabled = "on"
	}
	return s.db.RunInTx(ctx, func(ctx context.Context) error {
		before, err := s.ReadSettings(ctx)
		if err != nil {
			return err
		}
		for _, kv := range [][2]string{
			{KeyCountryDefault, in.CountryDefault},
			{KeyCAEnabled, enabled},
			{KeyCAThresholdCents, fmt.Sprintf("%d", in.CAThresholdCents)},
		} {
			if kv[1] == "" {
				continue
			}
			if err := s.repo.PutSetting(ctx, kv[0], kv[1]); err != nil {
				return err
			}
		}
		// What the counter records follows these figures, so a change to
		// them is audited with the old and the new, in the same transaction.
		return audit.NewLogger(s.db).Log(ctx, audit.Entry{
			Action: "large_cash.settings.changed", EntityType: "system_settings", EntityID: settingsEntityID, UserID: actor,
			Changes: map[string]interface{}{
				"before": map[string]interface{}{"country_default": before.CountryDefault, "ca_enabled": before.CAEnabled, "ca_threshold_cents": before.CAThresholdCents},
				"after":  map[string]interface{}{"country_default": in.CountryDefault, "ca_enabled": in.CAEnabled, "ca_threshold_cents": in.CAThresholdCents},
			}})
	})
}

// Country answers the country whose rule a branch runs: the branch's own,
// else the dealer default.
func (s *Service) Country(ctx context.Context, branchID uuid.UUID) (string, error) {
	return s.repo.BranchCountry(ctx, branchID)
}

// aggregate is the counted cash picture for one payer under one rule.
type aggregate struct {
	rule       string
	threshold  int64
	comparator string
	base       int64
	// txIDs and tenderCount describe the tenders already inside the running
	// window, for the crossing row.
	txIDs       []uuid.UUID
	tenderCount int
}

// computed is one sale as the large cash rules see it: the branch's country,
// the classifier's verdicts, the counted cash, the rule family's running
// total for this payer and the branch's walk-in identity floor.
type computed struct {
	country   string
	agg       *aggregate
	decisions []Decision
	counted   int64
	// floor is the branch's walk-in identity floor, zero when off.
	floor int64
	// overFloor is true when this sale is a walk-in's cash sale that passes
	// the floor: the counter asks who paid before Pay.
	overFloor bool
	// cashTendered is true when the sale takes cash: the floor's words are
	// about cash, so they are said only when there is cash on the counter.
	cashTendered bool
}

// compute classifies the sale's tenders under the branch's rule and reads
// the payer's running aggregate. It takes the payer's advisory lock first,
// so two registers crossing the threshold for one payer at once serialize
// and the second sees the first's tenders.
func (s *Service) compute(ctx context.Context, f *CompletionFacts) (*computed, error) {
	country, err := s.repo.BranchCountry(ctx, f.BranchID)
	if err != nil {
		return nil, err
	}
	out := &computed{country: country}
	sale := SaleFacts{Country: country, SuspectedAvoidance: f.SuspectedAvoidance}
	if country == "US" {
		// the consumer durable test is at the sale line: a product flagged
		// consumer durable whose line is priced over the threshold
		durable, err := s.repo.AnyConsumerDurableLine(ctx, f.Lines, ThresholdCents)
		if err != nil {
			return nil, err
		}
		sale.ConsumerDurableOverThreshold = durable
	}
	for i := range f.Tenders {
		d := Classify(sale, f.Tenders[i])
		out.decisions = append(out.decisions, d)
		if d.Counts {
			out.counted += d.CountedCents
		}
		if f.Tenders[i].Method == "cash" {
			out.cashTendered = true
		}
	}
	// the branch's walk-in identity floor: the branch's own amount, else the
	// branch country's default. Only a walk-in's cash sale is asked about:
	// a named customer is already the payer's key.
	set, err := s.repo.WalkInFloor(ctx, f.BranchID)
	if err != nil {
		return nil, err
	}
	out.floor = ResolveWalkInFloor(country, set)
	if out.counted == 0 {
		return out, nil
	}
	out.overFloor = out.floor > 0 && f.CustomerID == nil && out.counted > out.floor
	if f.Payer != nil && !s.sealing() {
		// the payer's identity would be keyed and stored without the vault:
		// refuse before any digest is taken or any row is written
		return nil, ErrVaultUnavailable
	}
	payerKey := s.payerKey(f)
	if err := s.repo.AdvisoryLock(ctx, payerKey); err != nil {
		return nil, err
	}
	switch country {
	case "US":
		base, ids, n, err := s.repo.USSinceLastEvent(ctx, payerKey, s.now().AddDate(-1, 0, 0))
		if err != nil {
			return nil, err
		}
		out.agg = &aggregate{rule: RuleUS, threshold: ThresholdCents, comparator: "greater",
			base: base, txIDs: ids, tenderCount: n}
	case "CA":
		st, err := s.ReadSettings(ctx)
		if err != nil {
			return nil, err
		}
		if !st.CAEnabled && out.floor == WalkInFloorOffCents {
			// the dealer threshold is off and the branch asks no walk-in's
			// identity: nothing is counted or recorded
			out.counted = 0
			out.decisions = nil
			return out, nil
		}
		start, end, err := s.repo.BranchDayWindow(ctx, f.BranchID, s.now())
		if err != nil {
			return nil, err
		}
		base, ids, n, err := s.repo.CAInWindow(ctx, payerKey, start, end)
		if err != nil {
			return nil, err
		}
		out.agg = &aggregate{rule: RuleCA, threshold: st.CAThresholdCents, comparator: "at_or_above",
			base: base, txIDs: ids, tenderCount: n}
		if !st.CAEnabled {
			// the dealer threshold is off: the branch's own floor is the only
			// question this sale raises, and no crossing is recorded.
			out.agg.threshold = WalkInFloorOffCents
		}
	}
	return out, nil
}

// crossed answers whether this sale's counted cash crosses the rule's
// threshold: the United States duty and the Canadian dealer policy.
func (c *computed) crossed() bool {
	if c.agg == nil || c.agg.threshold <= 0 {
		return false
	}
	return Crosses(c.agg.rule, c.agg.base+c.counted, c.agg.threshold)
}

// Preview answers what the counter asks before Pay. It writes nothing.
func (s *Service) Preview(ctx context.Context, f *CompletionFacts) (*Preview, error) {
	c, err := s.compute(ctx, f)
	if err != nil {
		return nil, err
	}
	p := &Preview{Country: c.country, CountedCents: c.counted, ThresholdCents: ThresholdCents,
		Comparator: "greater", Fields: []string{}}
	p.WalkInFloorCents = c.floor
	p.WalkInAggregated = c.floor > WalkInFloorOffCents
	if !p.WalkInAggregated && c.cashTendered {
		// the counter takes cash: it says on the screen that the walk-in's
		// cash is not aggregated at this branch
		p.WalkInNote = WalkInFloorNote
	}
	switch {
	case c.agg != nil && c.agg.rule == RuleUS:
		p.RuleFamily = RuleUS
		p.AggregateCents = c.agg.base + c.counted
		p.ThresholdCents = c.agg.threshold
		p.AsksDetails = Crosses(RuleUS, p.AggregateCents, c.agg.threshold)
		p.ThresholdCrossed = p.AsksDetails
	case c.agg != nil && c.agg.rule == RuleCA:
		p.RuleFamily = RuleCA
		p.AggregateCents = c.agg.base + c.counted
		p.ThresholdCents = c.agg.threshold
		p.Comparator = "at_or_above"
		p.DealerThreshold = c.agg.threshold > WalkInFloorOffCents
		p.Note = CanadaNote
		if p.DealerThreshold {
			p.AsksDetails = Crosses(RuleCA, p.AggregateCents, c.agg.threshold)
			p.ThresholdCrossed = p.AsksDetails
		}
	}
	// The floor asks the counter for the walk-in's identity so their cash
	// sales are counted together. A capture that already holds the identity
	// has answered it.
	p.AsksFloor = c.overFloor && f.Payer == nil
	if p.AsksFloor {
		p.AsksDetails = true
	}
	if p.AsksDetails {
		if p.RuleFamily == RuleCA {
			p.Fields = CAFields
		} else {
			p.Fields = USFields
		}
	}
	return p, nil
}

// Capture runs inside the completion's transaction: it records the tenders
// that counted (each with the classifier's reason), and on a threshold
// crossing it records the captured payer and writes the large_cash_events
// row. A crossing without the payer's details answers 409
// payer_details_required and the whole completion refuses, and a recorded
// decline does not answer it: the crossing stands until the payer is given
// (item 299). A walk-in's cash sale over the branch's identity floor is
// asked about before Pay: without the payer's details and without a recorded
// decline it answers 409 walk_in_identity_required, and a recorded decline
// lets the sale go ahead and lands in the worklist as an identity declined.
func (s *Service) Capture(ctx context.Context, f *CompletionFacts) error {
	c, err := s.compute(ctx, f)
	if err != nil {
		return err
	}
	if c.country != "US" && c.country != "CA" {
		return nil
	}
	if c.counted == 0 {
		return nil
	}
	for i, d := range c.decisions {
		if !d.Counts {
			continue
		}
		if err := s.repo.InsertTender(ctx, f.TransactionID, f.BranchID, d.RuleFamily,
			s.payerKey(f), d.CountedCents, d.Instrument, d.Reason, i); err != nil {
			return err
		}
	}
	if c.crossed() {
		// the crossing: the payer's details are the condition of the completion,
		// and a decline does not answer them (item 299): the refusal stands
		// until the payer is given
		if f.Payer == nil {
			return ErrPayerDetailsRequired
		}
		if err := s.checkPayer(c.country, f.Payer); err != nil {
			return err
		}
		if err := s.storePayer(ctx, f, c.country); err != nil {
			return err
		}
		txIDs := append(c.agg.txIDs, f.TransactionID)
		seen := map[uuid.UUID]bool{}
		uniq := make([]uuid.UUID, 0, len(txIDs))
		for _, id := range txIDs {
			if !seen[id] {
				seen[id] = true
				uniq = append(uniq, id)
			}
		}
		state := EventStateReportable
		if c.agg.rule == RuleCA {
			state = EventStateRecorded
		}
		if err := s.repo.InsertEvent(ctx, s.payerKey(f), f.CustomerID, f.BranchID, c.agg.rule, f.Currency,
			c.counted, c.agg.base+c.counted, uniq, c.agg.tenderCount+1, state); err != nil {
			return err
		}
		// The crossing is audited in the completion's transaction. The row names
		// the rule and the figures and that a payer was captured, never an
		// identity value (ADR 0003).
		return audit.NewLogger(s.db).Log(ctx, audit.Entry{
			Action: "large_cash.crossing.recorded", EntityType: "pos_transaction", EntityID: f.TransactionID, UserID: f.Actor,
			Changes: map[string]interface{}{
				"rule_family": c.agg.rule, "state": state, "branch_id": f.BranchID.String(),
				"counted_cents": c.counted, "total_cents": c.agg.base + c.counted, "payer_captured": true,
			}})
	}
	if c.overFloor && f.Payer == nil {
		// the branch's floor, a sale that does not cross: the counter asked
		// who paid and has no answer yet. The decline answers this ask only,
		// never the crossing above.
		if !f.PayerDeclined {
			return ErrWalkInIdentityRequired
		}
		return s.recordDeclined(ctx, f, c)
	}
	return nil
}

// recordDeclined records the walk-in who declined to identify over the
// branch's floor: the sale goes ahead and the row lands in the worklist for a
// manager to review. The state says identity declined and the figures are
// the sale's, so a manager sees what the sale was.
func (s *Service) recordDeclined(ctx context.Context, f *CompletionFacts, c *computed) error {
	rule := RuleUS
	if c.country == "CA" {
		rule = RuleCA
	}
	counted := 0
	for _, d := range c.decisions {
		if d.Counts {
			counted++
		}
	}
	if err := s.repo.InsertEvent(ctx, s.payerKey(f), f.CustomerID, f.BranchID, rule, f.Currency,
		c.counted, c.counted, []uuid.UUID{f.TransactionID}, counted, StateIdentityDeclined); err != nil {
		return err
	}
	return audit.NewLogger(s.db).Log(ctx, audit.Entry{
		Action: "large_cash.identity_declined.recorded", EntityType: "pos_transaction", EntityID: f.TransactionID, UserID: f.Actor,
		Changes: map[string]interface{}{
			"rule_family": rule, "state": StateIdentityDeclined, "branch_id": f.BranchID.String(),
			"counted_cents": c.counted, "total_cents": c.counted, "payer_captured": false,
		}})
}

// payerKey is the aggregation key: the named customer, else the captured
// identity's digest (the walk-in's key, the same person across their sales),
// else nothing aggregates.
func (s *Service) payerKey(f *CompletionFacts) string {
	if f.CustomerID != nil {
		return "cus:" + f.CustomerID.String()
	}
	if f.Payer != nil {
		if digest := s.tinDigest(f.Payer.TaxpayerID); digest != "" {
			return "tin:" + digest
		}
		if digest := s.identityDigest(f.Payer); digest != "" {
			return "id:" + digest
		}
	}
	return "walk:" + f.TransactionID.String()
}

// identityDigest is the HMAC-SHA256 of the captured identity's strongest
// stable value: the ID number when the capture carries one, else the name and
// the date of birth. It keys a walk-in's sales together without the identity
// ever being stored in cleartext, and it is the key the preview and the
// completion both use, so the two agree on the person.
func (s *Service) identityDigest(p *PayerIn) string {
	value := strings.TrimSpace(p.IDNumber)
	if value == "" {
		name := strings.ToLower(strings.TrimSpace(p.Name))
		dob := strings.TrimSpace(p.DateOfBirth)
		if name == "" || dob == "" {
			return ""
		}
		value = name + "|" + dob
	}
	return s.digest("id", strings.ToUpper(strings.Join(strings.Fields(value), "")))
}

// tinDigest is the HMAC-SHA256 of a normalized taxpayer identification
// number, keyed with the vault key so the digest cannot be brute forced
// off the row.
func (s *Service) tinDigest(tin string) string {
	normalized := strings.ToUpper(strings.Join(strings.Fields(tin), ""))
	if normalized == "" {
		return ""
	}
	return s.digest("tin", normalized)
}

// digest is the keyed digest of a normalized value: the HMAC-SHA256 under the
// vault key. Without the key it is empty, never an unkeyed digest a nine
// digit number space reverses; compute refuses such a capture first.
func (s *Service) digest(label, normalized string) string {
	if s.digestKey == nil {
		return ""
	}
	m := hmac.New(sha256.New, s.digestKey)
	m.Write([]byte(label + ":" + normalized))
	return hex.EncodeToString(m.Sum(nil))
}

// checkPayer refuses a crossing whose payer details are missing, in the
// words a clerk reads.
func (s *Service) checkPayer(country string, p *PayerIn) error {
	if p == nil {
		return ErrPayerDetailsRequired
	}
	missing := func(label string) error {
		return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
			Message: "the payer's details are incomplete: the large cash record needs them",
			Details: []httpx.FieldError{httpx.Blocker("payer_details_required", label)}}
	}
	if strings.TrimSpace(p.Name) == "" {
		return missing("the payer's name")
	}
	if strings.TrimSpace(p.Address) == "" {
		return missing("the payer's address")
	}
	if strings.TrimSpace(p.Occupation) == "" {
		return missing("the payer's occupation")
	}
	if p.DateOfBirth == "" {
		return missing("the payer's date of birth")
	}
	if _, err := time.Parse("2006-01-02", p.DateOfBirth); err != nil {
		return missing("the payer's date of birth as a date")
	}
	if strings.TrimSpace(p.IDType) == "" {
		return missing("the kind of identification looked at")
	}
	if country == "US" {
		if strings.TrimSpace(p.TaxpayerID) == "" {
			return missing("the payer's taxpayer identification number")
		}
		if strings.TrimSpace(p.IDNumber) == "" {
			return missing("the identification number")
		}
	}
	return nil
}

// storePayer seals the sensitive values with the payment vault (the same
// AES-256-GCM envelope, never a cipher of our own) and writes the payer row.
func (s *Service) storePayer(ctx context.Context, f *CompletionFacts, country string) error {
	p := f.Payer
	if p == nil {
		return errors.New("the payer row needs a payer")
	}
	tinSealed := ""
	if tin := strings.TrimSpace(p.TaxpayerID); tin != "" {
		sealed, err := s.vault.Seal(tin)
		if err != nil {
			return err
		}
		tinSealed = sealed
	}
	idSealed := ""
	if num := strings.TrimSpace(p.IDNumber); num != "" {
		sealed, err := s.vault.Seal(num)
		if err != nil {
			return err
		}
		idSealed = sealed
	}
	dob := p.DateOfBirth
	return s.repo.InsertPayer(ctx, f.TransactionID, f.BranchID, f.CustomerID, country,
		strings.TrimSpace(p.Name), strings.TrimSpace(p.Address), strings.TrimSpace(p.Occupation),
		dob, p.IDType, idSealed, tinSealed, s.tinDigest(p.TaxpayerID), p.ThirdPartyPayer)
}
