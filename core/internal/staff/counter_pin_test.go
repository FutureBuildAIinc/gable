// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staff_test

// The counter PIN (CP-04 slice 3): a manager's step up secret, set on the
// staff screen by an admin, hashed with argon2id per PIN, never stored or
// audited in the clear. Five wrong tries in a row lock it until an admin
// unlocks it.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/staff"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

func pinStaff(t *testing.T, email string) *staff.Staff {
	t.Helper()
	db := testutil.RequireDB(t)
	svc := staff.NewService(staff.NewRepository(db)).WithTxRunner(db)
	created, err := svc.CreateStaff(context.Background(), &staff.ParsedCreate{
		Email: email, FullName: "Counter Manager", StaffNo: stringPtrOf("MGR-" + uuid.NewString()[:8]), Role: "admin", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropStaff(t, db, created.ID) })
	return created
}

// RULE (CP-04): a set PIN is stored as its argon2id hash with its own salt,
// never the PIN itself, and the audit row of the set names the act without
// the PIN. A PIN of one repeated digit or a plain run is refused, as is one
// outside four to eight digits.
func TestCP04_SetAndClearCounterPIN(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := staff.NewService(staff.NewRepository(db)).WithTxRunner(db).WithAuditLog(audit.NewLogger(db))
	st := pinStaff(t, "pin-set-"+uuid.NewString()[:8]+"@example.com")

	// weak PINs are refused before anything is written
	for _, weak := range []string{"", "12", "123", "123456789", "12a4", "1111", "0000", "1234", "4321", "23456"} {
		if _, err := svc.SetCounterPIN(ctx, st.ID, weak, "admin-sub"); err == nil {
			t.Errorf("SetCounterPIN(%q) succeeded, want refused", weak)
		}
	}
	var hash *string
	var setAt *time.Time
	var failures int
	var locked bool
	if err := db.Pool.QueryRow(ctx, `SELECT counter_pin_hash FROM staff WHERE id = $1`, st.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != nil && *hash != "" {
		t.Error("a refused PIN wrote a hash")
	}

	// a good PIN is stored hashed, never the PIN, with its own salt
	st, err := svc.SetCounterPIN(ctx, st.ID, "4481", "admin-sub")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT counter_pin_hash, counter_pin_set_at, counter_pin_failures, counter_pin_locked
		FROM staff WHERE id = $1`, st.ID).Scan(&hash, &setAt, &failures, &locked); err != nil {
		t.Fatal(err)
	}
	if hash == nil || *hash == "" {
		t.Fatal("no hash stored for the set PIN")
	}
	if strings.Contains(*hash, "4481") {
		t.Errorf("the stored hash = %q, want an argon2id hash that is not the PIN", *hash)
	}
	storedHash := *hash
	if setAt == nil || failures != 0 || locked {
		t.Error("after the set the row lacks set_at, or carries failures or the lock")
	}
	if !st.CounterPinSet || st.CounterPinLocked {
		t.Errorf("the staff answer says counter_pin_set=%v locked=%v, want true false", st.CounterPinSet, st.CounterPinLocked)
	}
	if st.Revision != 2 {
		t.Errorf("revision after first SetCounterPIN = %d, want 2", st.Revision)
	}

	// a second set of a different PIN stores a different salt and hash
	if _, err := svc.SetCounterPIN(ctx, st.ID, "9173", "admin-sub"); err != nil {
		t.Fatal(err)
	}
	var again string
	if err := db.Pool.QueryRow(ctx, `SELECT counter_pin_hash FROM staff WHERE id = $1`, st.ID).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again == storedHash {
		t.Error("two PINs hashed to the same stored value, want a per PIN salt")
	}

	// the audit row names the act and never the PIN
	var changesRaw []byte
	if err := db.Pool.QueryRow(ctx,
		`SELECT changes FROM audit_log WHERE action = 'staff.counter_pin_set' AND entity_id = $1 ORDER BY created_at DESC LIMIT 1`,
		st.ID).Scan(&changesRaw); err != nil {
		t.Fatalf("no audit row for the PIN set: %v", err)
	}
	if strings.Contains(string(changesRaw), "4481") || strings.Contains(string(changesRaw), "9173") {
		t.Errorf("the audit row holds the PIN: %s", changesRaw)
	}

	// verification is constant time and answers both ways
	if !staff.VerifyCounterPIN(again, "9173") {
		t.Error("VerifyCounterPIN refused the right PIN")
	}
	if staff.VerifyCounterPIN(again, "4481") {
		t.Error("VerifyCounterPIN accepted the old PIN")
	}

	// the clear empties every PIN column
	cleared, err := svc.ClearCounterPIN(ctx, st.ID, "admin-sub")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.CounterPinSet || cleared.CounterPinLocked {
		t.Errorf("after the clear counter_pin_set=%v locked=%v, want false false", cleared.CounterPinSet, cleared.CounterPinLocked)
	}
	if cleared.Revision != 4 {
		t.Errorf("revision after ClearCounterPIN = %d, want 4", cleared.Revision)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT counter_pin_hash, counter_pin_set_at, counter_pin_failures, counter_pin_locked
		FROM staff WHERE id = $1`, st.ID).Scan(&hash, &setAt, &failures, &locked); err != nil {
		t.Fatal(err)
	}
	if hash != nil || setAt != nil || failures != 0 || locked {
		t.Error("the clear left PIN state behind")
	}
	if n := countRows(t, db, `SELECT count(*) FROM audit_log WHERE action = 'staff.counter_pin_cleared' AND entity_id = $1`, st.ID); n != 1 {
		t.Errorf("%d audit rows for the clear, want 1", n)
	}
}

