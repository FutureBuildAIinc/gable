// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

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
	// events_outbox rows (OUTBOX_RETENTION_DAYS, default 14). A row older than
	// this is deleted only once every registered subscriber cursor is at or
	// past it and no parked entry names it (ADR 0003 section 6). Zero or a
	// negative value turns the purge off. An outside consumer of GET
	// /api/v1/events that falls further behind than this loses the events
	// between its cursor and the oldest retained row.
	OutboxRetentionDays int // OUTBOX_RETENTION_DAYS

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

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
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
