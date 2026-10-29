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

// Service is the large cash capture, classification and aggregation.
type Service struct {
	db    *database.DB
	repo  *Repository
	vault *payment.Vault
	// keyHex is the vault key as hex, the HMAC key of the TIN digest. Empty
	// in an unconfigured dev stack, where the digest falls back to a plain
	// SHA-256.
	keyHex string
	now    func() time.Time
}

// NewService builds the large cash service. vault may be nil (plaintext
// passthrough, as the payment vault does when no key is configured).
func NewService(db *database.DB, vault *payment.Vault, keyHex string) *Service {
	if vault == nil {
		vault, _ = payment.NewVault("")
	}
	return &Service{db: db, repo: NewRepository(db), vault: vault, keyHex: strings.TrimSpace(keyHex), now: time.Now}
}

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

// compute classifies the sale's tenders under the branch's rule and reads
// the payer's running aggregate. It takes the payer's advisory lock first,
// so two registers crossing the threshold for one payer at once serialize
// and the second sees the first's tenders.
func (s *Service) compute(ctx context.Context, f *CompletionFacts) (country string, agg *aggregate, decisions []Decision, counted int64, err error) {
	country, err = s.repo.BranchCountry(ctx, f.BranchID)
	if err != nil {
		return "", nil, nil, 0, err
	}
	sale := SaleFacts{Country: country, SuspectedAvoidance: f.SuspectedAvoidance}
	if country == "US" {
		// the consumer durable test is at the sale line: a product flagged
		// consumer durable whose line is priced over the threshold
		durable, err := s.repo.AnyConsumerDurableLine(ctx, f.Lines, ThresholdCents)
		if err != nil {
			return "", nil, nil, 0, err
		}
		sale.ConsumerDurableOverThreshold = durable
	}
	for i := range f.Tenders {
		d := Classify(sale, f.Tenders[i])
		decisions = append(decisions, d)
		if d.Counts {
			counted += d.CountedCents
		}
	}
	if counted == 0 {
		return country, nil, decisions, 0, nil
	}
	payerKey := s.payerKey(f)
	if err := s.repo.AdvisoryLock(ctx, payerKey); err != nil {
		return "", nil, nil, 0, err
	}
	switch country {
	case "US":
		base, ids, n, err := s.repo.USSinceLastEvent(ctx, payerKey, s.now().AddDate(-1, 0, 0))
		if err != nil {
			return "", nil, nil, 0, err
		}
		return country, &aggregate{rule: RuleUS, threshold: ThresholdCents, comparator: "greater",
			base: base, txIDs: ids, tenderCount: n}, decisions, counted, nil
	case "CA":
		st, err := s.ReadSettings(ctx)
		if err != nil {
			return "", nil, nil, 0, err
		}
		if !st.CAEnabled {
			// the dealer threshold is off: nothing is counted or recorded
			return country, nil, decisions, 0, nil
		}
		start, end, err := s.repo.BranchDayWindow(ctx, f.BranchID, s.now())
		if err != nil {
			return "", nil, nil, 0, err
		}
		base, ids, n, err := s.repo.CAInWindow(ctx, payerKey, start, end)
		if err != nil {
			return "", nil, nil, 0, err
		}
		return country, &aggregate{rule: RuleCA, threshold: st.CAThresholdCents, comparator: "at_or_above",
			base: base, txIDs: ids, tenderCount: n}, decisions, counted, nil
	}
	return country, nil, decisions, 0, nil
}

