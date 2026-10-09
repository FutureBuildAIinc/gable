// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"time"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/pkg/clientip"
	"github.com/joho/godotenv"
)

type Config struct {
	Port        string
	DatabaseURL string
	JWKSURL     string
	AuthIssuer  string

	// Run Payments Gateway
	RunPaymentsAPIKey      string
	RunPaymentsPublicKey   string
	RunPaymentsMID         string // Run merchant ID (the `mid` header)
	RunPaymentsBaseURL     string
	RunPaymentsEnvironment string // "sandbox" or "production"
	PaymentVaultKey        string // PAYMENT_VAULT_KEY: 32-byte hex; seals processor creds at rest

	// Avalara Sales Tax
	AvalaraAccountID   string
	AvalaraLicenseKey  string
	AvalaraEnvironment string // "sandbox" or "production"
	AvalaraCompanyCode string
	AvalaraBaseURL     string // AVALARA_BASE_URL: overrides the environment's URL (a self hosted AvaTax compatible endpoint, and the local stub of the wiring tests); empty uses the environment

	// Google Maps — deprecated, superseded by OpenRouteService (kept for one
	// back-compat release; no longer wired in main.go).
	GoogleMapsAPIKey string

	// OpenRouteService (routing optimization + geocoding). Primary source for
	// the key is system_settings/env; base URL and profile are env-tunable.
	ORSAPIKey  string // OPENROUTESERVICE_API_KEY
	ORSBaseURL string // OPENROUTESERVICE_BASE_URL (default https://api.openrouteservice.org)
	ORSProfile string // ORS_PROFILE (default driving-hgv — lumber trucks)

	// Twilio SMS
	TwilioAccountSID string
	TwilioAuthToken  string
	TwilioFromNumber string

	// OpenRouter (unified AI — one key for text, vision OCR, and image generation).
	// Primary source for the key/base-URL is system_settings (admin UI); env is the
	// fallback. Model slugs default to the ai package defaults when left empty.
	OpenRouterAPIKey  string // OPENROUTER_API_KEY
	OpenRouterBaseURL string // OPENROUTER_BASE_URL (swappable to a self-hosted vLLM/Ollama/LiteLLM endpoint)
	AIModelText       string // AI_MODEL_TEXT
	AIModelVision     string // AI_MODEL_VISION
	AIModelCheap      string // AI_MODEL_CHEAP
	AIModelImage      string // AI_MODEL_IMAGE

	// Auth & Security
	AuthMode string // "dev" to disable auth; otherwise JWKS_URL is required

	// TrustedProxies lists the reverse proxy networks whose X-Forwarded-For
	// is believed (TRUSTED_PROXIES, comma separated CIDRs or addresses).
	// Empty by default: no forwarding header is trusted and the client is the
	// TCP peer. A deployment behind a load balancer sets the balancer's network.
	TrustedProxies clientip.Trusted

	// RateLimitPerMinute is the per-address request limit on the ERP API
	// (RATE_LIMIT_PER_MINUTE, default 120; a value below 1 stops the server at
	// boot). The Playwright stack raises it:
	// the desk makes many calls per page and a whole suite shares one address.
	RateLimitPerMinute int

	// Logging
	LogLevel string // DEBUG, INFO, WARN, ERROR (default: INFO)

	// Events
	//
	// EventsOrg is the org slug stamped on every events_outbox row. Gable is
	// one database per dealer today, so the org is a deployment property:
	// set it when one deployment serves an org with a name worth reading on
	// the events feed. Defaults to "default".
	EventsOrg string // EVENTS_ORG

	// OutboxRetentionDays is how many days the worker role keeps
	// events_outbox rows (OUTBOX_RETENTION_DAYS, default 14). A row older
	// than this is deleted only once every registered subscriber cursor is at
	// or past it and no parked entry names it (ADR 0003 section 6). Zero or
	// a negative value turns the purge off; a value above
	// MaxOutboxRetentionDays is clamped to it. An outside consumer of GET
	// /api/v1/events that falls further behind than this loses the events
	// between its cursor and the oldest retained row.
	OutboxRetentionDays int // OUTBOX_RETENTION_DAYS

	// The draft change feed (ADR 0007 section 3.5). Zero or negative values
	// are refused at boot, except the retention, where zero or negative
	// turns the purge off (the outbox's rule). Serve assembles these into
	// the drafts package's FeedSettings.
	DraftFeedHeartbeat              time.Duration // DRAFT_FEED_HEARTBEAT, default 15s
	DraftFeedPoll                   time.Duration // DRAFT_FEED_POLL, default 1s
	DraftFeedBatch                  int           // DRAFT_FEED_BATCH, default 100
	DraftFeedWriteTimeout           time.Duration // DRAFT_FEED_WRITE_TIMEOUT, default 10s
	DraftFeedMaxLifetime            time.Duration // DRAFT_FEED_MAX_LIFETIME, default 15m
	DraftEventsRetention            time.Duration // DRAFT_EVENTS_RETENTION, default 7d; zero or negative turns the purge off
	DraftFeedMaxStreamsPerPrincipal int           // DRAFT_FEED_MAX_STREAMS_PER_PRINCIPAL, default 8
	DraftFeedMaxStreams             int           // DRAFT_FEED_MAX_STREAMS, default 500

	// EDI
	//
	// EDIOutputDir is where generated X12 documents are written. It defaults to
	// a path relative to the working directory rather than an absolute one:
	// the previous hardcoded "/app/edi_out" only exists inside the container
	// image, so every local and non-container self-host run failed to create it
	// with "permission denied" — logged and then ignored, leaving EDI output
	// silently broken. Container deployments set EDI_OUTPUT_DIR=/app/edi_out.
	EDIOutputDir string // EDI_OUTPUT_DIR

	// Database Pool
	DBMaxConns        int32 // Max open connections (default: 10)
	DBMinConns        int32 // Min idle connections (default: 2)
	DBMaxConnLifetime int   // Max connection lifetime in minutes (default: 60)

	// FutureBuild Brain Integration
	FBBrainEnabled        bool   // Global kill switch for Brain integration
	FBBrainBaseURL        string // Brain API base URL (e.g. https://ai-gateway.example.com)
	FBBrainIntegrationKey string // Shared secret for service-to-service X-Integration-Key auth
	FBBrainPublicKeyPath  string // Path to Brain's RSA public key PEM for A2A JWS verification
	FBBrainOrgID          string // Tenant org_id for Brain financial attribution
}

