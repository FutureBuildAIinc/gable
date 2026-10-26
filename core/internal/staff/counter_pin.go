// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staff

// The manager's counter PIN (CP-04 slice 3): the secret a manager types at
// the terminal to step up and approve one act. An admin sets it here on the
// staff screen; it is stored only as an argon2id hash with its own salt, and
// the step up verifies it in constant time. Five wrong tries in a row lock
// it until an admin unlocks it.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

// pinHashParams is the one argon2id shape the counter PIN uses: the same
// cost the API keys carry, plenty for a four to eight digit secret that
// locks after five tries.
const (
	pinTime    uint32 = 1
	pinMemory  uint32 = 64 * 1024
	pinThreads uint8  = 4
	pinKeyLen  uint32 = 32
	pinSaltLen        = 16
)

// ValidateCounterPIN refuses a PIN that is not four to eight digits, one
// repeated digit, or a plain run up or down (1234, 4321): the shapes a
// person leaning over a shoulder types first.
func ValidateCounterPIN(pin string) error {
	if len(pin) < 4 || len(pin) > 8 {
		return httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "pin", Message: "must be 4 to 8 digits"})
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "pin", Message: "must be 4 to 8 digits"})
		}
	}
	repeated := true
	runUp, runDown := true, true
	for i := 1; i < len(pin); i++ {
		d, prev := pin[i], pin[i-1]
		if d != prev {
			repeated = false
		}
		if d != prev+1 {
			runUp = false
		}
		if d != prev-1 {
			runDown = false
		}
	}
	if repeated || runUp || runDown {
		return httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "pin", Message: "must not be one repeated digit or a plain run like 1234"})
	}
	return nil
}

// HashCounterPIN hashes a PIN with argon2id and a fresh random salt. The
// stored shape is salt$hash, both base64 raw URL: no PIN and no hash ever
// travels or logs in the clear.
func HashCounterPIN(pin string) (string, error) {
	salt := make([]byte, pinSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pin), salt, pinTime, pinMemory, pinThreads, pinKeyLen)
	return base64.RawURLEncoding.EncodeToString(salt) + "$" + base64.RawURLEncoding.EncodeToString(key), nil
}

// VerifyCounterPIN checks a PIN against a stored hash in constant time. A
// malformed stored hash answers false: the caller refuses the step up the
// same way as a wrong PIN, never saying which part failed.
func VerifyCounterPIN(stored, pin string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 2 {
		return false
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	want, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pin), salt, pinTime, pinMemory, pinThreads, uint32(len(want)))
	return subtle.ConstantTimeCompare(want, got) == 1
}

// SetCounterPIN sets a staff member's counter PIN on the admin's word: the
// row is locked, the PIN validated and hashed, the failures and the lock
// reset, and the audit row written without the PIN. The staff answer says
// only counter_pin_set and locked.
func (s *Service) SetCounterPIN(ctx context.Context, id uuid.UUID, pin string, actor string) (*Staff, error) {
	if err := ValidateCounterPIN(pin); err != nil {
		return nil, err
	}
	hash, err := HashCounterPIN(pin)
	if err != nil {
		return nil, err
	}
	var out *Staff
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockStaff(ctx, id); err != nil {
			return notFound(err)
		}
		if err := s.repo.SetCounterPIN(ctx, id, hash); err != nil {
			return err
		}
		// the staff answer moves with the PIN facts, so a client holding the
		// document on its revision sees the change through the revision
		// (ADR 0001 section 11)
		if err := s.repo.BumpStaffRevision(ctx, id); err != nil {
			return err
		}
		cur, err := s.repo.Get(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.auditPIN(ctx, "staff.counter_pin_set", id, actor, map[string]any{}); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ClearCounterPIN removes the PIN whole: hash, set time, failures and lock.
// A cleared PIN is no PIN: the step up refuses the manager until an admin
// sets one again.
func (s *Service) ClearCounterPIN(ctx context.Context, id uuid.UUID, actor string) (*Staff, error) {
	var out *Staff
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockStaff(ctx, id); err != nil {
			return notFound(err)
		}
		if err := s.repo.ClearCounterPIN(ctx, id); err != nil {
			return err
		}
		if err := s.repo.BumpStaffRevision(ctx, id); err != nil {
			return err
		}
		cur, err := s.repo.Get(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.auditPIN(ctx, "staff.counter_pin_cleared", id, actor, map[string]any{}); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UnlockCounterPIN releases a PIN five failures locked: the lock and the
// failures go, the hash stands, so the manager's own PIN works again at
// once.
func (s *Service) UnlockCounterPIN(ctx context.Context, id uuid.UUID, actor string) (*Staff, error) {
	var out *Staff
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockStaff(ctx, id); err != nil {
			return notFound(err)
		}
		before, err := s.repo.Get(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.repo.UnlockCounterPIN(ctx, id); err != nil {
			return err
		}
		if err := s.repo.BumpStaffRevision(ctx, id); err != nil {
			return err
		}
		cur, err := s.repo.Get(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.auditPIN(ctx, "staff.counter_pin_unlocked", id, actor, map[string]any{"was_locked": before.CounterPinLocked}); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// auditPIN writes the PIN act's audit row. The changes never hold the PIN or
// its hash: the row says only that the act happened and who did it.
func (s *Service) auditPIN(ctx context.Context, action string, id uuid.UUID, actor string, changes map[string]any) error {
	if s.auditLog == nil {
		return nil
	}
	return s.auditLog.Log(ctx, audit.Entry{
		Action: action, EntityType: "staff", EntityID: id, UserID: actor, Changes: changes,
	})
}

// pinAnswer is the wire answer of the three PIN routes: the two facts the
// staff screen shows and nothing else.
type pinAnswer struct {
	CounterPinSet bool `json:"counter_pin_set"`
	Locked        bool `json:"locked"`
}
