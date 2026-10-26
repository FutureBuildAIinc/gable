// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"database/sql"
	"errors"
	"log"
	"os"
	"strings"
	"time"
)

// The seed once guard.
//
// The demo seed truncates the transactional tables before it writes (see
// AGENTS.md, "Seed resets transactional data each run"). That is right for a
// developer's local stack, where a fresh demo on every start is the point. It
// is wrong for a demo tenant that lives behind a deployment platform: the
// platform runs the seed on every deploy, so every redeploy of the same
// commit would wipe the demo the prospect was looking at.
//
// The guard gives the seed a memory. The seed writes a marker when it
// finishes, and a later run finds the marker and does nothing.
//
// DEMO_SEED_ONCE=1 turns the guard on. It is off by default, so the local
// stack (docker-compose.yml) keeps repaving the demo data on every `make up`
// the way it always has. The deploy recipe (deploy/compose.yml) turns it on.
//
// DEMO_SEED_FORCE=1 asks for a reseed whatever the marker says: the operator
// wants the demo back at a known state and says so out loud.
const (
	// seedOnceEnv turns the guard on.
	seedOnceEnv = "DEMO_SEED_ONCE"
	// seedForceEnv asks for a reseed even when the marker is there.
	seedForceEnv = "DEMO_SEED_FORCE"
	// seedMarkerKey is the system_settings key the seed writes when it
	// finishes. system_settings is the table migration 039 already creates,
	// and it is not one of the tables the seed truncates, so the marker
	// survives the seed's own reset.
	seedMarkerKey = "demo.seeded_at"
)

// seedSkipMessage is the start of the line the seed prints when the guard
// skips the run. It is a constant so the test can name the line it expects.
const seedSkipMessage = "Seed: the demo seed already ran"

// seedOnceRequested reports whether the operator asked for the once guard.
func seedOnceRequested() bool {
	return envIsTrue(seedOnceEnv)
}

// seedForced reports whether the operator asked for a reseed.
func seedForced() bool {
	return envIsTrue(seedForceEnv)
}

// envIsTrue reports whether an environment value is an affirmative one.
// Anything else, an empty value included, means no.
func envIsTrue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// seedMarkerValue reads the marker and answers its value and whether it is
// there at all. A marker that cannot be read counts as no marker: the seed
// runs, and the operator sees the reason in the log.
func seedMarkerValue(db *sql.DB) (string, bool) {
	var value string
	err := db.QueryRow(`SELECT value FROM system_settings WHERE key = $1`, seedMarkerKey).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false
		}
		log.Printf("Seed: reading the %s marker: %v", seedMarkerKey, err)
		return "", false
	}
	return value, true
}

// writeSeedMarker records that the seed finished. It is the last thing the
// seed does, so a run that failed part way leaves no marker and the next run
// tries again.
func writeSeedMarker(db *sql.DB) {
	value := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO system_settings (key, value, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
		seedMarkerKey, value)
	if err != nil {
		// The marker is the seed's memory, not its data: a failure to write
		// it must not lose the demo that was just seeded. It is logged
		// loudly because the next run will seed again.
		log.Printf("Seed: writing the %s marker: %v", seedMarkerKey, err)
	}
}