// MaxOutboxRetentionDays caps OUTBOX_RETENTION_DAYS (ten years), so the
// worker's days to Duration conversion cannot overflow.
const MaxOutboxRetentionDays = 3650

// validateDraftFeedSettings refuses a zero or negative setting at boot,
// except the retention, where zero or negative turns the purge off.
func validateDraftFeedSettings(cfg *Config) error {
	for _, tc := range []struct {
		name  string
		value time.Duration
	}{
		{"DRAFT_FEED_HEARTBEAT", cfg.DraftFeedHeartbeat},
		{"DRAFT_FEED_POLL", cfg.DraftFeedPoll},
		{"DRAFT_FEED_WRITE_TIMEOUT", cfg.DraftFeedWriteTimeout},
		{"DRAFT_FEED_MAX_LIFETIME", cfg.DraftFeedMaxLifetime},
	} {
		if tc.value <= 0 {
			return fmt.Errorf("invalid %s %s: must be a positive duration", tc.name, tc.value)
		}
	}
	if cfg.DraftFeedBatch < 1 {
		return fmt.Errorf("invalid DRAFT_FEED_BATCH %d: must be 1 or more", cfg.DraftFeedBatch)
	}
	if cfg.DraftFeedMaxStreamsPerPrincipal < 1 || cfg.DraftFeedMaxStreams < 1 {
		return fmt.Errorf("invalid DRAFT_FEED_MAX_STREAMS(_PER_PRINCIPAL): must be 1 or more")
	}
	return nil
}

