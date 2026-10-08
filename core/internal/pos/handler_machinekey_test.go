// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	glint "github.com/gablelbm/gable/internal/integrations/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// newHandlerMux builds the POS handler over the real service stack, exactly
// as cmd/server wires it, and registers its routes on a fresh mux.
func newHandlerMux(t *testing.T) *http.ServeMux {
	t.Helper()
	db := testutil.RequireDB(t)
	logger := slog.Default()

	glSvc := gl.NewService(gl.NewRepository(db), glint.NewMockGLAdapter(), logger)
	accountSvc := account.NewService(account.NewRepository(db), db, logger)
	invoiceSvc := invoice.NewService(invoice.NewRepository(db), glSvc, accountSvc, db).
		WithAuditLog(audit.NewLogger(db))
	paymentSvc := payment.NewService(db, payment.NewRepository(db), invoice.NewRepository(db), accountSvc).
		WithAuditLog(audit.NewLogger(db))
	svc := pos.NewService(db, pos.NewRepository(db),
		product.NewService(product.NewRepository(db)),
		inventory.NewService(inventory.NewRepository(db)),
		invoiceSvc, paymentSvc, logger).
		WithAuditLog(audit.NewLogger(db))

	mux := http.NewServeMux()
	pos.NewHandler(svc).RegisterRoutes(mux)
	return mux
}

// withMachineKey stands where the machine-key auth core sits: once a key
// validates it puts the key's id in the request context, and downstream
// handlers see it (the scopes the core also sets are irrelevant here, these
// routes have no scope-based branching of their own).
func withMachineKey(keyID string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(middleware.WithKeyID(r.Context(), keyID)))
	})
}

// keyRefusalBody decodes the refusal envelope the handler writes.
type keyRefusalBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
}

// TestMachineKeyNeverGetsFabricatedCashier drives the three POS routes whose
// handlers fall back to a fabricated cashier when no user identity is on the
// request. A machine key has no user identity, so before this fix a
// pos:write key reaching these routes had its sales and drawer sessions
// attributed to a random UUID no operator ever met. The routes now refuse a
// machine key with 403 forbidden and a message saying a cashier must be a
// user; callers without a key (dev mode's unauthenticated demo) keep the
// fallback.
func TestMachineKeyNeverGetsFabricatedCashier(t *testing.T) {
	mux := newHandlerMux(t)
	keyID := uuid.New().String()
	keyed := withMachineKey(keyID, mux)

	routes := []string{
		"/api/v1/pos/transactions",
		"/api/v1/pos/till/open",
		"/api/v1/pos/returns",
	}
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, route,
				strings.NewReader(`{"register_id":"KEYTEST-`+keyID[:8]+`"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Request-ID", "req-pos-key-test")
			rec := httptest.NewRecorder()
			keyed.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("machine key on %s: status = %d, want 403; body: %s", route, rec.Code, rec.Body.String())
			}
			var body keyRefusalBody
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode refusal envelope: %v (body: %s)", err, rec.Body.String())
			}
			if body.Error.Code != "forbidden" {
				t.Errorf("code = %q, want forbidden", body.Error.Code)
			}
			if !strings.Contains(body.Error.Message, "a cashier must be a user") {
				t.Errorf("message = %q, want it to say a cashier must be a user", body.Error.Message)
			}
		})
	}

	// The dev-mode control: the same routes without a key keep serving their
	// unauthenticated callers (the fabricated stand-in), so the demo POS
	// still works; only the keyed caller is refused.
	for _, route := range routes {
		t.Run("dev "+route, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, route,
				strings.NewReader(`{"register_id":"KEYTEST-DEV-`+keyID[:8]+`"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code == http.StatusForbidden {
				t.Fatalf("keyless dev caller on %s: status 403, want the dev fallback (body: %s)", route, rec.Body.String())
			}
		})
	}
}