// RULE: a locked PIN stays locked until an admin unlocks it; the unlock
// clears the failures and the lock, and leaves a fresh set to be done or the
// old hash gone (the unlock never reveals or resets the PIN itself).
func TestCP04_UnlockCounterPIN(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := staff.NewService(staff.NewRepository(db)).WithTxRunner(db).WithAuditLog(audit.NewLogger(db))
	st := pinStaff(t, "pin-unlock-"+uuid.NewString()[:8]+"@example.com")
	if _, err := svc.SetCounterPIN(ctx, st.ID, "5629", "admin-sub"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx,
		`UPDATE staff SET counter_pin_failures = 5, counter_pin_locked = TRUE WHERE id = $1`, st.ID); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetStaff(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CounterPinLocked {
		t.Fatal("the locked flag did not read back")
	}
	unlocked, err := svc.UnlockCounterPIN(ctx, st.ID, "admin-sub")
	if err != nil {
		t.Fatal(err)
	}
	if unlocked.CounterPinLocked || !unlocked.CounterPinSet {
		t.Errorf("after the unlock locked=%v set=%v, want false true (the hash stands)", unlocked.CounterPinLocked, unlocked.CounterPinSet)
	}
	if unlocked.Revision != 3 {
		t.Errorf("revision after UnlockCounterPIN = %d, want 3", unlocked.Revision)
	}
	var failures int
	var locked bool
	if err := db.Pool.QueryRow(ctx, `SELECT counter_pin_failures, counter_pin_locked FROM staff WHERE id = $1`, st.ID).Scan(&failures, &locked); err != nil {
		t.Fatal(err)
	}
	if failures != 0 || locked {
		t.Error("the unlock left the failures or the lock")
	}
	if n := countRows(t, db, `SELECT count(*) FROM audit_log WHERE action = 'staff.counter_pin_unlocked' AND entity_id = $1 AND (changes->>'was_locked')::boolean = TRUE`, st.ID); n != 1 {
		t.Errorf("%d audit rows for the unlock with was_locked=true, want 1", n)
	}
	// unlocking again when not locked records was_locked=false
	if _, err := svc.UnlockCounterPIN(ctx, st.ID, "admin-sub"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM audit_log WHERE action = 'staff.counter_pin_unlocked' AND entity_id = $1 AND (changes->>'was_locked')::boolean = FALSE`, st.ID); n != 1 {
		t.Errorf("%d audit rows for the unlock with was_locked=false, want 1", n)
	}
}

// The PIN answer on the wire says only the two facts, never the hash.
func TestCP04_PINAnswerShape(t *testing.T) {
	st := pinStaff(t, "pin-shape-"+uuid.NewString()[:8]+"@example.com")
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "counter_pin_hash") {
		t.Errorf("the staff answer holds the hash: %s", raw)
	}
	if !strings.Contains(string(raw), `"counter_pin_set":false`) || !strings.Contains(string(raw), `"locked":false`) {
		t.Errorf("the staff answer lacks the two PIN facts: %s", raw)
	}
}

func stringPtrOf(s string) *string { return &s }