// Preview answers what the counter asks before Pay. It writes nothing.
func (s *Service) Preview(ctx context.Context, f *CompletionFacts) (*Preview, error) {
	country, agg, _, counted, err := s.compute(ctx, f)
	if err != nil {
		return nil, err
	}
	p := &Preview{Country: country, CountedCents: counted, ThresholdCents: ThresholdCents,
		Comparator: "greater", Fields: []string{}}
	switch {
	case agg != nil && agg.rule == RuleUS:
		p.RuleFamily = RuleUS
		p.AggregateCents = agg.base + counted
		p.ThresholdCents = agg.threshold
		p.AsksDetails = Crosses(RuleUS, p.AggregateCents, agg.threshold)
	case agg != nil && agg.rule == RuleCA:
		p.RuleFamily = RuleCA
		p.AggregateCents = agg.base + counted
		p.ThresholdCents = agg.threshold
		p.Comparator = "at_or_above"
		p.DealerThreshold = true
		p.Note = CanadaNote
		p.AsksDetails = Crosses(RuleCA, p.AggregateCents, agg.threshold)
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
// payer_details_required and the whole completion refuses.
func (s *Service) Capture(ctx context.Context, f *CompletionFacts) error {
	country, agg, decisions, counted, err := s.compute(ctx, f)
	if err != nil {
		return err
	}
	if country != "US" && country != "CA" {
		return nil
	}
	for i, d := range decisions {
		if !d.Counts {
			continue
		}
		if err := s.repo.InsertTender(ctx, f.TransactionID, f.BranchID, d.RuleFamily,
			s.payerKey(f), d.CountedCents, d.Instrument, d.Reason, i); err != nil {
			return err
		}
	}
	if agg == nil || !Crosses(agg.rule, agg.base+counted, agg.threshold) {
		return nil
	}
	// the crossing: the payer's details are the condition of the completion
	if err := s.checkPayer(country, f.Payer); err != nil {
		return err
	}
	if err := s.storePayer(ctx, f, country); err != nil {
		return err
	}
	txIDs := append(agg.txIDs, f.TransactionID)
	seen := map[uuid.UUID]bool{}
	uniq := make([]uuid.UUID, 0, len(txIDs))
	for _, id := range txIDs {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	state := "reportable"
	if agg.rule == RuleCA {
		state = "recorded"
	}
	if err := s.repo.InsertEvent(ctx, s.payerKey(f), f.CustomerID, f.BranchID, agg.rule, f.Currency,
		counted, agg.base+counted, uniq, agg.tenderCount+1, state); err != nil {
		return err
	}
	// The crossing is audited in the completion's transaction. The row names
	// the rule and the figures and that a payer was captured, never an
	// identity value (ADR 0003).
	return audit.NewLogger(s.db).Log(ctx, audit.Entry{
		Action: "large_cash.crossing.recorded", EntityType: "pos_transaction", EntityID: f.TransactionID, UserID: f.Actor,
		Changes: map[string]interface{}{
			"rule_family": agg.rule, "state": state, "branch_id": f.BranchID.String(),
			"counted_cents": counted, "total_cents": agg.base + counted, "payer_captured": true,
		}})
}

// payerKey is the aggregation key: the named customer, else the captured
// taxpayer identification number's digest (the walk-in's strong key), else
// nothing aggregates.
func (s *Service) payerKey(f *CompletionFacts) string {
	if f.CustomerID != nil {
		return "cus:" + f.CustomerID.String()
	}
	if f.Payer != nil && strings.TrimSpace(f.Payer.TaxpayerID) != "" {
		return "tin:" + s.tinDigest(f.Payer.TaxpayerID)
	}
	return "walk:" + f.TransactionID.String()
}

// tinDigest is the HMAC-SHA256 of a normalized taxpayer identification
// number, keyed with the vault key so the digest cannot be brute forced
// off the row. Without a configured key (a dev stack) it is a plain
// SHA-256, which the sealed column's presence still protects.
func (s *Service) tinDigest(tin string) string {
	normalized := strings.ToUpper(strings.Join(strings.Fields(tin), ""))
	if normalized == "" {
		return ""
	}
	if s.keyHex != "" {
		key, err := hex.DecodeString(s.keyHex)
		if err == nil && len(key) == 32 {
			m := hmac.New(sha256.New, key)
			m.Write([]byte("tin:" + normalized))
			return hex.EncodeToString(m.Sum(nil))
		}
	}
	sum := sha256.Sum256([]byte("tin:" + normalized))
	return hex.EncodeToString(sum[:])
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