func Load() (*Config, error) {
	_ = godotenv.Load() // Load .env if it exists, ignore if not

	cfg := &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://gable_user:gable_password@localhost:5434/gable_db?sslmode=disable"),
		JWKSURL:     getEnv("JWKS_URL", ""),
		AuthIssuer:  getEnv("AUTH_ISSUER", ""),

		EDIOutputDir: getEnv("EDI_OUTPUT_DIR", "edi_out"),

		// Run Payments — defaults to sandbox mode
		RunPaymentsAPIKey:      getEnv("RUN_PAYMENTS_API_KEY", ""),
		RunPaymentsPublicKey:   getEnv("RUN_PAYMENTS_PUBLIC_KEY", ""),
		RunPaymentsMID:         getEnv("RUN_PAYMENTS_MID", ""),
		RunPaymentsBaseURL:     getEnv("RUN_PAYMENTS_BASE_URL", ""),
		RunPaymentsEnvironment: getEnv("RUN_PAYMENTS_ENV", "sandbox"),
		PaymentVaultKey:        getEnv("PAYMENT_VAULT_KEY", ""),

		// Avalara Sales Tax — defaults to sandbox mode
		AvalaraAccountID:   getEnv("AVALARA_ACCOUNT_ID", ""),
		AvalaraLicenseKey:  getEnv("AVALARA_LICENSE_KEY", ""),
		AvalaraEnvironment: getEnv("AVALARA_ENV", "sandbox"),
		AvalaraCompanyCode: getEnv("AVALARA_COMPANY_CODE", ""),
		AvalaraBaseURL:     getEnv("AVALARA_BASE_URL", ""),

		// Google Maps — deprecated (see struct comment)
		GoogleMapsAPIKey: getEnv("GOOGLE_MAPS_API_KEY", ""),

		// OpenRouteService (routing + geocoding)
		ORSAPIKey:  getEnv("OPENROUTESERVICE_API_KEY", ""),
		ORSBaseURL: getEnv("OPENROUTESERVICE_BASE_URL", "https://api.openrouteservice.org"),
		ORSProfile: getEnv("ORS_PROFILE", "driving-hgv"),

		// Twilio SMS
		TwilioAccountSID: getEnv("TWILIO_ACCOUNT_SID", ""),
		TwilioAuthToken:  getEnv("TWILIO_AUTH_TOKEN", ""),
		TwilioFromNumber: getEnv("TWILIO_FROM_NUMBER", ""),

		// OpenRouter (unified AI). Empty model slugs fall back to the ai package
		// defaults, keeping a single source of truth for default model choices.
		OpenRouterAPIKey:  getEnv("OPENROUTER_API_KEY", ""),
		OpenRouterBaseURL: getEnv("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"),
		AIModelText:       getEnv("AI_MODEL_TEXT", ""),
		AIModelVision:     getEnv("AI_MODEL_VISION", ""),
		AIModelCheap:      getEnv("AI_MODEL_CHEAP", ""),
		AIModelImage:      getEnv("AI_MODEL_IMAGE", ""),

		// Auth & Security
		AuthMode: getEnv("AUTH_MODE", ""),

		// Logging
		LogLevel: getEnv("LOG_LEVEL", "INFO"),

		// Events
		EventsOrg:           getEnv("EVENTS_ORG", "default"),
		OutboxRetentionDays: getEnvInt("OUTBOX_RETENTION_DAYS", 14),

		// The draft change feed (ADR 0007 section 3.5), with its defaults.
		DraftFeedHeartbeat:              getEnvDuration("DRAFT_FEED_HEARTBEAT", 15*time.Second),
		DraftFeedPoll:                   getEnvDuration("DRAFT_FEED_POLL", time.Second),
		DraftFeedBatch:                  getEnvInt("DRAFT_FEED_BATCH", 100),
		DraftFeedWriteTimeout:           getEnvDuration("DRAFT_FEED_WRITE_TIMEOUT", 10*time.Second),
		DraftFeedMaxLifetime:            getEnvDuration("DRAFT_FEED_MAX_LIFETIME", 15*time.Minute),
		DraftEventsRetention:            getEnvDuration("DRAFT_EVENTS_RETENTION", 7*24*time.Hour),
		DraftFeedMaxStreamsPerPrincipal: getEnvInt("DRAFT_FEED_MAX_STREAMS_PER_PRINCIPAL", 8),
		DraftFeedMaxStreams:             getEnvInt("DRAFT_FEED_MAX_STREAMS", 500),

		RateLimitPerMinute: getEnvInt("RATE_LIMIT_PER_MINUTE", 120),

		// Database Pool
		DBMaxConns:        int32(getEnvInt("DB_MAX_CONNS", 10)),
		DBMinConns:        int32(getEnvInt("DB_MIN_CONNS", 2)),
		DBMaxConnLifetime: getEnvInt("DB_MAX_CONN_LIFETIME_MIN", 60),

		// FutureBuild Brain Integration
		FBBrainEnabled:        strings.EqualFold(getEnv("FB_BRAIN_ENABLED", "false"), "true"),
		FBBrainBaseURL:        getEnv("FB_BRAIN_BASE_URL", "http://localhost:8081"),
		FBBrainIntegrationKey: getEnv("FB_BRAIN_INTEGRATION_KEY", ""),
		FBBrainPublicKeyPath:  getEnv("FB_BRAIN_PUBLIC_KEY_PATH", ""),
		FBBrainOrgID:          getEnv("FB_BRAIN_ORG_ID", ""),
	}

	// F-05: Startup validation — fail fast if Brain is enabled but missing required config
	if cfg.FBBrainEnabled && cfg.FBBrainIntegrationKey == "" {
		return nil, fmt.Errorf("FB_BRAIN_ENABLED=true but FB_BRAIN_INTEGRATION_KEY is empty; cannot authenticate with Brain")
	}

	// Fail closed on an insecure AI base URL: the Bearer key must never be sent in
	// plaintext to an arbitrary host (https required, or http only for loopback).
	if err := ai.ValidateBaseURL(cfg.OpenRouterBaseURL); err != nil {
		return nil, fmt.Errorf("invalid OPENROUTER_BASE_URL: %w", err)
	}

	trusted, err := clientip.Parse(getEnv("TRUSTED_PROXIES", ""))
	if err != nil {
		return nil, fmt.Errorf("invalid TRUSTED_PROXIES: %w", err)
	}
	cfg.TrustedProxies = trusted

	if cfg.RateLimitPerMinute < 1 {
		return nil, fmt.Errorf("invalid RATE_LIMIT_PER_MINUTE %d: must be 1 or more", cfg.RateLimitPerMinute)
	}

	if cfg.OutboxRetentionDays > MaxOutboxRetentionDays {
		slog.Warn("OUTBOX_RETENTION_DAYS above the cap, using the cap", "value", cfg.OutboxRetentionDays, "cap", MaxOutboxRetentionDays)
		cfg.OutboxRetentionDays = MaxOutboxRetentionDays
	}

	if err := validateDraftFeedSettings(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if value, exists := os.LookupEnv(key); exists {
		d, err := time.ParseDuration(value)
		if err != nil {
			slog.Warn("Invalid duration env var, using default", "key", key, "value", value)
			return fallback
		}
		return d
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value, exists := os.LookupEnv(key); exists {
		n, err := strconv.Atoi(value)
		if err != nil {
			slog.Warn("Invalid integer env var, using default", "key", key, "value", value, "default", fallback)
			return fallback
		}
		return n
	}
	return fallback
}
